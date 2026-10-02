package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/store"
)

var waitingForInput = agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonInput}

// expectOnlyStatusEvent reads sub until run reaches to and fails on any
// other run.status event for run before it.
func expectOnlyStatusEvent(t *testing.T, sub events.Subscription, run domain.RunID, to domain.RunStatus) events.RunStatusPayload {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("event stream closed while waiting for %s", to)
			}
			p, isStatus := ev.Payload.(events.RunStatusPayload)
			if !isStatus || ev.RunID != run {
				continue
			}
			if p.To != to {
				t.Fatalf("status event %s -> %s (%q) before %s", p.From, p.To, p.Reason, to)
			}
			return p
		case <-deadline:
			t.Fatalf("timed out waiting for run.status %s on run %s", to, run)
		}
	}
}

// expireReportDeadline makes an armed run overdue and runs the poll loop's
// deadline check once.
func (e *testEnv) expireReportDeadline(t *testing.T, run domain.RunID) {
	t.Helper()
	e.sched.mu.Lock()
	entry := e.sched.runs[run]
	if entry == nil || entry.reported == "" {
		e.sched.mu.Unlock()
		t.Fatalf("run %s is not armed", run)
	}
	entry.reportedAt = time.Now().UTC().Add(-reportFinishDeadline)
	e.sched.mu.Unlock()
	e.sched.finishOverdueReports()
}

// TestReportedSuccessFinishesTUIRunAtTurnEnd is the bug this mechanism
// fixes: an agent that reports success and ends its turn lands in Done,
// with its work committed and its container retained, instead of parking
// in needs-attention for an input it no longer wants.
func TestReportedSuccessFinishesTUIRunAtTurnEnd(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	sub := e.subscribe(t)
	ctx := t.Context()

	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported replay: %v", err)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.ReportedOutcome != domain.RunCompleted {
		t.Fatalf("armed sidecar = %+v, %v; want reported_outcome completed", sc, err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end waiting report: %v", err)
	}

	p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if p.Reason != reportedSuccessRetainedReason || p.From != domain.RunRunning || !p.OutcomeUnseen {
		t.Fatalf("finish event = %+v, want running -> completed because %q, outcome unseen", p, reportedSuccessRetainedReason)
	}
	expectNoStatusEvent(t, sub, run.ID, "a finished run")
	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Reason != reportedSuccessRetainedReason || !row.OutcomeUnseen {
		t.Fatalf("stored row = %q, unseen %v; want %q, unseen", row.Reason, row.OutcomeUnseen, reportedSuccessRetainedReason)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: add OAuth login" {
		t.Fatalf("commits = %v, want one aether: commit", got)
	}
	if e.git.publishedCount(run.ID) == 0 {
		t.Fatal("run branch never published")
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || !sc.Retained || !sc.Paused {
		t.Fatalf("finished sidecar = %+v, %v; want a retained, paused container", sc, err)
	}

	// A human can still decide the finished run's disposition, which
	// settles the unseen outcome.
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun after reported finish: %v", err)
	}
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunMerged); p.OutcomeUnseen {
		t.Fatalf("close event = %+v, want outcome_unseen clear", p)
	}
	if merged := e.waitStoreStatus(t, run.ID, domain.RunMerged); merged.Reason != retainedCloseReason || merged.OutcomeUnseen {
		t.Fatalf("merged row = %q, unseen %v; want %q, seen", merged.Reason, merged.OutcomeUnseen, retainedCloseReason)
	}
}

// TestReportedFailureFinishesAtDeadlineAndRelaunches: with no turn-end
// report, the deadline finishes the run, and the failed run still reopens.
func TestReportedFailureFinishesAtDeadlineAndRelaunches(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, container := e.launchFake(t, "fix flaky test")
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunFailed, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.expireReportDeadline(t, run.ID)

	row := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if row.Reason != reportedFailureRetainedReason || !row.OutcomeUnseen {
		t.Fatalf("stored row = %q, unseen %v; want %q, unseen", row.Reason, row.OutcomeUnseen, reportedFailureRetainedReason)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "wip: fix flaky test" {
		t.Fatalf("commits = %v, want one wip: commit", got)
	}
	sub := e.subscribe(t)
	reopened, err := e.sched.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch failed run: %v", err)
	}
	if reopened.Status != domain.RunRunning || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("relaunched = %+v, want the same running run and container", reopened)
	}
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunRunning); p.OutcomeUnseen {
		t.Fatalf("relaunch event = %+v, want outcome_unseen clear", p)
	}
	if fresh, err := e.db.GetRun(ctx, run.ID); err != nil || fresh.OutcomeUnseen {
		t.Fatalf("relaunched row = %+v, %v; want outcome_unseen clear", fresh, err)
	}
}

// TestReportedSuccessOutranksTheExitCode: a headless agent that reported
// success and then exits non-zero still completes.
func TestReportedSuccessOutranksTheExitCode(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "write docs", "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.rt.byName(string(run.ID)).exitNow(1)

	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Reason != reportedSuccessReason || !row.OutcomeUnseen {
		t.Fatalf("stored row = %q, unseen %v; want %q, unseen", row.Reason, row.OutcomeUnseen, reportedSuccessReason)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: write docs" {
		t.Fatalf("commits = %v, want one aether: commit", got)
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
}

// TestHumanDecisionOutranksAReportedFinish: a close or kill that gets there
// first decides the run, and the armed finish then does nothing.
func TestHumanDecisionOutranksAReportedFinish(t *testing.T) {
	t.Parallel()
	t.Run("close", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		run, _ := e.launchFake(t, "close first")
		if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
			t.Fatalf("FinishReported: %v", err)
		}
		if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
			t.Fatalf("CloseRun: %v", err)
		}
		sub := e.subscribe(t)
		e.expireReportDeadlineAfterClose(t, run.ID)
		expectNoStatusEvent(t, sub, run.ID, "an armed finish after a human close")
		row := e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
		if row.Reason != retainedCloseReason || row.OutcomeUnseen {
			t.Fatalf("row = %q, unseen %v; want the human close %q, seen", row.Reason, row.OutcomeUnseen, retainedCloseReason)
		}
		if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "wip: close first" {
			t.Fatalf("commits = %v, want only the close's wip: commit", got)
		}
	})
	t.Run("kill", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		run, _ := e.launchFake(t, "kill first")
		if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
			t.Fatalf("FinishReported: %v", err)
		}
		e.sched.mu.Lock()
		entry := e.sched.runs[run.ID]
		e.sched.mu.Unlock()
		if err := e.sched.Kill(ctx, run.ID, e.member.ID); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		e.sched.mu.Lock()
		e.sched.startReportedFinishLocked(entry)
		e.sched.mu.Unlock()
		row := e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
		if row.Reason != "killed" {
			t.Fatalf("reason = %q, want killed", row.Reason)
		}
		waitFor(t, "the armed finish to stand down", func() bool {
			e.sched.mu.Lock()
			defer e.sched.mu.Unlock()
			return !entry.reportFinishing
		})
		if row := e.waitStoreStatus(t, run.ID, domain.RunAbandoned); row.Reason != "killed" || row.OutcomeUnseen {
			t.Fatalf("row after the armed finish = %q, unseen %v; want killed, seen", row.Reason, row.OutcomeUnseen)
		}
	})
}

// expireReportDeadlineAfterClose fires the deadline trigger for a run that
// is no longer live, as a finish already scheduled would.
func (e *testEnv) expireReportDeadlineAfterClose(t *testing.T, run domain.RunID) {
	t.Helper()
	e.sched.mu.Lock()
	entry := e.sched.runs[run]
	if entry == nil {
		e.sched.mu.Unlock()
		t.Fatalf("run %s has no owner", run)
	}
	e.sched.startReportedFinishLocked(entry)
	e.sched.mu.Unlock()
	waitFor(t, "the armed finish to stand down", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return !entry.reportFinishing && entry.reported == ""
	})
}

// TestBlockedReasonSurvivesTheHooksThatFollowIt: the blocked summary is
// shown by the park that follows its tool call and gone after the agent
// resumes.
func TestBlockedReasonSurvivesTheHooksThatFollowIt(t *testing.T) {
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
	reason := func() string {
		t.Helper()
		r, err := e.db.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		return r.Reason
	}
	working := agentstatus.Report{State: agentstatus.Working}

	if err := e.sched.ReportBlocked(ctx, run.ID, "report-blocked-1", "need the staging key", time.Now()); err != nil {
		t.Fatalf("ReportBlocked: %v", err)
	}
	report(working)
	report(waitingForInput)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if got := reason(); got != "blocked: need the staging key" {
		t.Fatalf("parked reason = %q, want the blocked summary", got)
	}
	report(waitingForInput)
	if got := reason(); got != "blocked: need the staging key" {
		t.Fatalf("reason after a repeated wait = %q, want the blocked summary", got)
	}

	report(working)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	report(waitingForInput)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if got := reason(); got != agentstatus.ReasonInput {
		t.Fatalf("reason after the agent resumed = %q, want %q", got, agentstatus.ReasonInput)
	}
}

// TestRecoveryFinishesAnArmedRun: the arm survives a restart between the
// report and the finish.
func TestRecoveryFinishesAnArmedRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "restart in between")
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	waitFor(t, "armed run recovered", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		owner := s2.runs[run.ID]
		return owner != nil && owner.reported == domain.RunCompleted
	})
	if err := s2.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end report after restart: %v", err)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunCompleted); row.Reason != reportedSuccessRetainedReason {
		t.Fatalf("reason = %q, want %q", row.Reason, reportedSuccessRetainedReason)
	}
}

// TestRelaunchedRunReportsAgain: relaunch supersedes the terminal report,
// so the reopened agent can report and finish a second time.
func TestRelaunchedRunReportsAgain(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "two rounds")
	first := &store.CoordReport{WorkspaceID: e.ws.ID, RunID: run.ID, Outcome: store.CoordOutcomeSuccess, Summary: "round one", IdempotencyKey: "round-1"}
	if err := e.db.AppendCoordReport(ctx, first); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.expireReportDeadline(t, run.ID)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)

	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if got, err := e.db.GetCoordReport(ctx, first.ID); err != nil || got.SupersededAt == nil {
		t.Fatalf("first report after relaunch = %+v, %v; want superseded", got, err)
	}
	e.sched.mu.Lock()
	armed := e.sched.runs[run.ID].reported
	e.sched.mu.Unlock()
	if armed != "" {
		t.Fatalf("relaunched run is still armed with %s", armed)
	}
	second := &store.CoordReport{WorkspaceID: e.ws.ID, RunID: run.ID, Outcome: store.CoordOutcomeSuccess, Summary: "round two", IdempotencyKey: "round-2"}
	if err := e.db.AppendCoordReport(ctx, second); err != nil {
		t.Fatalf("second report after relaunch: %v", err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported after relaunch: %v", err)
	}
	e.expireReportDeadline(t, run.ID)
	if row := e.waitStoreStatus(t, run.ID, domain.RunCompleted); row.Reason != reportedSuccessRetainedReason {
		t.Fatalf("second finish reason = %q, want %q", row.Reason, reportedSuccessRetainedReason)
	}
}

// TestRetainedExpiryRelabelsAReportedRun: the status stays, the retention
// promise leaves the reason, and relaunch is refused.
func TestRetainedExpiryRelabelsAReportedRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "expire me")
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.expireReportDeadline(t, run.ID)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	waitFor(t, "retained owner", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		owner := e.sched.runs[run.ID]
		return owner != nil && owner.retained && !owner.reportFinishing
	})
	e.sched.mu.Lock()
	past := time.Now().UTC().Add(-time.Second)
	e.sched.runs[run.ID].retainedUntil = &past
	e.sched.mu.Unlock()

	sub := e.subscribe(t)
	e.sched.sweepRetained(ctx)
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunCompleted); p.Reason != reportedSuccessReason || !p.OutcomeUnseen {
		t.Fatalf("expiry relabel event = %+v, want %q with the outcome still unseen", p, reportedSuccessReason)
	}
	waitFor(t, "expired container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Reason != reportedSuccessReason || !row.OutcomeUnseen {
		t.Fatalf("expired row = %q, unseen %v; want %q, unseen", row.Reason, row.OutcomeUnseen, reportedSuccessReason)
	}
	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Relaunch after expiry = %v, want ErrInvalidTransition", err)
	}
}

// TestSeenClearsTheUnseenOutcomeForItsOwner: only the owner clears the
// flag, the first clear publishes run.outcome_seen and a timeline note,
// and a repeat returns the run without publishing again.
func TestSeenClearsTheUnseenOutcomeForItsOwner(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "review me")
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.expireReportDeadline(t, run.ID)
	if row := e.waitStoreStatus(t, run.ID, domain.RunCompleted); !row.OutcomeUnseen {
		t.Fatal("reported finish left outcome_unseen clear")
	}
	sub := e.subscribe(t)

	other := newSteerer(t, e, "Cody", "", "")
	if _, err := e.sched.Seen(ctx, run.ID, other.ID); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("Seen by a non-owner = %v, want ErrDenied", err)
	}
	seen, err := e.sched.Seen(ctx, run.ID, e.member.ID)
	if err != nil || seen.OutcomeUnseen {
		t.Fatalf("Seen by the owner = %+v, %v; want outcome_unseen clear", seen, err)
	}
	again, err := e.sched.Seen(ctx, run.ID, e.member.ID)
	if err != nil || again.OutcomeUnseen {
		t.Fatalf("repeat Seen = %+v, %v; want the run, flag clear", again, err)
	}
	if _, err := e.sched.Seen(ctx, "run_missing", e.member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Seen on a missing run = %v, want ErrNotFound", err)
	}

	seenEvents, notes := 0, 0
	deadline := time.After(200 * time.Millisecond)
	for done := false; !done; {
		select {
		case ev := <-sub.Events():
			if ev.RunID != run.ID {
				continue
			}
			switch p := ev.Payload.(type) {
			case events.RunOutcomeSeenPayload:
				seenEvents++
				if ev.ActorID != e.member.ID {
					t.Fatalf("run.outcome_seen actor = %s, want the owner", ev.ActorID)
				}
			case events.TimelinePayload:
				if p.Kind == events.TimelineNote {
					notes++
				}
			}
		case <-deadline:
			done = true
		}
	}
	if seenEvents != 1 || notes != 1 {
		t.Fatalf("published %d run.outcome_seen and %d notes, want one of each", seenEvents, notes)
	}
}

// finishByDeadline arms a run on a harness without a status reporter with
// a reported success and lets the deadline finish it, returning once the
// retained owner settled.
func (e *testEnv) finishByDeadline(t *testing.T, run domain.RunID) {
	t.Helper()
	if err := e.sched.FinishReported(t.Context(), run, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.expireReportDeadline(t, run)
	e.waitStoreStatus(t, run, domain.RunCompleted)
	waitFor(t, "retained owner", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		owner := e.sched.runs[run]
		return owner != nil && owner.retained && !owner.reportFinishing
	})
}

// TestOnlyATurnEndWaitFinishesAnArmedRun: a permission prompt after the
// report is the agent still mid-turn. It parks the run for the member with
// its own reason, and only the turn-end wait that follows finishes it.
func TestOnlyATurnEndWaitFinishesAnArmedRun(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	sub := e.subscribe(t)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}

	permission := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonPermission}
	if err := e.sched.ReportAgentState(ctx, run.ID, permission); err != nil {
		t.Fatalf("permission wait: %v", err)
	}
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunNeedsAttention); p.Reason != agentstatus.ReasonPermission {
		t.Fatalf("permission park reason = %q, want %q", p.Reason, agentstatus.ReasonPermission)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	armed, finishing := entry.reported, entry.reportFinishing
	e.sched.mu.Unlock()
	if armed != domain.RunCompleted || finishing {
		t.Fatalf("after a permission wait: armed %q, finishing %v; want still armed, not finishing", armed, finishing)
	}

	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatalf("working: %v", err)
	}
	expectOnlyStatusEvent(t, sub, run.ID, domain.RunRunning)
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end wait: %v", err)
	}
	expectOnlyStatusEvent(t, sub, run.ID, domain.RunCompleted)
}

// TestTurnEndHarnessIsNotCutOffByTheDeadline: a harness that reports its
// turn end finishes on that report alone, however long the agent keeps
// working after it reported.
func TestTurnEndHarnessIsNotCutOffByTheDeadline(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	sub := e.subscribe(t)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.expireReportDeadline(t, run.ID)
	expectNoStatusEvent(t, sub, run.ID, "an overdue arm on a turn-end harness")
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end wait: %v", err)
	}
	expectOnlyStatusEvent(t, sub, run.ID, domain.RunCompleted)
}

// TestBlockedReasonWaitsForTheTurnEnd: a permission wait, and a stall on a
// harness that reports its turn end, keep their own reason and leave the
// blocked reason pending; the turn-end wait shows it. On a harness with no
// reporter the stall is the only park, so it shows the blocked reason.
func TestBlockedReasonWaitsForTheTurnEnd(t *testing.T) {
	t.Parallel()
	reason := func(t *testing.T, e *testEnv, run domain.RunID) string {
		t.Helper()
		r, err := e.db.GetRun(t.Context(), run)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		return r.Reason
	}
	stall := func(t *testing.T, e *testEnv, run domain.RunID) {
		t.Helper()
		waitFor(t, "the run to stall", func() bool {
			e.sched.checkStalls(t.Context())
			r, err := e.db.GetRun(t.Context(), run)
			return err == nil && r.Status == domain.RunNeedsAttention
		})
	}
	const blocked = "blocked: need the staging key"

	t.Run("permission wait", func(t *testing.T) {
		t.Parallel()
		e := newReportingEnv(t, nil)
		ctx := t.Context()
		run, _ := e.launchReporting(t)
		e.waitStoreStatus(t, run.ID, domain.RunRunning)
		if err := e.sched.ReportBlocked(ctx, run.ID, "report-blocked-1", "need the staging key", time.Now()); err != nil {
			t.Fatalf("ReportBlocked: %v", err)
		}
		permission := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonPermission}
		if err := e.sched.ReportAgentState(ctx, run.ID, permission); err != nil {
			t.Fatalf("permission wait: %v", err)
		}
		if got := reason(t, e, run.ID); got != agentstatus.ReasonPermission {
			t.Fatalf("permission park reason = %q, want %q", got, agentstatus.ReasonPermission)
		}
		if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
			t.Fatalf("working: %v", err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunRunning)
		if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
			t.Fatalf("turn-end wait: %v", err)
		}
		if got := reason(t, e, run.ID); got != blocked {
			t.Fatalf("turn-end park reason = %q, want %q", got, blocked)
		}
	})
	t.Run("stall mid-turn", func(t *testing.T) {
		t.Parallel()
		e := newReportingEnv(t, func(cfg *Config) { cfg.StallThreshold = time.Millisecond })
		ctx := t.Context()
		run, _ := e.launchReporting(t)
		e.waitStoreStatus(t, run.ID, domain.RunRunning)
		if err := e.sched.ReportBlocked(ctx, run.ID, "report-blocked-1", "need the staging key", time.Now()); err != nil {
			t.Fatalf("ReportBlocked: %v", err)
		}
		stall(t, e, run.ID)
		if got := reason(t, e, run.ID); !strings.HasPrefix(got, "stalled: ") {
			t.Fatalf("stall reason = %q, want a stall", got)
		}
		if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
			t.Fatalf("turn-end wait: %v", err)
		}
		if got := reason(t, e, run.ID); got != blocked {
			t.Fatalf("turn-end park reason = %q, want %q", got, blocked)
		}
	})
	t.Run("stall without a reporter", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, func(cfg *Config) { cfg.StallThreshold = time.Millisecond })
		ctx := t.Context()
		run, _ := e.launchFake(t, "no reporter")
		if err := e.sched.ReportBlocked(ctx, run.ID, "report-blocked-1", "need the staging key", time.Now()); err != nil {
			t.Fatalf("ReportBlocked: %v", err)
		}
		stall(t, e, run.ID)
		if got := reason(t, e, run.ID); got != blocked {
			t.Fatalf("stall reason = %q, want %q", got, blocked)
		}
	})
}

// TestReplayedBlockedReportStaysCleared: the outbox can hand the same
// blocked report over again after marking it published failed. Once the
// agent resumed past it, the replay must not bring the reason back.
func TestReplayedBlockedReportStaysCleared(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	working := agentstatus.Report{State: agentstatus.Working}
	blocked := func() {
		t.Helper()
		if err := e.sched.ReportBlocked(ctx, run.ID, "report-blocked-1", "need the staging key", time.Now()); err != nil {
			t.Fatalf("ReportBlocked: %v", err)
		}
	}
	report := func(r agentstatus.Report) {
		t.Helper()
		if err := e.sched.ReportAgentState(ctx, run.ID, r); err != nil {
			t.Fatalf("report %s: %v", r.State, err)
		}
	}

	blocked()
	report(waitingForInput)
	report(working)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	blocked()
	report(waitingForInput)
	if r := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); r.Reason != agentstatus.ReasonInput {
		t.Fatalf("reason after a replayed blocked report = %q, want %q", r.Reason, agentstatus.ReasonInput)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.BlockedReportID != "report-blocked-1" || sc.BlockedReason != "" {
		t.Fatalf("sidecar = %+v, %v; want the applied report ID and no reason", sc, err)
	}
}

// TestStaleReportNeverArmsAReopenedRun: the outbox can load a report before
// a relaunch supersedes it and hand it over after the run reopened. A
// report older than the current launch arms nothing and blocks nothing.
func TestStaleReportNeverArmsAReopenedRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "stale report")
	e.finishByDeadline(t, run.ID)
	before := time.Now()
	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}

	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, before); err != nil {
		t.Fatalf("stale FinishReported: %v", err)
	}
	if err := e.sched.ReportBlocked(ctx, run.ID, "report-old-blocked", "old blocker", before); err != nil {
		t.Fatalf("stale ReportBlocked: %v", err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	armed, blocked := entry.reported, entry.blockedReason
	e.sched.mu.Unlock()
	if armed != "" || blocked != "" {
		t.Fatalf("reopened run after stale reports: armed %q, blocked %q; want neither", armed, blocked)
	}
}

// TestRelaunchClearsTheArmBeforeReopening: the retained sidecar of a run its
// agent finished still carries the arm. Relaunch clears it on disk before
// it promotes the row, so a crash after the promotion cannot recover a
// reopened run that finishes itself.
func TestRelaunchClearsTheArmBeforeReopening(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run, _ := e.launchFake(t, "arm on disk")
	e.finishByDeadline(t, run.ID)
	if sc, err := e.sched.readSidecar(run.ID); err != nil || !sc.Retained || sc.ReportedOutcome != domain.RunCompleted {
		t.Fatalf("retained sidecar = %+v, %v; want it still armed", sc, err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	promoteErr := errors.New("row promotion unavailable")
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Store = &failingRunUpdateStore{Store: e.db, failAt: 1, err: promoteErr}
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	if _, err := s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, promoteErr) {
		t.Fatalf("Relaunch = %v, want the promotion error", err)
	}
	if sc, err := s2.readSidecar(run.ID); err != nil || sc.ReportedOutcome != "" {
		t.Fatalf("sidecar at the promotion = %+v, %v; want the arm cleared", sc, err)
	}
}

// crashArmedAtTurnEnd parks a reporting run at its turn end, stops the
// scheduler, and rewrites the run's sidecar as a crash would leave it: armed
// with a reported success, and when retained, with the retained marker a
// close writes just before its row transition. The row stays parked.
func (e *testEnv) crashArmedAtTurnEnd(t *testing.T, retained bool) domain.RunID {
	t.Helper()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end wait: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("readSidecar: %v", err)
	}
	sc.ReportedOutcome = domain.RunCompleted
	if retained {
		until := time.Now().UTC().Add(time.Hour)
		sc.Retained, sc.RetainedUntil = true, &until
	}
	if err := e.sched.writeSidecar(sc); err != nil {
		t.Fatalf("writeSidecar: %v", err)
	}
	return run.ID
}

// TestRecoveryFinishesAnArmedRunWhoseTurnEnded: a restart between the turn
// end and the finish, or a close that died between its retained marker and
// its row transition, leaves an armed run parked at its turn end. No
// further turn-end wait will come, so recovery finishes it on the first
// poll; the retained marker on the active row is dropped, the arm kept.
func TestRecoveryFinishesAnArmedRunWhoseTurnEnded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		retained bool
	}{{"restart mid-finish", false}, {"crash mid-close", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newReportingEnv(t, func(cfg *Config) {
				cfg.StallThreshold = time.Hour
				cfg.PollInterval = 10 * time.Millisecond
			})
			run := e.crashArmedAtTurnEnd(t, tc.retained)

			s2 := e.newScheduler(t, e.rt, newFakePTY())
			startScheduler(t, s2)
			row := e.waitStoreStatus(t, run, domain.RunCompleted)
			if row.Reason != reportedSuccessRetainedReason || !row.OutcomeUnseen {
				t.Fatalf("recovered finish = %q, unseen %v; want %q, unseen", row.Reason, row.OutcomeUnseen, reportedSuccessRetainedReason)
			}
		})
	}
}

// TestFailedRelaunchKeepsTheReportAndUnseenOutcome: a relaunch that rolls
// back leaves the run as it was - its terminal report still active, so the
// same agent cannot report a second outcome, and its outcome still unseen.
func TestFailedRelaunchKeepsTheReportAndUnseenOutcome(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run, _ := e.launchFake(t, "failed relaunch")
	first := &store.CoordReport{WorkspaceID: e.ws.ID, RunID: run.ID, Outcome: store.CoordOutcomeSuccess, Summary: "done", IdempotencyKey: "success-1"}
	if err := e.db.AppendCoordReport(ctx, first); err != nil {
		t.Fatalf("report: %v", err)
	}
	e.finishByDeadline(t, run.ID)
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	resumeErr := errors.New("runtime resume unavailable")
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = &resumeFailureRuntime{Runtime: e.rt, resumeErr: resumeErr}
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	if _, err := s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, resumeErr) {
		t.Fatalf("Relaunch = %v, want the resume error", err)
	}
	if got, err := e.db.GetCoordReport(ctx, first.ID); err != nil || got.SupersededAt != nil {
		t.Fatalf("report after a failed relaunch = %+v, %v; want it still active", got, err)
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil || row.Status != domain.RunCompleted || row.Reason != reportedSuccessRetainedReason || !row.OutcomeUnseen {
		t.Fatalf("row after a failed relaunch = %+v, %v; want the retained completed row, outcome unseen", row, err)
	}
}

// TestReportAfterExitOverridesTheExitOutcome: a report whose hand-off was
// deferred reaches the scheduler after the headless process already exited
// and recorded its exit code. The report still decides the run, even one
// finalized while the run was provisioning, unless a relaunch superseded
// it.
func TestReportAfterExitOverridesTheExitOutcome(t *testing.T) {
	t.Parallel()
	exited := func(t *testing.T, e *testEnv, task string, code int, want domain.RunStatus) *domain.Run {
		t.Helper()
		run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "fake", domain.LaunchHeadless)
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunRunning)
		e.rt.byName(string(run.ID)).exitNow(code)
		row := e.waitStoreStatus(t, run.ID, want)
		waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
		return row
	}
	reported := func(t *testing.T, e *testEnv, run domain.RunID, outcome store.CoordOutcome) *store.CoordReport {
		t.Helper()
		report := &store.CoordReport{WorkspaceID: e.ws.ID, RunID: run, Outcome: outcome, Summary: "reported", IdempotencyKey: "report-1"}
		if err := e.db.AppendCoordReport(t.Context(), report); err != nil {
			t.Fatalf("AppendCoordReport: %v", err)
		}
		return report
	}

	t.Run("override", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		row := exited(t, e, "exit first", 1, domain.RunFailed)
		if row.Reason != "agent exited 1" {
			t.Fatalf("exit reason = %q, want agent exited 1", row.Reason)
		}
		report := reported(t, e, row.ID, store.CoordOutcomeSuccess)
		sub := e.subscribe(t)
		if err := e.sched.FinishReported(ctx, row.ID, report.ID, domain.RunCompleted, *report.FinalizedAt); err != nil {
			t.Fatalf("FinishReported after exit: %v", err)
		}
		p := expectOnlyStatusEvent(t, sub, row.ID, domain.RunCompleted)
		if p.From != domain.RunFailed || p.Reason != reportedSuccessReason || !p.OutcomeUnseen {
			t.Fatalf("override event = %+v, want failed -> completed because %q, outcome unseen", p, reportedSuccessReason)
		}
		got, err := e.db.GetRun(ctx, row.ID)
		if err != nil || got.Status != domain.RunCompleted || got.Reason != reportedSuccessReason || !got.OutcomeUnseen {
			t.Fatalf("row = %+v, %v; want completed because %q, outcome unseen", got, err, reportedSuccessReason)
		}
		if err := e.sched.FinishReported(ctx, row.ID, report.ID, domain.RunCompleted, *report.FinalizedAt); err != nil {
			t.Fatalf("replayed FinishReported: %v", err)
		}
		expectNoStatusEvent(t, sub, row.ID, "a replayed report")
	})
	t.Run("report finalized while provisioning", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		row := exited(t, e, "early report", 1, domain.RunFailed)
		report := reported(t, e, row.ID, store.CoordOutcomeSuccess)
		if err := e.sched.FinishReported(ctx, row.ID, report.ID, domain.RunCompleted, row.StartedAt.Add(-time.Second)); err != nil {
			t.Fatalf("early FinishReported: %v", err)
		}
		got, err := e.db.GetRun(ctx, row.ID)
		if err != nil || got.Status != domain.RunCompleted || got.Reason != reportedSuccessReason || !got.OutcomeUnseen {
			t.Fatalf("row = %+v, %v; want completed because %q, outcome unseen", got, err, reportedSuccessReason)
		}
	})
	t.Run("superseded report is ignored", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		row := exited(t, e, "old report", 0, domain.RunCompleted)
		report := reported(t, e, row.ID, store.CoordOutcomeFailure)
		if err := e.db.SupersedeCoordTerminalReport(ctx, row.ID); err != nil {
			t.Fatalf("SupersedeCoordTerminalReport: %v", err)
		}
		if err := e.sched.FinishReported(ctx, row.ID, report.ID, domain.RunFailed, time.Now()); err != nil {
			t.Fatalf("superseded FinishReported: %v", err)
		}
		got, err := e.db.GetRun(ctx, row.ID)
		if err != nil || got.Status != domain.RunCompleted || got.Reason != exitedCompletedReason || got.OutcomeUnseen {
			t.Fatalf("row = %+v, %v; want the exit's completed row untouched", got, err)
		}
	})
	t.Run("kill", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "kill first", "fake", domain.LaunchHeadless)
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunRunning)
		if err := e.sched.Kill(ctx, run.ID, e.member.ID); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
		if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
			t.Fatalf("FinishReported after kill: %v", err)
		}
		if got, err := e.db.GetRun(ctx, run.ID); err != nil || got.Status != domain.RunAbandoned || got.Reason != "killed" {
			t.Fatalf("row = %+v, %v; want the kill untouched", got, err)
		}
	})
}

// TestReportAfterTheTurnEndedFinishesAtOnce: the report's hand-off can
// arrive after the agent already parked at its turn end, and no further
// turn-end wait will come. The run finishes on the hand-off. A relaunch
// forgets that turn end, so the reopened agent's own report waits for its
// own.
func TestReportAfterTheTurnEndedFinishesAtOnce(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end wait: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	sub := e.subscribe(t)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if p.From != domain.RunNeedsAttention || p.Reason != reportedSuccessRetainedReason {
		t.Fatalf("finish event = %+v, want needs-attention -> completed because %q", p, reportedSuccessRetainedReason)
	}
	waitFor(t, "retained owner", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		owner := e.sched.runs[run.ID]
		return owner != nil && owner.retained && !owner.reportFinishing
	})

	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	expectOnlyStatusEvent(t, sub, run.ID, domain.RunRunning)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported after relaunch: %v", err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	armed, finishing := entry.reported, entry.reportFinishing
	e.sched.mu.Unlock()
	if armed != domain.RunCompleted || finishing {
		t.Fatalf("relaunched run after its report: armed %q, finishing %v; want armed, waiting for its turn end", armed, finishing)
	}
	expectNoStatusEvent(t, sub, run.ID, "a report before the reopened turn ended")
}

// failingReportedFinishStore fails the next fail reported-finish row writes.
type failingReportedFinishStore struct {
	store.Store
	fail atomic.Int32
}

func (s *failingReportedFinishStore) FinishRunReported(ctx context.Context, id domain.RunID, status domain.RunStatus, reason string, startedAt, finishedAt *time.Time) error {
	if s.fail.Add(-1) >= 0 {
		return errors.New("test: reported finish row write failed")
	}
	return s.Store.FinishRunReported(ctx, id, status, reason, startedAt, finishedAt)
}

// TestFailedReportedFinishIsRetried: a finish whose close failed is retried
// by the poll loop once reportFinishDeadline passes again, whether the turn
// end came before the report or after it. The agent is idle at its prompt
// and will not end another turn on its own.
func TestFailedReportedFinishIsRetried(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		parkedBefore bool
	}{{"parked before the report", true}, {"turn end after the report", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failing := &failingReportedFinishStore{}
			e := newReportingEnv(t, func(cfg *Config) {
				failing.Store = cfg.Store
				cfg.Store = failing
			})
			ctx := t.Context()
			run, _ := e.launchReporting(t)
			e.waitStoreStatus(t, run.ID, domain.RunRunning)
			failing.fail.Store(1)
			if tc.parkedBefore {
				if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
					t.Fatalf("turn-end wait: %v", err)
				}
			}
			if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
				t.Fatalf("FinishReported: %v", err)
			}
			if !tc.parkedBefore {
				if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
					t.Fatalf("turn-end wait: %v", err)
				}
			}
			e.sched.mu.Lock()
			entry := e.sched.runs[run.ID]
			e.sched.mu.Unlock()
			waitFor(t, "the failed finish", func() bool {
				e.sched.mu.Lock()
				defer e.sched.mu.Unlock()
				return failing.fail.Load() <= 0 && !entry.reportFinishing
			})
			if row, err := e.db.GetRun(ctx, run.ID); err != nil || row.Status.Terminal() {
				t.Fatalf("row after the failed finish = %+v, %v; want it still live", row, err)
			}

			e.expireReportDeadline(t, run.ID)
			if row := e.waitStoreStatus(t, run.ID, domain.RunCompleted); row.Reason != reportedSuccessRetainedReason {
				t.Fatalf("retried finish reason = %q, want %q", row.Reason, reportedSuccessRetainedReason)
			}
		})
	}
}

// TestReportFinalizedWhileProvisioningStillArms: the coordination socket
// answers before the run turns running, so a report can be finalized
// before the launch's StartedAt and handed off after it. Only a relaunch
// makes a report stale.
func TestReportFinalizedWhileProvisioningStillArms(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "early report")
	row := e.waitStoreStatus(t, run.ID, domain.RunRunning)
	early := row.StartedAt.Add(-time.Second)
	if err := e.sched.ReportBlocked(ctx, run.ID, "report-blocked-1", "need the staging key", early); err != nil {
		t.Fatalf("ReportBlocked: %v", err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, early); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	armed, blocked := entry.reported, entry.blockedReportID
	e.sched.mu.Unlock()
	if armed != domain.RunCompleted || blocked != "report-blocked-1" {
		t.Fatalf("after reports finalized while provisioning: armed %q, blocked %q; want both applied", armed, blocked)
	}
}

// exitOverrideBus hands a late success report to the scheduler the moment
// a run's exit records failed, as a deferred outbox hand-off can while
// finalize is still running.
type exitOverrideBus struct {
	events.Bus
	sched    atomic.Pointer[Scheduler]
	reportID atomic.Value
	once     sync.Once
	err      error
}

func (b *exitOverrideBus) Publish(ctx context.Context, ev events.Event) (events.Event, error) {
	out, err := b.Bus.Publish(ctx, ev)
	if p, ok := ev.Payload.(events.RunStatusPayload); ok && p.To == domain.RunFailed &&
		strings.HasPrefix(p.Reason, exitedFailedReasonPrefix) {
		if s := b.sched.Load(); s != nil {
			// publish runs under s.mu, which overrideExitLocked needs.
			b.once.Do(func() { b.err = s.overrideExitLocked(ctx, ev.RunID, b.reportID.Load().(string), domain.RunCompleted) })
		}
	}
	return out, err
}

// TestFinishEvidenceFollowsAnOverriddenExit: a report that overrides the
// exit while finalize is still running decides the finish evidence too.
func TestFinishEvidenceFollowsAnOverriddenExit(t *testing.T) {
	t.Parallel()
	bus := &exitOverrideBus{}
	e := newTestEnv(t, func(cfg *Config) {
		bus.Bus = cfg.Bus
		cfg.Bus = bus
	})
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	bus.sched.Store(e.sched)
	ctx := t.Context()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "late report", "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	report := &store.CoordReport{WorkspaceID: e.ws.ID, RunID: run.ID, Outcome: store.CoordOutcomeSuccess, Summary: "done", IdempotencyKey: "report-1"}
	if err := e.db.AppendCoordReport(ctx, report); err != nil {
		t.Fatalf("AppendCoordReport: %v", err)
	}
	bus.reportID.Store(report.ID)
	e.rt.byName(string(run.ID)).exitNow(1)
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	if bus.err != nil {
		t.Fatalf("overrideExitLocked: %v", bus.err)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunCompleted); row.Reason != reportedSuccessReason {
		t.Fatalf("row reason = %q, want %q", row.Reason, reportedSuccessReason)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var keys []string
	for _, req := range capture.reqs {
		if strings.HasPrefix(req.IdempotencyKey, "finish:"+string(run.ID)+":") {
			keys = append(keys, req.IdempotencyKey)
		}
	}
	if len(keys) != 1 || !strings.HasSuffix(keys[0], ":"+string(domain.RunCompleted)) {
		t.Fatalf("finish evidence keys = %v, want one for the completed row", keys)
	}
}

// TestStalledArmedRunFinishesAtTheDeadline: a hook that never delivers the
// turn end leaves a run on a reporter harness armed. Once it stalls into
// needs-attention it is not working, so the deadline finishes it. A
// permission prompt after the report is mid-turn: the deadline leaves it
// for the owner to answer.
func TestStalledArmedRunFinishesAtTheDeadline(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, func(cfg *Config) { cfg.StallThreshold = time.Millisecond })
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	sub := e.subscribe(t)
	permission := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonPermission}
	if err := e.sched.ReportAgentState(ctx, run.ID, permission); err != nil {
		t.Fatalf("permission wait: %v", err)
	}
	expectOnlyStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	e.expireReportDeadline(t, run.ID)
	expectNoStatusEvent(t, sub, run.ID, "an overdue arm parked at a permission prompt")
	e.sched.mu.Lock()
	finishing := e.sched.runs[run.ID].reportFinishing
	e.sched.mu.Unlock()
	if finishing {
		t.Fatal("the deadline started finishing a run parked at a permission prompt")
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatalf("working: %v", err)
	}
	expectOnlyStatusEvent(t, sub, run.ID, domain.RunRunning)
	waitFor(t, "the run to stall", func() bool {
		e.sched.checkStalls(ctx)
		r, err := e.db.GetRun(ctx, run.ID)
		return err == nil && r.Status == domain.RunNeedsAttention
	})
	e.expireReportDeadline(t, run.ID)
	if row := e.waitStoreStatus(t, run.ID, domain.RunCompleted); row.Reason != reportedSuccessRetainedReason {
		t.Fatalf("finish reason = %q, want %q", row.Reason, reportedSuccessRetainedReason)
	}
}

// TestOlderBlockedReportNeverReplacesANewerOne: an older blocked report
// whose publication is retried after a newer one landed changes nothing,
// and the sidecar keeps the newer one's finalized time for a restart.
func TestOlderBlockedReportNeverReplacesANewerOne(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "two blockers")
	older := time.Now().UTC()
	newer := older.Add(time.Second)
	if err := e.sched.ReportBlocked(ctx, run.ID, "report-blocked-2", "need the prod key", newer); err != nil {
		t.Fatalf("newer ReportBlocked: %v", err)
	}
	if err := e.sched.ReportBlocked(ctx, run.ID, "report-blocked-1", "need the staging key", older); err != nil {
		t.Fatalf("older ReportBlocked: %v", err)
	}
	e.sched.mu.Lock()
	reason := e.sched.runs[run.ID].blockedReason
	e.sched.mu.Unlock()
	if reason != "blocked: need the prod key" {
		t.Fatalf("blocked reason = %q, want the newer report's", reason)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.BlockedReportAt == nil || !sc.BlockedReportAt.Equal(newer) {
		t.Fatalf("sidecar = %+v, %v; want the newer report's finalized time", sc, err)
	}
}
