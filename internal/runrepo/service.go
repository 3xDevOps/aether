// Package runrepo operates native git and gh in an already authorized live run.
// It never resolves a run ID, chooses an account, or infers a writable remote.
package runrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const MaxOutput = protocol.MaxRunRepoOutputBytes

// ExecFunc is runtime.Runtime.Exec, bound by the caller to its runtime.
// Implementations must retain Runtime.Exec's combined 1 MiB capture bound.
type ExecFunc func(context.Context, runtime.ID, []string, string) (int, string, string, error)

// Execution is trusted dispatcher input, not a wire request. Authorize must
// recheck live run membership, account-use and (for mutations) Push authority.
// It is called before every execution, including the final mutation boundary.
// The execution/account environment is the run's; never substitute the server's.
type Execution struct {
	ContainerID runtime.ID
	WorkDir     string
	Authorize   func(context.Context, bool) error
	// CoAuthors reuses the scheduler's attribution list, excluding the
	// actual native author email (including Git environment overrides).
	CoAuthors func(context.Context, string) ([]string, error)
}

type Service struct{ exec ExecFunc }

func New(exec ExecFunc) *Service { return &Service{exec: exec} }

type CommandOutput = protocol.RunRepoCommandOutput

type CommandError struct {
	Operation string
	Output    CommandOutput
	Cause     error
}

func (e *CommandError) Error() string {
	message := fmt.Sprintf("runrepo: %s exited %d", e.Operation, e.Output.ExitCode)
	if e.Output.Stderr != "" {
		message += ": " + e.Output.Stderr
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}
func (e *CommandError) Unwrap() error { return e.Cause }

var ErrStale = errors.New("runrepo: branch or HEAD changed; refresh before acting")
var ErrTruncated = errors.New("runrepo: output limit exceeded; response is incomplete")

type Expected = protocol.RunGitExpected

type StaleError struct {
	Expected Expected
	Actual   Expected
	Detached bool
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("%v: expected %s at %s, actual %s at %s (detached=%t)", ErrStale, e.Expected.Branch, e.Expected.Head, e.Actual.Branch, e.Actual.Head, e.Detached)
}
func (e *StaleError) Unwrap() error { return ErrStale }

func (s *Service) command(ctx context.Context, run Execution, mutate bool, argv ...string) (CommandOutput, error) {
	if s == nil || s.exec == nil || run.ContainerID == "" || !strings.HasPrefix(run.WorkDir, "/") || strings.ContainsRune(run.WorkDir, 0) || run.Authorize == nil {
		return CommandOutput{}, errors.New("runrepo: authorized live execution is required")
	}
	if err := run.Authorize(ctx, mutate); err != nil {
		return CommandOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args := []string{"env", "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "GIT_LITERAL_PATHSPECS=1"}
	args = append(args, argv...)
	code, stdout, stderr, err := s.exec(ctx, run.ContainerID, args, run.WorkDir)
	out := boundOutput(CommandOutput{ExitCode: code, Stdout: stdout, Stderr: stderr})
	if err != nil || code != 0 {
		return out, &CommandError{Operation: argv[0], Output: out, Cause: err}
	}
	return out, nil
}

// Diagnostics have priority over ordinary output within the combined bound.
func boundOutput(out CommandOutput) CommandOutput {
	if len(out.Stdout)+len(out.Stderr) > MaxOutput {
		out.Truncated = true
		out.Stderr = clipUTF8(out.Stderr, MaxOutput)
		out.Stdout = clipUTF8(out.Stdout, MaxOutput-len(out.Stderr))
	}
	return out
}

func clipUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

// FailureOutput retains the actual failed command, not an earlier successful
// read. Partial external-success booleans remain the caller's responsibility.
func FailureOutput(out CommandOutput, err error) CommandOutput {
	var ce *CommandError
	if errors.As(err, &ce) {
		return ce.Output
	}
	return out
}

func (s *Service) git(ctx context.Context, run Execution, mutate bool, args ...string) (CommandOutput, error) {
	return s.command(ctx, run, mutate, append([]string{"git", "--no-pager", "-c", "color.ui=false"}, args...)...)
}

func complete(out CommandOutput, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if out.Truncated {
		return "", &CommandError{Operation: "read", Output: out, Cause: ErrTruncated}
	}
	return out.Stdout, nil
}

func cleanText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validatePaths(paths []string, required bool) error {
	if len(paths) > protocol.MaxRunGitPaths || required && len(paths) == 0 {
		return errors.New("runrepo: select between 1 and 256 paths")
	}
	total := 0
	for _, p := range paths {
		total += len(p)
		if !cleanText(p, protocol.MaxRunGitPathBytes) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
			return fmt.Errorf("runrepo: invalid relative path %q", p)
		}
		for _, part := range strings.Split(p, "/") {
			if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
				return fmt.Errorf("runrepo: invalid relative path %q", p)
			}
		}
	}
	if total > 32<<10 {
		return errors.New("runrepo: selected paths exceed input limit")
	}
	return nil
}

func (s *Service) branch(ctx context.Context, run Execution, branch string) error {
	if !cleanText(branch, 1024) || strings.HasPrefix(branch, "-") {
		return errors.New("runrepo: invalid branch")
	}
	_, err := s.git(ctx, run, false, "check-ref-format", "refs/heads/"+branch)
	return err
}

func (s *Service) CheckExpected(ctx context.Context, run Execution, expected Expected) error {
	if !validOID(expected.Head) {
		return errors.New("runrepo: expected HEAD must be a full object ID")
	}
	if err := s.branch(ctx, run, expected.Branch); err != nil {
		return err
	}
	out, err := s.git(ctx, run, false, "rev-parse", "--verify", "HEAD")
	head, err := complete(out, err)
	if err != nil {
		return err
	}
	out, err = s.git(ctx, run, false, "symbolic-ref", "--quiet", "--short", "HEAD")
	actual := Expected{Head: strings.TrimSpace(head), Branch: strings.TrimSpace(out.Stdout)}
	if err != nil {
		var commandErr *CommandError
		if !errors.As(err, &commandErr) || commandErr.Cause != nil || out.ExitCode != 1 {
			return err
		}
		return &StaleError{Expected: expected, Actual: actual, Detached: true}
	}
	if out.Truncated {
		return ErrTruncated
	}
	if actual != expected {
		return &StaleError{Expected: expected, Actual: actual}
	}
	return nil
}
