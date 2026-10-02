package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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

	if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
		t.Fatalf("FinishReported replay: %v", err)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.ReportedOutcome != domain.RunCompleted {
		t.Fatalf("armed sidecar = %+v, %v; want reported_outcome completed", sc, err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end waiting report: %v", err)
	}

	p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if p.Reason != reportedSuccessRetainedReason || p.From != domain.RunRunning {
		t.Fatalf("finish event = %+v, want running -> completed because %q", p, reportedSuccessRetainedReason)
	}
	expectNoStatusEvent(t, sub, run.ID, "a finished run")
	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Reason != reportedSuccessRetainedReason {
		t.Fatalf("stored reason = %q, want %q", row.Reason, reportedSuccessRetainedReason)
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

	// A human can still decide the finished run's disposition.
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun after reported finish: %v", err)
	}
	if merged := e.waitStoreStatus(t, run.ID, domain.RunMerged); merged.Reason != retainedCloseReason {
		t.Fatalf("merged reason = %q, want %q", merged.Reason, retainedCloseReason)
	}
}

// TestReportedFailureFinishesAtDeadlineAndRelaunches: with no turn-end
// report, the deadline finishes the run, and the failed run still reopens.
func TestReportedFailureFinishesAtDeadlineAndRelaunches(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, container := e.launchFake(t, "fix flaky test")
	if err := e.sched.FinishReported(ctx, run.ID, domain.RunFailed); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.expireReportDeadline(t, run.ID)

	row := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if row.Reason != reportedFailureRetainedReason {
		t.Fatalf("stored reason = %q, want %q", row.Reason, reportedFailureRetainedReason)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "wip: fix flaky test" {
		t.Fatalf("commits = %v, want one wip: commit", got)
	}
	reopened, err := e.sched.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch failed run: %v", err)
	}
	if reopened.Status != domain.RunRunning || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("relaunched = %+v, want the same running run and container", reopened)
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
	if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.rt.byName(string(run.ID)).exitNow(1)

	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Reason != reportedSuccessReason {
		t.Fatalf("stored reason = %q, want %q", row.Reason, reportedSuccessReason)
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
		if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
			t.Fatalf("FinishReported: %v", err)
		}
		if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
			t.Fatalf("CloseRun: %v", err)
		}
		sub := e.subscribe(t)
		e.expireReportDeadlineAfterClose(t, run.ID)
		expectNoStatusEvent(t, sub, run.ID, "an armed finish after a human close")
		row := e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
		if row.Reason != retainedCloseReason {
			t.Fatalf("reason = %q, want the human close %q", row.Reason, retainedCloseReason)
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
		if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
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
		if row := e.waitStoreStatus(t, run.ID, domain.RunAbandoned); row.Reason != "killed" {
			t.Fatalf("reason after the armed finish = %q, want killed", row.Reason)
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

	if err := e.sched.ReportBlocked(ctx, run.ID, "need the staging key"); err != nil {
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
	if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
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
	if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
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
	if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
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
	if err := e.sched.FinishReported(ctx, run.ID, domain.RunCompleted); err != nil {
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

	e.sched.sweepRetained(ctx)
	waitFor(t, "expired container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Reason != reportedSuccessReason {
		t.Fatalf("expired reason = %q, want %q", row.Reason, reportedSuccessReason)
	}
	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Relaunch after expiry = %v, want ErrInvalidTransition", err)
	}
}
