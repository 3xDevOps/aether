package scheduler

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/3xDevOps/Aether/internal/shellquote"
)

// TestSupervisorSwapsItsChild drives the real supervisor script on a PTY
// through the swaps a mode switch asks for: the agent for a resumed command,
// that command's exit, a login shell, and a child that ignores SIGTERM.
func TestSupervisorSwapsItsChild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	next, state := filepath.Join(dir, "next-command"), filepath.Join(dir, "state")
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := startTestPTYProcess(t, supervisorCommand(next, state, []string{testBinary,
		"-test.run=^TestSupervisorSignalChild$", "--", "harness"}))
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
	// Keep each login shell in a builtin read after installing its HUP
	// handler. A printf followed by the host's prompt/readline is not a
	// handshake for the signal path this test is about to exercise.
	if _, err := p.master.Write([]byte("trap 'printf \"fallback-%s\\n\" hup; exit 0' HUP; printf 'fallback-%s\\n' ready; while :; do read -r line; done\n")); err != nil {
		t.Fatal(err)
	}
	p.waitForOutput(t, "fallback-ready")

	swap("# n2\n")
	p.waitForOutput(t, "fallback-hup")
	waitState("n2 started")
	if _, err := p.master.Write([]byte("trap 'printf \"shell-%s\\n\" hup; exit 0' HUP; printf 'shell-%s\\n' ok; while :; do read -r line; done\n")); err != nil {
		t.Fatal(err)
	}
	p.waitForOutput(t, "shell-ok")

	// Acknowledge TERM without exiting so the force signal cannot race it.
	swap("# n3\nexec " + shellquote.QuoteAlways(testBinary) + " '-test.run=^TestSupervisorSignalChild$' -- stubborn\n")
	p.waitForOutput(t, "shell-hup")
	p.waitForOutput(t, "stubborn-up")
	swap("# n4\n")
	p.waitForOutput(t, "stubborn-term")
	if got, err := os.ReadFile(state); err != nil || strings.TrimSpace(string(got)) != "n3 started" {
		t.Fatalf("a child that ignores SIGTERM was replaced without a second SIGALRM: state = %q, err = %v", got, err)
	}
	if err := p.cmd.Process.Signal(syscall.SIGALRM); err != nil {
		t.Fatal(err)
	}
	waitState("n4 started")
	if _, err := p.master.Write([]byte("printf 'after-kill-%s\\n' ok\n")); err != nil {
		t.Fatal(err)
	}
	p.waitForOutput(t, "after-kill-ok")

	if got := strings.Count(p.output.String(), "[aether] harness exited with code"); got != 1 {
		t.Fatalf("harness exit lines = %d, output = %q", got, p.output.String())
	}
}

// A shell trap can remain pending if TERM arrives just before its read
// syscall blocks. Register a buffered signal channel before readiness instead.
func TestSupervisorSignalChild(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 {
		return
	}
	args := os.Args[i+1:]
	if len(args) != 1 || args[0] != "harness" && args[0] != "stubborn" {
		t.Fatalf("unexpected helper arguments: %q", args)
	}
	name, ready := args[0], "ready"
	if name == "stubborn" {
		ready = "up"
	}
	terms := make(chan os.Signal, 1)
	signal.Notify(terms, syscall.SIGTERM)
	defer signal.Stop(terms)
	fmt.Printf("%s-%s\n", name, ready)
	for range terms {
		fmt.Printf("%s-term\n", name)
		if name == "harness" {
			return
		}
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
	body := "trap 'exit 8' TERM; echo up >> " + shellquote.QuoteAlways(starts) + "; printf 'served-%s\\n' ready; IFS= read -r line; printf 'served-input:%s\\n' \"$line\"; exit 7\n"
	if err := os.WriteFile(next, []byte("# n1\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Process.Signal(syscall.SIGALRM); err != nil {
		t.Fatal(err)
	}
	p.waitForOutput(t, "served-ready")
	if err := p.cmd.Process.Signal(syscall.SIGALRM); err != nil {
		t.Fatal(err)
	}
	// Let the same child finish through its PTY input. Observing its exit
	// from the supervisor ensures the stale signal was handled; elapsed
	// time alone cannot establish that on a busy host.
	if _, err := p.master.Write([]byte("finish\n")); err != nil {
		t.Fatal(err)
	}
	p.waitForOutput(t, "served-input:finish")
	p.waitForOutput(t, "[aether] harness exited with code 7")
	if got, _ := os.ReadFile(starts); string(got) != "up\n" {
		t.Fatalf("a SIGALRM for a served request restarted the child: starts = %q", got)
	}
	if got, _ := os.ReadFile(state); strings.TrimSpace(string(got)) != "n1 exited 7" {
		t.Fatalf("state after the served child finished = %q", got)
	}
}
