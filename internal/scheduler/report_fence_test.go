package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
)

// A direct report the scheduler cannot apply is an error the agent sees; an
// outbox hand-off fenced the same way settles without one.
func TestDirectReportFencesAreErrors(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	at := time.Now()
	if err := e.sched.ReportWaiting(ctx, run.ID, "waiting-1", "", "on 3 workers", at); err != nil {
		t.Fatalf("ReportWaiting: %v", err)
	}
	if err := e.sched.ReportWaiting(ctx, run.ID, "waiting-1", "", "on 3 workers", at); err != nil {
		t.Fatalf("retried ReportWaiting: %v", err)
	}
	if err := e.sched.ReportWaiting(ctx, run.ID, "waiting-0", "", "on 4 workers", at.Add(-time.Second)); !errors.Is(err, coord.ErrReportSuperseded) {
		t.Fatalf("older ReportWaiting = %v, want %v", err, coord.ErrReportSuperseded)
	}
	if err := e.sched.ReportNeedsInput(ctx, run.ID, "needs-input-0", "which region?", at.Add(-time.Second)); !errors.Is(err, coord.ErrReportSuperseded) {
		t.Fatalf("older ReportNeedsInput = %v, want %v", err, coord.ErrReportSuperseded)
	}
	if err := e.sched.ReportBlocked(ctx, run.ID, "blocked-0", "need the staging key", at.Add(-time.Second)); err != nil {
		t.Fatalf("older ReportBlocked = %v, want the outbox to settle it", err)
	}

	e.sched.mu.Lock()
	e.sched.runs[run.ID].killRequested = true
	e.sched.mu.Unlock()
	if err := e.sched.ReportWaiting(ctx, run.ID, "waiting-2", "", "on 2 workers", at.Add(time.Second)); !errors.Is(err, coord.ErrRunFinished) {
		t.Fatalf("ReportWaiting on a run being killed = %v, want %v", err, coord.ErrRunFinished)
	}
	if err := e.sched.FinishReported(ctx, run.ID, "success-1", domain.RunCompleted, at.Add(time.Second)); err != nil {
		t.Fatalf("FinishReported on a run being killed = %v, want the outbox to settle it", err)
	}
	e.sched.mu.Lock()
	reason := e.sched.runs[run.ID].idleReason
	e.sched.mu.Unlock()
	if reason != waitingReasonPrefix+"on 3 workers" {
		t.Fatalf("pending reason = %q, want the first waiting report's", reason)
	}
}

// A waiting report belongs to its turn: a turn that fails drops it, so a
// later clean turn end does not show it.
func TestAbnormalTurnEndDropsAPendingWait(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	report := func(r agentstatus.Report) {
		t.Helper()
		if err := e.sched.ReportAgentState(ctx, run.ID, r); err != nil {
			t.Fatalf("report %s: %v", r.State, err)
		}
	}
	report(agentstatus.Report{State: agentstatus.Working})
	if err := e.sched.ReportWaiting(ctx, run.ID, "waiting-1", "", "on 3 workers", time.Now()); err != nil {
		t.Fatalf("ReportWaiting: %v", err)
	}
	report(agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonFailed})
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if r, err := e.db.GetRun(ctx, run.ID); err != nil || r.Reason != agentstatus.ReasonFailed {
		t.Fatalf("run after the failed turn = %+v, %v, want reason %q", r, err, agentstatus.ReasonFailed)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.IdleReason != "" {
		t.Fatalf("sidecar after the failed turn = %+v, %v, want no pending reason", sc, err)
	}

	report(agentstatus.Report{State: agentstatus.Working})
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	report(waitingForInput)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if r, err := e.db.GetRun(ctx, run.ID); err != nil || r.Reason != turnEndedReason {
		t.Fatalf("run after a clean turn end = %+v, %v, want reason %q", r, err, turnEndedReason)
	}
}
