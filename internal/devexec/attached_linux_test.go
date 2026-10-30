//go:build linux

package devexec

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type attachedProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

// startAttached runs RunAttached in its own process, as the staged helper
// runs, with input ahead of the stdin it holds open.
func startAttached(t *testing.T, input string, role ...string) *attachedProcess {
	t.Helper()
	argv := append([]string{os.Args[0], "-test.run=^TestSupervisorSubprocess$", "--"}, role...)
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestSupervisorSubprocess$", "--", Attached, strconv.Itoa(len(input))}, argv...)...)
	cmd.Env = append(os.Environ(), "AETHER_DEVEXEC_SUBPROCESS=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if _, err = io.WriteString(stdin, input); err != nil {
		t.Fatal(err)
	}
	return &attachedProcess{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
}

func (p *attachedProcess) line(t *testing.T) string {
	t.Helper()
	line, err := p.stdout.ReadString('\n')
	if err != nil {
		t.Fatalf("read the command's output: %v", err)
	}
	return strings.TrimSpace(line)
}

func (p *attachedProcess) exitCode(t *testing.T) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatal(err)
		}
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(10 * time.Second):
		t.Fatal("the helper did not exit")
		return 0
	}
}

func TestRunAttachedGivesTheCommandItsInputAndExitsWithIt(t *testing.T) {
	p := startAttached(t, "hello\n", "input")
	if got := p.line(t); got != "received:hello" {
		t.Fatalf("output = %q", got)
	}
	if code := p.exitCode(t); code != 19 {
		t.Fatalf("exit = %d, want the command's 19 while the caller is still attached", code)
	}
}

func TestRunAttachedKillsTheCommandWhenTheCallerLeaves(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	p := startAttached(t, "", "hold", pidFile)
	if got := p.line(t); got != "descendant-ready" {
		t.Fatalf("output = %q", got)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if code := p.exitCode(t); code != 128+int(syscall.SIGKILL) {
		t.Fatalf("exit = %d, want the command killed", code)
	}
	if err = syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the command is still there: kill(%d, 0) = %v", pid, err)
	}
}
