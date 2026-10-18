//go:build windows

package localops

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHandoffSleeper(t *testing.T) {
	if os.Getenv("AETHER_HANDOFF_SLEEPER") == "" {
		t.Skip("runs only as the child of the wait tests")
	}
	time.Sleep(time.Minute)
}

func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHandoffSleeper$")
	cmd.Env = append(os.Environ(), "AETHER_HANDOFF_SLEEPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func TestWaitForExitReturnsWhenTheProcessEnds(t *testing.T) {
	cmd := sleeper(t)
	done := make(chan error, 1)
	go func() { done <- waitForExit(cmd.Process.Pid, 30*time.Second) }()
	_ = cmd.Process.Kill()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForExit: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("waitForExit did not return after the process was killed")
	}
}

func TestWaitForExitTimesOutOnARunningProcess(t *testing.T) {
	cmd := sleeper(t)
	err := waitForExit(cmd.Process.Pid, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestWaitForExitAcceptsAProcessThatIsGone(t *testing.T) {
	cmd := sleeper(t)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := waitForExit(cmd.Process.Pid, time.Second); err != nil {
		t.Fatalf("waitForExit: %v", err)
	}
}
