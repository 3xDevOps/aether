//go:build windows

package localops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandoffSleeper(t *testing.T) {
	if os.Getenv("AETHER_HANDOFF_SLEEPER") == "" {
		t.Skip("sessions only as the child of the wait tests")
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

// heldApp is an installed app directory with one of its files open, the
// way an app that is exiting, or a Defender scan, holds it.
func heldApp(t *testing.T) (dir string, held *os.File) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "Aether Desktop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, desktopExe)
	if err := os.WriteFile(exe, []byte("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := os.Rename(dir, dir+".aside"); err == nil || !renameBusy(err) {
		t.Fatalf("rename under an open handle = %v, want a busy error", err)
	}
	return dir, held
}

func TestRenameWhileBusyWaitsForTheAppToBeReleased(t *testing.T) {
	dir, held := heldApp(t)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = held.Close()
	}()
	if err := renameWhileBusy(os.Rename, dir, dir+".aside", 10*time.Second); err != nil {
		t.Fatalf("renameWhileBusy: %v", err)
	}
}

func TestRenameWhileBusyReturnsTheErrorOfAnAppStillOpen(t *testing.T) {
	dir, _ := heldApp(t)
	err := renameWhileBusy(os.Rename, dir, dir+".aside", 200*time.Millisecond)
	if err == nil || !renameBusy(err) {
		t.Fatalf("err = %v, want the busy error itself", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("the app moved anyway: %v", statErr)
	}
}
