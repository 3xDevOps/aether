package localops

import (
	"bytes"
	"context"
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

// Scan modes. Inventory is a first run against the machine; refine reruns
// the agent over a previous Dockerfile and manifest pair with feedback.
const (
	ScanModeInventory = "inventory"
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
	// Harness names the setup-capable harness to run, or "fake" for the
	// canned inventory that exercises the flow without a vendor CLI.
	Harness string
	// Mode is ScanModeInventory or ScanModeRefine.
	Mode string
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
	if opts.Harness == "fake" {
		return fakeScan(emit)
	}
	argv, err := scanArgvTemplate(opts)
	if err != nil {
		return nil, err
	}
	prompt, err := renderScanPrompt(opts)
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
			prompt = retryPrompt(prompt, lastErr)
		}
		result, tail, attemptErr, retryable := scanAttempt(ctx, argv, prompt, timeout, emit)
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
func renderScanPrompt(opts ScanOptions) (string, error) {
	switch opts.Mode {
	case ScanModeInventory:
		return envprompt.RenderInventory(envprompt.InventoryParams{BaseImage: envdef.BaseImage})
	case ScanModeRefine:
		return envprompt.RenderRefine(envprompt.RefineParams{
			Dockerfile:   opts.PreviousDockerfile,
			ManifestJSON: opts.PreviousManifestJSON,
			Feedback:     opts.Feedback,
		})
	default:
		return "", fmt.Errorf("localops: unknown scan mode %q (want %s or %s)", opts.Mode, ScanModeInventory, ScanModeRefine)
	}
}

// retryPrompt appends the validation error to the prompt for the one
// retry the contract allows.
func retryPrompt(prompt string, validationErr error) string {
	return prompt + "\n\nYour previous attempt failed validation:\n" +
		validationErr.Error() +
		"\n\nCorrect these problems and write both files again."
}

// scanAttempt runs the harness once in a fresh scratch directory and
// validates what it wrote. retryable marks contract violations (missing
// or invalid output files) that earn the one retry; execution failures
// (timeout, crash, unrunnable command) do not.
func scanAttempt(ctx context.Context, argvTemplate []string, prompt string, timeout time.Duration, emit func(ScanEvent)) (result *ScanResult, tail string, err error, retryable bool) {
	scratch, err := os.MkdirTemp("", "aether-envscan-")
	if err != nil {
		return nil, "", fmt.Errorf("localops: create scan scratch directory: %w", err), false
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	tail, err = runScanCommand(ctx, argvTemplate, prompt, scratch, timeout, emit)
	if err != nil {
		return nil, tail, err, false
	}

	emit(ScanEvent{Status: ScanStatusValidating})
	result, err = collectScanOutput(scratch)
	if err != nil {
		return nil, tail, err, true
	}
	return result, tail, nil, false
}

// runScanCommand executes one harness invocation in scratch, streaming
// combined output lines through emit and returning the output tail.
func runScanCommand(ctx context.Context, argvTemplate []string, prompt, scratch string, timeout time.Duration, emit func(ScanEvent)) (string, error) {
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
	cmd.Dir = scratch
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

// fakeScan returns the canned pair through the same validation path a
// real scan uses, so the fake can never drift from the contract.
func fakeScan(emit func(ScanEvent)) (*ScanResult, error) {
	emit(ScanEvent{Status: ScanStatusRunning})
	emit(ScanEvent{Line: "fake harness: returning the canned inventory"})
	emit(ScanEvent{Status: ScanStatusValidating})
	items, err := envdef.ParseManifest([]byte(fakeScanManifestJSON))
	if err != nil {
		return nil, fmt.Errorf("localops: canned fake manifest: %w", err)
	}
	if err := envdef.ValidateDockerfile(fakeScanDockerfile, items); err != nil {
		return nil, fmt.Errorf("localops: canned fake Dockerfile: %w", err)
	}
	return &ScanResult{
		Dockerfile:   fakeScanDockerfile,
		ManifestJSON: fakeScanManifestJSON,
		Manifest:     items,
	}, nil
}
