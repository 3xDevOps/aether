package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func TestEnsureRunShellTabStartsBashInWorkspace(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.WorktreeMount = "/workspace" })
	run, _ := e.launchFake(t, "shell")

	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "tab-1", 100, 30); err != nil {
		t.Fatalf("EnsureRunShellTab: %v", err)
	}
	active := e.pty.ActiveSessions("run-shell:" + string(run.ID) + ":")
	if len(active) != 1 || active[0] != ptyhost.RunShellSession(run.ID, "tab-1") {
		t.Fatalf("active shell sessions = %v", active)
	}
	calls := e.rt.execTTYCalls()
	if len(calls) != 1 || strings.Join(calls[0].argv, " ") != "/bin/bash -l" || calls[0].workDir != e.cfg.WorktreeMount {
		t.Fatalf("ExecTTY calls = %+v, want bash in %q", calls, e.cfg.WorktreeMount)
	}
}

func TestEnsureRunShellTabFallsBackToSh(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "shell fallback")
	var attempts int
	e.rt.execTTYHook = func(ctx context.Context, id runtime.ID, argv []string, workDir string, cols, rows uint) (runtime.Attachment, error) {
		attempts++
		if attempts == 1 {
			return nil, &runtime.ExecExitError{Code: 127}
		}
		return e.rt.attachForExec(ctx, id, argv, workDir, cols, rows)
	}

	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "fallback", 80, 24); err != nil {
		t.Fatalf("EnsureRunShellTab fallback: %v", err)
	}
	calls := e.rt.execTTYCalls()
	if len(calls) != 2 || strings.Join(calls[1].argv, " ") != "/bin/sh -l" {
		t.Fatalf("ExecTTY fallback calls = %+v", calls)
	}
}

func TestEnsureRunShellTabRejectsInvalidAndMissingRuns(t *testing.T) {
	e := newTestEnv(t, nil)
	if err := e.sched.EnsureRunShellTab(t.Context(), "missing", "tab", 80, 24); !errors.Is(err, ptyhost.ErrNoSession) {
		t.Fatalf("missing run error = %v, want ErrNoSession", err)
	}
	run, _ := e.launchFake(t, "shell validation")
	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "Bad_tab", 80, 24); err == nil || !strings.Contains(err.Error(), "tab") {
		t.Fatalf("invalid tab error = %v", err)
	}
}

func TestEnsureRunShellTabEnforcesFourTabLimitAndIsIdempotent(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "shell limit")
	for _, tab := range []string{"one", "two", "three", "four"} {
		if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, tab, 80, 24); err != nil {
			t.Fatalf("EnsureRunShellTab(%q): %v", tab, err)
		}
	}
	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "five", 80, 24); err == nil || !strings.Contains(err.Error(), "at most 4 shell tabs") {
		t.Fatalf("fifth tab error = %v", err)
	}
	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "one", 80, 24); err != nil {
		t.Fatalf("idempotent EnsureRunShellTab: %v", err)
	}
	if got := len(e.rt.execTTYCalls()); got != 4 {
		t.Fatalf("ExecTTY calls after idempotent ensure = %d, want 4", got)
	}
}

func TestEnsureRunShellTabRejectsPausedRun(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "paused shell")
	e.sched.mu.Lock()
	e.sched.runs[run.ID].paused = true
	e.sched.mu.Unlock()
	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "paused", 80, 24); !errors.Is(err, ptyhost.ErrNoSession) {
		t.Fatalf("paused run error = %v, want ErrNoSession", err)
	}
}

func TestFinalizeStopsRunShellTabs(t *testing.T) {
	e := newTestEnv(t, nil)
	run, c := e.launchFake(t, "shell cleanup")
	if err := e.sched.EnsureRunShellTab(t.Context(), run.ID, "cleanup", 80, 24); err != nil {
		t.Fatalf("EnsureRunShellTab: %v", err)
	}
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	prefixes := e.pty.stoppedPrefixesSnapshot()
	want := "run-shell:" + string(run.ID) + ":"
	for _, prefix := range prefixes {
		if prefix == want {
			return
		}
	}
	t.Fatalf("prefix stops = %v, want %q", prefixes, want)
}
