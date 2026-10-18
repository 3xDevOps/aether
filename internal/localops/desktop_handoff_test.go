package localops

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type handoffTrace struct{ steps []string }

func (tr *handoffTrace) ops(waitErr error, installErrs ...error) handoffOps {
	return handoffOps{
		waitExit: func(pid int, _ time.Duration) error {
			tr.steps = append(tr.steps, "wait")
			return waitErr
		},
		endDaemon:   func() bool { tr.steps = append(tr.steps, "daemon"); return true },
		startDaemon: func() { tr.steps = append(tr.steps, "daemon-run") },
		install: func() error {
			tr.steps = append(tr.steps, "install")
			if len(installErrs) == 0 {
				return nil
			}
			err := installErrs[0]
			installErrs = installErrs[1:]
			return err
		},
		launch: func() error { tr.steps = append(tr.steps, "launch"); return nil },
	}
}

func (tr *handoffTrace) got() string { return strings.Join(tr.steps, " ") }

func TestFinishDesktopUpdateSwapsThenLaunches(t *testing.T) {
	cacheHome(t)
	var tr handoffTrace
	if err := finishDesktopUpdate(tr.ops(nil), []int{10, 20}, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if want := "wait wait daemon install daemon-run launch"; tr.got() != want {
		t.Fatalf("steps = %q, want %q", tr.got(), want)
	}
	if got := LastDesktopBuildError(); got != "" {
		t.Fatalf("a swap that worked recorded %q", got)
	}
}

func TestFinishDesktopUpdateRetriesASwapAnExitingChildBlocked(t *testing.T) {
	cacheHome(t)
	var tr handoffTrace
	busy := errors.New("Access is denied")
	if err := finishDesktopUpdate(tr.ops(nil, busy, busy), []int{10}, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if want := "wait daemon install install install daemon-run launch"; tr.got() != want {
		t.Fatalf("steps = %q, want %q", tr.got(), want)
	}
}

func TestFinishDesktopUpdateRecordsAFailedSwapAndStartsTheOldApp(t *testing.T) {
	cacheHome(t)
	var tr handoffTrace
	locked := errors.New("localops: move the installed app aside: Access is denied")
	ops := tr.ops(nil)
	ops.install = func() error { tr.steps = append(tr.steps, "install"); return locked }
	err := finishDesktopUpdate(ops, []int{10}, 20*time.Millisecond, 5*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "Access is denied") {
		t.Fatalf("err = %v, want the swap's own error", err)
	}
	if !strings.HasSuffix(tr.got(), "install daemon-run launch") {
		t.Fatalf("steps = %q, want the old app started after the failed swap", tr.got())
	}
	if got := LastDesktopBuildError(); !strings.Contains(got, "Access is denied") {
		t.Fatalf("recorded = %q, want the real error for the next dashboard", got)
	}
}

func TestFinishDesktopUpdateDoesNotSwapAnAppThatNeverExited(t *testing.T) {
	cacheHome(t)
	var tr handoffTrace
	err := finishDesktopUpdate(tr.ops(errors.New("still running after 1m0s")), []int{10, 20}, time.Second, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "process 10") {
		t.Fatalf("err = %v, want the wait failure naming the process", err)
	}
	if want := "wait launch"; tr.got() != want {
		t.Fatalf("steps = %q, want %q: no daemon end, no swap", tr.got(), want)
	}
	if got := LastDesktopBuildError(); !strings.Contains(got, "still running") {
		t.Fatalf("recorded = %q", got)
	}
}
