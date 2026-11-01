//go:build unix

package scheduler

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestExecTerminalStartsTheEnvironmentAndRunsALoginShell(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.rt.execStderr = "warning\n"
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) { return 3, "out\n", nil }

	code, stdout, stderr, err := e.sched.ExecTerminal(t.Context(), e.member.ID, `echo "$HOME"; exit 3`)
	if err != nil || code != 3 || stdout != "out\n" || stderr != "warning\n" {
		t.Fatalf("ExecTerminal = (%d, %q, %q, %v), want the command's own exit code and streams", code, stdout, stderr, err)
	}
	terminal, err := e.db.GetTerminal(t.Context(), e.member.ID)
	if err != nil {
		t.Fatalf("the environment ExecTerminal started: %v", err)
	}
	calls := e.rt.execRuns()
	if len(calls) != 1 {
		t.Fatalf("exec calls = %+v, want one", calls)
	}
	call := calls[0]
	if call.id != runtime.ID(terminal.ContainerID) || call.workDir != "/root" {
		t.Errorf("exec ran in container %q at %q, want the member terminal %q at its home", call.id, call.workDir, terminal.ContainerID)
	}
	if len(call.argv) != 6 || call.argv[0] != "/bin/sh" || !strings.Contains(call.argv[2], `-lc "$1"`) || call.argv[4] != `echo "$HOME"; exit 3` {
		t.Errorf("exec argv = %q, want a login shell given the command as one argument", call.argv)
	}
}

func TestExecTerminalReportsTheTimeLimitOnlyWhenTheScriptDoes(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	e.rt.execStderr = "aether: terminal exec stopped the command at its time limit\n"
	e.rt.execHandler = func(runtime.ID, []string) (int, string, error) { return 137, "partial\n", nil }

	code, stdout, stderr, err := e.sched.ExecTerminal(t.Context(), e.member.ID, "kill -KILL $$")
	if err != nil || code != 137 || stdout != "partial\n" || stderr != e.rt.execStderr {
		t.Fatalf("ExecTerminal = (%d, %q, %q, %v), want a command's own status and output passed through", code, stdout, stderr, err)
	}

	e.rt.execHandler = func(_ runtime.ID, argv []string) (int, string, error) {
		e.rt.mu.Lock()
		e.rt.execStderr = "last words\n" + argv[len(argv)-1] + "\n"
		e.rt.mu.Unlock()
		return 124, "partial\n", nil
	}
	_, _, _, err = e.sched.ExecTerminal(t.Context(), e.member.ID, "sleep 1000")
	if err == nil || err.Error() != "terminal exec: the command passed the 10m0s time limit" {
		t.Fatalf("ExecTerminal at the limit = %v, want the time limit error", err)
	}
}

// Only the script can tell a command the limit stopped from one that ended
// by itself, whatever its status, so it runs here for real. A login shell
// may write lines of its own, so the streams are searched, not compared.
func TestTerminalExecScriptMarksOnlyACommandStoppedAtTheLimit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no timeout on PATH")
	}
	run := func(command string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		argv := terminalExecArgv(command, "MARK", time.Second)
		cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatalf("session %q: %v", command, err)
		}
		return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
	}

	// The child ignores TERM and would outlive a kill of its parent alone.
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	stubborn := `sh -c 'trap "" TERM; echo $$ >"$0"; while :; do sleep 0.1; done' ` + pidFile + ` & echo out; echo late >&2; sleep 30; echo never`
	code, stdout, stderr := run(stubborn)
	if !strings.Contains(stdout, "out\n") || strings.Contains(stdout, "never") || !strings.Contains(stderr, "late\n") || !strings.HasSuffix(stderr, "MARK\n") {
		t.Errorf("a command past the limit = (%d, %q, %q), want its output so far and then the mark", code, stdout, stderr)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	// Where PID 1 collects nothing, a killed child stays a PID in state Z.
	running := func() bool {
		if errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
			return false
		}
		stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child))
		return statErr != nil || !strings.Contains(string(stat), ") Z ")
	}
	if _, err = exec.LookPath("setsid"); err == nil {
		for range 50 {
			if !running() {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if running() {
			t.Errorf("child %d that ignores TERM outlived the time limit", child)
		}
	}
	_ = syscall.Kill(child, syscall.SIGKILL)

	for _, own := range []struct {
		command string
		code    int
	}{{"echo own >&2; exit 137", 137}, {"echo own >&2; exit 124", 124}, {"echo own >&2; exit 143", 143}, {"echo own >&2; exit 7", 7}, {"echo own >&2", 0}} {
		if code, _, stderr := run(own.command); code != own.code || !strings.Contains(stderr, "own\n") || strings.HasSuffix(stderr, "MARK\n") {
			t.Errorf("%q = (%d, %q), want its own status %d and no mark", own.command, code, stderr, own.code)
		}
	}
}
