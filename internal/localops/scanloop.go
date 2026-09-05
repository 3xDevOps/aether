package localops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/harness"
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

// ScanEvent is one progress report from a running scan: a coarse status
// change or one raw line of agent output, never both.
type ScanEvent struct {
	Status string
	Line   string
}

// ScanFailure is a scan that ended without a valid result. OutputTail
// holds the last lines the agent printed, for diagnosis.
type ScanFailure struct {
	Err        error
	OutputTail string
}

func (f *ScanFailure) Error() string { return f.Err.Error() }
func (f *ScanFailure) Unwrap() error { return f.Err }

// runScanLoop is the loop every local scan shares: render the prompt into
// a fresh scratch directory, run the harness under a hard timeout,
// validate what it wrote, and retry once with the validation error
// appended before giving up. The scratch directory is removed after every
// attempt, success or failure. render receives the scratch path so a
// prompt can name it as the output directory; collect turns the scratch
// contents into the caller's result or the validation error that earns
// the retry; retryNote closes the retry prompt, since scan kinds ask for
// different files. Failures come back as *ScanFailure carrying the
// output tail.
func runScanLoop[T any](ctx context.Context, argvTemplate []string, repoPath string, timeout time.Duration, retryNote string, emit func(ScanEvent),
	render func(scratch string) (string, error), collect func(scratch string) (*T, error)) (*T, error) {
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
		prompt, promptErr := render(scratch)
		if promptErr != nil {
			_ = os.RemoveAll(scratch)
			return nil, promptErr
		}
		if lastErr != nil {
			prompt = retryPrompt(prompt, lastErr, retryNote)
		}
		result, tail, attemptErr, retryable := scanAttempt(ctx, argvTemplate, prompt, scratch, repoPath, timeout, emit, collect)
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

// scanAttempt runs the harness once and validates what it wrote into
// scratch. The caller owns scratch. repoPath, when set, becomes the
// working directory and its git status must survive the run unchanged.
// retryable marks contract violations (missing or invalid output files)
// that earn the one retry; execution failures (timeout, crash, unrunnable
// command) and a modified repository do not.
func scanAttempt[T any](ctx context.Context, argvTemplate []string, prompt, scratch, repoPath string, timeout time.Duration, emit func(ScanEvent),
	collect func(scratch string) (*T, error)) (result *T, tail string, err error, retryable bool) {
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
	result, err = collect(scratch)
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
	// A cancelled scan must take the harness's children with it, or they
	// keep the output pipe open and Wait only returns after WaitDelay.
	detachCommand(cmd)
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

// retryPrompt appends the validation error to the prompt for the one
// retry the contract allows, closing with the caller's note about what
// to write again.
func retryPrompt(prompt string, validationErr error, retryNote string) string {
	return prompt + "\n\nYour previous attempt failed validation:\n" +
		validationErr.Error() + "\n\n" + retryNote
}

// scanArgvTemplate resolves the argv template a scan instantiates: the
// override when set, otherwise the setup-capable harness's headless argv.
func scanArgvTemplate(harnessName string, override []string) ([]string, error) {
	if len(override) > 0 {
		return override, nil
	}
	for _, p := range harness.SetupHarnesses() {
		if p.Name == harnessName {
			return p.HeadlessArgs, nil
		}
	}
	return nil, fmt.Errorf("localops: harness %q is not available for environment setup", harnessName)
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
