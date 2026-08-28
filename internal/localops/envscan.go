package localops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/envdef"
	"github.com/3xDevOps/Aether/internal/envprompt"
	"github.com/3xDevOps/Aether/internal/harness"
)

// Scan modes. Inventory is a first run against the machine; repo derives
// the environment from a repository's own files instead; refine reruns
// the agent over a previous Dockerfile and manifest pair with feedback.
const (
	ScanModeInventory = "inventory"
	ScanModeRepo      = "repo"
	ScanModeRefine    = "refine"
)

// Coarse scan statuses reported through the progress callback, in the
// order a run moves through them. A retry re-enters running after
// retrying.
const (
	ScanStatusRunning    = "running"
	ScanStatusValidating = "validating"
	ScanStatusRetrying   = "retrying"
)

// defaultScanTimeout bounds one harness invocation when the caller sets
// no timeout.
const defaultScanTimeout = 10 * time.Minute

// scanTailLines is how many trailing output lines a failure carries for
// diagnosis.
const scanTailLines = 40

// HarnessStatus is one setup-capable harness's local availability.
type HarnessStatus struct {
	Name string `json:"name"`
	// Installed reports whether the harness executable is on PATH.
	Installed bool `json:"installed"`
}

// DetectHarnesses reports, for each setup-capable harness in setup order,
// whether its executable is on this machine's PATH. Plain PATH lookup, no
// agent involved.
func DetectHarnesses() []HarnessStatus {
	profiles := harness.SetupHarnesses()
	out := make([]HarnessStatus, 0, len(profiles))
	for _, p := range profiles {
		_, err := exec.LookPath(p.HeadlessArgs[0])
		out = append(out, HarnessStatus{Name: p.Name, Installed: err == nil})
	}
	return out
}

// ScanEvent is one progress report from a running scan: a coarse status
// change or one raw line of agent output, never both.
type ScanEvent struct {
	Status string
	Line   string
}

// ScanOptions parameterizes one RunScan call.
type ScanOptions struct {
	// Harness names the setup-capable harness to run, or "fake" for a
	// canned pair that exercises the flow without a vendor CLI.
	Harness string
	// Mode is ScanModeInventory, ScanModeRepo, or ScanModeRefine.
	Mode string
	// RepoPath is the repository folder a repo scan reads. Required when
	// Mode is ScanModeRepo; set on a refine run when the pair being
	// refined came from a repo scan, so the agent can read the repository
	// again. The agent runs with the repository as its working directory
	// but writes its output into the scratch directory, and the scan
	// fails if the repository's git status changes during the run.
	RepoPath string
	// PreviousDockerfile, PreviousManifestJSON, and Feedback seed a refine
	// run; ignored for inventory.
	PreviousDockerfile   string
	PreviousManifestJSON string
	Feedback             string
	// Argv overrides the harness's headless argv template; the task
	// placeholder is substituted with the rendered prompt. Tests and the
	// stub-driven gateway tests use it.
	Argv []string
	// Timeout bounds each harness invocation; zero means the default ten
	// minutes.
	Timeout time.Duration
}

// ScanResult is a validated inventory: the Dockerfile, the raw manifest
// text as the agent wrote it, and the parsed items.
type ScanResult struct {
	Dockerfile   string
	ManifestJSON string
	Manifest     []domain.ManifestItem
}

// ScanFailure is a scan that ended without a valid pair. OutputTail holds
// the last lines the agent printed, for diagnosis.
type ScanFailure struct {
	Err        error
	OutputTail string
}

func (f *ScanFailure) Error() string { return f.Err.Error() }
func (f *ScanFailure) Unwrap() error { return f.Err }

// RunScan runs one environment inventory on this machine: it renders the
// versioned prompt, runs the chosen harness headless in a scratch
// directory under a hard timeout, validates the Dockerfile and
// manifest.json the agent wrote, and retries once with the validation
// error appended to the prompt before giving up. The scratch directory is
// removed after every attempt, success or failure. progress may be nil;
// when set it is called serially, so callers need no locking. Failures
// are returned as *ScanFailure carrying the last output lines.
func RunScan(ctx context.Context, opts ScanOptions, progress func(ScanEvent)) (*ScanResult, error) {
	emit := serialEmitter(progress)
	repoPath, err := scanRepoPath(opts)
	if err != nil {
		return nil, err
	}
	if opts.Harness == "fake" {
		if opts.Mode == ScanModeRepo {
			return fakeScan(fakeRepoDockerfile, fakeRepoManifestJSON, "fake harness: returning the canned repo pair", emit)
		}
		return fakeScan(fakeScanDockerfile, fakeScanManifestJSON, "fake harness: returning the canned inventory", emit)
	}
	argv, err := scanArgvTemplate(opts)
	if err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultScanTimeout
	}

	var lastErr error
	var lastTail string
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			emit(ScanEvent{Status: ScanStatusRetrying})
		}
		scratch, mkErr := os.MkdirTemp("", "aether-envscan-")
		if mkErr != nil {
			return nil, fmt.Errorf("localops: create scan scratch directory: %w", mkErr)
		}
		prompt, promptErr := renderScanPrompt(opts, scratch)
		if promptErr != nil {
			_ = os.RemoveAll(scratch)
			return nil, promptErr
		}
		if lastErr != nil {
			prompt = retryPrompt(prompt, lastErr)
		}
		result, tail, attemptErr, retryable := scanAttempt(ctx, argv, prompt, scratch, repoPath, timeout, emit)
		_ = os.RemoveAll(scratch)
		if attemptErr == nil {
			return result, nil
		}
		if !retryable {
			return nil, &ScanFailure{Err: attemptErr, OutputTail: tail}
		}
		lastErr, lastTail = attemptErr, tail
	}
	return nil, &ScanFailure{
		Err:        fmt.Errorf("the agent's output failed validation twice: %w", lastErr),
		OutputTail: lastTail,
	}
}

// scanRepoPath returns the repository a run is anchored in: required and
// validated for repo mode, honored on refine runs whose pair came from a
// repo scan, empty otherwise.
func scanRepoPath(opts ScanOptions) (string, error) {
	switch {
	case opts.Mode == ScanModeRepo,
		opts.Mode == ScanModeRefine && opts.RepoPath != "":
		if err := validateRepoPath(opts.RepoPath); err != nil {
			return "", err
		}
		return opts.RepoPath, nil
	default:
		return "", nil
	}
}

// validateRepoPath checks the folder a repo-anchored run reads before
// anything launches. Each failure names the path so surfaces can show the
// message next to the folder input.
func validateRepoPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("localops: a repo scan needs the repository's folder")
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("localops: the folder %s does not exist", path)
	case err != nil:
		return fmt.Errorf("localops: check the folder %s: %w", path, err)
	case !info.IsDir():
		return fmt.Errorf("localops: %s is not a folder", path)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return fmt.Errorf("localops: %s is not a git repository (it has no .git entry)", path)
	}
	return nil
}

// repoStatus captures the repository's git status so the engine can prove
// a scan left the repository untouched.
func repoStatus(ctx context.Context, repo string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("localops: git status in %s: %w: %s", repo, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("localops: git status in %s: %w", repo, err)
	}
	return string(out), nil
}

// scanArgvTemplate resolves the argv template a scan instantiates: the
// override when set, otherwise the setup-capable harness's headless argv.
func scanArgvTemplate(opts ScanOptions) ([]string, error) {
	if len(opts.Argv) > 0 {
		return opts.Argv, nil
	}
	for _, p := range harness.SetupHarnesses() {
		if p.Name == opts.Harness {
			return p.HeadlessArgs, nil
		}
	}
	return nil, fmt.Errorf("localops: harness %q is not available for environment setup", opts.Harness)
}

// renderScanPrompt renders the versioned prompt for the scan's mode.
// Repo-anchored runs name scratch as the output directory, since their
// working directory is the repository the agent must never write into.
func renderScanPrompt(opts ScanOptions, scratch string) (string, error) {
	switch opts.Mode {
	case ScanModeInventory:
		return envprompt.RenderInventory(envprompt.InventoryParams{BaseImage: envdef.BaseImage})
	case ScanModeRepo:
		return envprompt.RenderRepo(envprompt.RepoParams{BaseImage: envdef.BaseImage, OutputDir: scratch})
	case ScanModeRefine:
		params := envprompt.RefineParams{
			Dockerfile:   opts.PreviousDockerfile,
			ManifestJSON: opts.PreviousManifestJSON,
			Feedback:     opts.Feedback,
		}
		if opts.RepoPath != "" {
			params.OutputDir = scratch
		}
		return envprompt.RenderRefine(params)
	default:
		return "", fmt.Errorf("localops: unknown scan mode %q (want %s, %s, or %s)", opts.Mode, ScanModeInventory, ScanModeRepo, ScanModeRefine)
	}
}

// retryPrompt appends the validation error to the prompt for the one
// retry the contract allows.
func retryPrompt(prompt string, validationErr error) string {
	return prompt + "\n\nYour previous attempt failed validation:\n" +
		validationErr.Error() +
		"\n\nCorrect these problems and write both files again."
}

// scanAttempt runs the harness once and validates what it wrote into
// scratch. The caller owns scratch. repoPath, when set, becomes the
// working directory and its git status must survive the run unchanged.
// retryable marks contract violations (missing or invalid output files)
// that earn the one retry; execution failures (timeout, crash, unrunnable
// command) and a modified repository do not.
func scanAttempt(ctx context.Context, argvTemplate []string, prompt, scratch, repoPath string, timeout time.Duration, emit func(ScanEvent)) (result *ScanResult, tail string, err error, retryable bool) {
	workDir := scratch
	var statusBefore string
	if repoPath != "" {
		workDir = repoPath
		statusBefore, err = repoStatus(ctx, repoPath)
		if err != nil {
			return nil, "", err, false
		}
	}

	tail, err = runScanCommand(ctx, argvTemplate, prompt, workDir, timeout, emit)
	if err != nil {
		return nil, tail, err, false
	}

	if repoPath != "" {
		statusAfter, statusErr := repoStatus(ctx, repoPath)
		if statusErr != nil {
			return nil, tail, statusErr, false
		}
		if statusAfter != statusBefore {
			return nil, tail, fmt.Errorf("localops: the scan modified the repository at %s, so its output was discarded", repoPath), false
		}
	}

	emit(ScanEvent{Status: ScanStatusValidating})
	result, err = collectScanOutput(scratch)
	if err != nil {
		return nil, tail, err, true
	}
	return result, tail, nil, false
}

// runScanCommand executes one harness invocation in workDir, streaming
// combined output lines through emit and returning the output tail.
func runScanCommand(ctx context.Context, argvTemplate []string, prompt, workDir string, timeout time.Duration, emit func(ScanEvent)) (string, error) {
	argv := harness.Argv(argvTemplate, prompt)
	if len(argv) == 0 {
		return "", fmt.Errorf("localops: scan argv is empty")
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tail := &tailBuffer{limit: scanTailLines}
	output := &lineWriter{emit: func(line string) {
		tail.add(line)
		emit(ScanEvent{Line: line})
	}}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Stdout = output
	cmd.Stderr = output
	// A harness may leave children holding the output pipe; do not let
	// Wait hang on them after the process itself is gone.
	cmd.WaitDelay = 5 * time.Second

	emit(ScanEvent{Status: ScanStatusRunning})
	err := cmd.Run()
	output.flush()
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		return tail.String(), fmt.Errorf("localops: the scan timed out after %s", timeout)
	case ctx.Err() != nil:
		return tail.String(), fmt.Errorf("localops: the scan was canceled: %w", ctx.Err())
	case err != nil:
		return tail.String(), fmt.Errorf("localops: run %s: %w", argv[0], err)
	}
	return tail.String(), nil
}

// collectScanOutput reads and validates the two files the output contract
// requires the agent to write into the scratch directory.
func collectScanOutput(scratch string) (*ScanResult, error) {
	dockerfile, err := readContractFile(scratch, "Dockerfile")
	if err != nil {
		return nil, err
	}
	manifestJSON, err := readContractFile(scratch, "manifest.json")
	if err != nil {
		return nil, err
	}
	items, err := envdef.ParseManifest(manifestJSON)
	if err != nil {
		return nil, err
	}
	if err := envdef.ValidateDockerfile(string(dockerfile), items); err != nil {
		return nil, err
	}
	return &ScanResult{
		Dockerfile:   string(dockerfile),
		ManifestJSON: string(manifestJSON),
		Manifest:     items,
	}, nil
}

func readContractFile(scratch, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(scratch, name))
	if err != nil {
		return nil, fmt.Errorf("the agent did not write %s into its working directory: %w", name, err)
	}
	return data, nil
}

// serialEmitter wraps progress so concurrent output copying and the scan
// loop never call it at the same time; a nil progress becomes a no-op.
func serialEmitter(progress func(ScanEvent)) func(ScanEvent) {
	if progress == nil {
		return func(ScanEvent) {}
	}
	var mu sync.Mutex
	return func(e ScanEvent) {
		mu.Lock()
		defer mu.Unlock()
		progress(e)
	}
}

// lineWriter splits a stream into lines for the progress feed. exec.Cmd
// serializes writes when the same writer serves stdout and stderr, so no
// locking is needed here.
type lineWriter struct {
	buf  []byte
	emit func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.emit(strings.TrimSuffix(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
}

// flush emits a trailing unterminated line after the process exits.
func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

// tailBuffer keeps the last limit lines for failure diagnosis.
type tailBuffer struct {
	limit int
	lines []string
}

func (t *tailBuffer) add(line string) {
	t.lines = append(t.lines, line)
	if len(t.lines) > t.limit {
		t.lines = t.lines[len(t.lines)-t.limit:]
	}
}

func (t *tailBuffer) String() string { return strings.Join(t.lines, "\n") }

// Canned pair for the "fake" harness: a real, buildable inventory with
// one apt item, so demos and tests exercise the whole mirror flow without
// a vendor CLI. It must always satisfy the envdef contract.
const fakeScanDockerfile = `FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends jq=1.7.1-3build1 \
    && rm -rf /var/lib/apt/lists/*
`

const fakeScanManifestJSON = `[
  {
    "name": "jq",
    "version": "1.7.1",
    "reason": "canned inventory item for demos and tests",
    "start_line": 3,
    "end_line": 5,
    "check_command": "jq --version"
  }
]
`

// Canned pair for a fake repo scan, distinct from the mirror pair so the
// from-repo flow is demoable end to end. It must always satisfy the
// envdef contract.
const fakeRepoDockerfile = `FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends ripgrep=14.1.0-1 \
    && rm -rf /var/lib/apt/lists/*
`

const fakeRepoManifestJSON = `[
  {
    "name": "ripgrep",
    "version": "14.1.0",
    "reason": "canned repo item for demos and tests",
    "start_line": 3,
    "end_line": 5,
    "check_command": "rg --version"
  }
]
`

// fakeScan returns a canned pair through the same validation path a real
// scan uses, so the fakes can never drift from the contract.
func fakeScan(dockerfile, manifestJSON, line string, emit func(ScanEvent)) (*ScanResult, error) {
	emit(ScanEvent{Status: ScanStatusRunning})
	emit(ScanEvent{Line: line})
	emit(ScanEvent{Status: ScanStatusValidating})
	items, err := envdef.ParseManifest([]byte(manifestJSON))
	if err != nil {
		return nil, fmt.Errorf("localops: canned fake manifest: %w", err)
	}
	if err := envdef.ValidateDockerfile(dockerfile, items); err != nil {
		return nil, fmt.Errorf("localops: canned fake Dockerfile: %w", err)
	}
	return &ScanResult{
		Dockerfile:   dockerfile,
		ManifestJSON: manifestJSON,
		Manifest:     items,
	}, nil
}
