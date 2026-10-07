package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/shellquote"
)

// TestSupervisorSwapsItsChild drives the real supervisor script on a PTY
// through the swaps a mode switch asks for: the agent for a resumed command,
// that command's exit, a login shell, and a child that ignores SIGTERM.
func TestSupervisorSwapsItsChild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	next, state := filepath.Join(dir, "next-command"), filepath.Join(dir, "state")
	p := startTestPTYProcess(t, supervisorCommand(next, state, []string{"/bin/sh", "-c",
		"trap 'printf \"harness-%s\\n\" term; exit 0' TERM; printf 'harness-%s\\n' ready; while :; do read -r line; done"}))
	defer func() {
		_ = p.cmd.Process.Kill()
		_ = p.master.Close()
	}()
	swap := func(content string) {
		t.Helper()
		if err := os.WriteFile(next, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := p.cmd.Process.Signal(syscall.SIGALRM); err != nil {
			t.Fatal(err)
		}
	}
	waitState := func(want string) {
		t.Helper()
		waitFor(t, "supervisor state "+want, func() bool {
			got, _ := os.ReadFile(state)
			return strings.TrimSpace(string(got)) == want
		})
	}

	p.waitForOutput(t, "harness-ready")
	swap("# n1\nprintf 'resumed-%s\\n' up; exit 7\n")
	p.waitForOutput(t, "harness-term")
	p.waitForOutput(t, "resumed-up")
	waitState("n1 exited 7")
	p.waitForOutput(t, "[aether] harness exited with code 7")
	if _, err := p.master.Write([]byte("printf 'fallback-%s\\n' ready\n")); err != nil {
		t.Fatal(err)
	}
	p.waitForOutput(t, "fallback-ready")

	swap("# n2\n")
	waitState("n2 started")
	if _, err := p.master.Write([]byte("printf 'shell-%s\\n' ok\n")); err != nil {
		t.Fatal(err)
	}
	p.waitForOutput(t, "shell-ok")

	swap("# n3\ntrap '' TERM; printf 'stubborn-%s\\n' up; while :; do sleep 1; done\n")
	p.waitForOutput(t, "stubborn-up")
	swap("# n4\n")
	time.Sleep(200 * time.Millisecond)
	if got, _ := os.ReadFile(state); strings.HasPrefix(string(got), "n4") {
		t.Fatal("a child that ignores SIGTERM was replaced without a second SIGALRM")
	}
	if err := p.cmd.Process.Signal(syscall.SIGALRM); err != nil {
		t.Fatal(err)
	}
	waitState("n4 started")

	if got := strings.Count(p.output.String(), "[aether] harness exited with code"); got != 1 {
		t.Fatalf("harness exit lines = %d, output = %q", got, p.output.String())
	}
}

func TestSupervisorIgnoresServedSwap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	next, state, starts := filepath.Join(dir, "next-command"), filepath.Join(dir, "state"), filepath.Join(dir, "starts")
	p := startTestPTYProcess(t, supervisorCommand(next, state, []string{"/bin/sh", "-c", "printf 'harness-%s\\n' ready; while :; do read -r line; done"}))
	defer func() {
		_ = p.cmd.Process.Kill()
		_ = p.master.Close()
	}()
	p.waitForOutput(t, "harness-ready")
	if err := os.WriteFile(next, []byte("# n1\necho up >> "+shellquote.QuoteAlways(starts)+"; while :; do read -r line; done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.cmd.Process.Signal(syscall.SIGALRM); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if got, _ := os.ReadFile(starts); string(got) != "up\n" {
		t.Fatalf("a SIGALRM for a served request restarted the child: starts = %q", got)
	}
}
