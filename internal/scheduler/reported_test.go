package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

var waitingForInput = agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}

// permissionInput opens (or closes) one permission request, the input-only
// report a native hook sends while the agent waits mid-turn.
func permissionInput(operation string) agentstatus.Report {
	return agentstatus.Report{InputUpdates: []domain.RunInputUpdate{{
		Operation: operation, SessionID: "session-1", Kind: "permission", ID: "permission-1",
	}}}
}

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
	if entry == nil || (entry.reported == "" && entry.idleReason == "") {
		e.sched.mu.Unlock()
		t.Fatalf("run %s is not armed", run)
	}
	entry.reportedAt = time.Now().UTC().Add(-reportFinishDeadline)
	if entry.idleReason != "" {
		entry.idleReportAt = time.Now().UTC().Add(-reportFinishDeadline)
	}
	e.sched.mu.Unlock()
	e.sched.finishOverdueReports()
}

// A reported task is done, but its interactive execution lifetime stays open.
func TestReportedSuccessParksTUIRunAtTurnEnd(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	run, container := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	sub := e.subscribe(t)
	ctx := t.Context()
	at := time.Now().UTC()
	for range 2 {
		if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, at); err != nil {
			t.Fatal(err)
		}
	}
	// The tool's Working callback is still part of the reporting turn.
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatal(err)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.IdleReason != reportedSuccessReason || sc.IdleShown {
		t.Fatalf("pending outcome = %+v, %v", sc, err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p.Reason != reportedSuccessReason || p.From != domain.RunRunning || !p.OutcomeUnseen || !p.FinishUnopened {
		t.Fatalf("park event = %+v", p)
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if row.Reason != reportedSuccessReason || !row.OutcomeUnseen || !row.FinishUnopened || row.FinishedAt != nil {
		t.Fatalf("parked row = %+v", row)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 0 {
		t.Fatalf("report unexpectedly finalized checkout: %v", got)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || sc.Retained || sc.Paused || sc.RetainedUntil != nil || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("park changed execution lifetime: %+v, %v", sc, err)
	}
	before := time.Now().UTC()
	if err = e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunMerged); p.OutcomeUnseen || p.FinishUnopened {
		t.Fatalf("close event = %+v, want a member's close to clear both flags", p)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunMerged); row.OutcomeUnseen || row.FinishUnopened {
		t.Fatalf("closed row = %+v, want neither flag", row)
	}
	sc, err = e.sched.readSidecar(run.ID)
	if err != nil || !sc.Retained || !sc.Paused || sc.RetainedUntil == nil ||
		sc.RetainedUntil.Before(before.Add(7*24*time.Hour)) {
		t.Fatalf("explicit close retention = %+v, %v", sc, err)
	}
}

func TestReportedFailureParksAtDeadlineAndResumesWithoutRelaunch(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, container := e.launchFake(t, "fix flaky test")
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunFailed, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.expireReportDeadline(t, run.ID)
	row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if row.Reason != reportedFailureReason || !row.OutcomeUnseen || row.FinishedAt != nil {
		t.Fatalf("parked failure = %+v", row)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatal(err)
	}
	fresh := e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if fresh.OutcomeUnseen || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("follow-up did not retain the same live run: %+v", fresh)
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
	if got := reason(); got != agentstatus.ReasonIdle {
		t.Fatalf("reason after the agent resumed = %q, want %q", got, agentstatus.ReasonIdle)
	}
}

// The pending outcome survives a server restart, but never closes the run.
func TestRecoveryParksAnArmedInteractiveRun(t *testing.T) {
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
		return owner != nil && owner.idleReason == reportedSuccessReason
	})
	if err := s2.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end report after restart: %v", err)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); row.Reason != reportedSuccessReason {
		t.Fatalf("reason = %q, want %q", row.Reason, reportedSuccessReason)
	}
}

// Repeated completion is a new task outcome in the same execution lifetime.
func TestInteractiveRunReportsAgainWithoutRelaunch(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, container := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	first := &store.CoordReport{WorkspaceID: e.ws.ID, RunID: run.ID, Outcome: store.CoordOutcomeSuccess, Summary: "round one", IdempotencyKey: "round-1"}
	if err := e.db.AppendCoordReport(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, first.ID, domain.RunCompleted, *first.FinalizedAt); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.Seen(ctx, run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatal(err)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunRunning); row.OutcomeUnseen || row.FinishUnopened {
		t.Fatalf("follow-up row = %+v, want neither flag", row)
	}
	second := &store.CoordReport{WorkspaceID: e.ws.ID, RunID: run.ID, Outcome: store.CoordOutcomeSuccess, Summary: "round two", IdempotencyKey: "round-2"}
	if err := e.db.AppendCoordReport(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, second.ID, domain.RunCompleted, *second.FinalizedAt); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, first.ID, domain.RunFailed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if row.Reason != reportedSuccessReason || !row.OutcomeUnseen || !row.FinishUnopened || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("second outcome = %+v", row)
	}
	if got, err := e.db.GetCoordReport(ctx, first.ID); err != nil || got.SupersededAt == nil {
		t.Fatalf("superseded first report = %+v, %v", got, err)
	}
}

// TestRetainedExpiryRelabelsAReportedRun: the status stays, the retention
// promise leaves the reason, relaunch is refused, and a finish a teammate
// opened does not read as unopened again.
func TestRetainedExpiryRelabelsAReportedRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "expire me")
	e.retainLegacyReportedRun(t, run.ID)
	if _, err := e.sched.Seen(ctx, run.ID, newSteerer(t, e, "Cody", "", "").ID); err != nil {
		t.Fatalf("Seen by a teammate: %v", err)
	}
	e.sched.mu.Lock()
	past := time.Now().UTC().Add(-time.Second)
	e.sched.runs[run.ID].retainedUntil = &past
	e.sched.mu.Unlock()
	finished := e.waitStoreStatus(t, run.ID, domain.RunCompleted).StatusChangedAt

	sub := e.subscribe(t)
	e.sched.sweepRetained(ctx)
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunCompleted); p.Reason != reportedSuccessReason || !p.OutcomeUnseen || p.FinishUnopened {
		t.Fatalf("expiry relabel event = %+v, want %q with the outcome still unseen and the finish still opened", p, reportedSuccessReason)
	}
	waitFor(t, "expired container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Reason != reportedSuccessReason || !row.OutcomeUnseen || row.FinishUnopened {
		t.Fatalf("expired row = %q, unseen %v, unopened %v; want %q, unseen, opened", row.Reason, row.OutcomeUnseen, row.FinishUnopened, reportedSuccessReason)
	}
	if finished == nil || !row.StatusChangedAt.Equal(*finished) {
		t.Fatalf("expired row StatusChangedAt = %v, want the finish at %v", row.StatusChangedAt, finished)
	}
	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Relaunch after expiry = %v, want ErrInvalidTransition", err)
	}
}

// TestSeenClearsEachFlagForWhoOpens: any member's open clears finish_unopened,
// only the owner's clears outcome_unseen, and one call publishes one event:
// run.outcome_seen with its timeline note when the outcome was unseen,
// run.finish_opened otherwise, nothing on a repeat or after a member's close.
func TestSeenClearsEachFlagForWhoOpens(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	other := newSteerer(t, e, "Cody", "", "")
	park := func(task string) domain.RunID {
		t.Helper()
		run, _ := e.launchFake(t, task)
		if err := e.sched.FinishReported(ctx, run.ID, "report-"+task, domain.RunCompleted, time.Now()); err != nil {
			t.Fatalf("FinishReported: %v", err)
		}
		e.expireReportDeadline(t, run.ID)
		if row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); !row.OutcomeUnseen || !row.FinishUnopened {
			t.Fatalf("reported park = %+v, want outcome_unseen and finish_unopened", row)
		}
		return run.ID
	}
	seen := func(run domain.RunID, actor domain.MemberID, wantUnseen bool) {
		t.Helper()
		got, err := e.sched.Seen(ctx, run, actor)
		if err != nil || got.FinishUnopened || got.OutcomeUnseen != wantUnseen {
			t.Fatalf("Seen by %s = %+v, %v; want finish_unopened clear, outcome_unseen %v", actor, got, err, wantUnseen)
		}
	}
	teammateFirst, ownerFirst := park("teammate first"), park("owner first")
	sub := e.subscribe(t)

	seen(teammateFirst, other.ID, true)
	seen(teammateFirst, e.member.ID, false)
	seen(teammateFirst, e.member.ID, false)
	seen(ownerFirst, e.member.ID, false)
	seen(ownerFirst, other.ID, false)
	e.rt.byName(string(ownerFirst)).exitNow(0)
	if row := e.waitStoreStatus(t, ownerFirst, domain.RunCompleted); row.OutcomeUnseen || !row.FinishUnopened {
		t.Fatalf("exited row = %+v, want finish_unopened set again and no outcome to review", row)
	}
	seen(ownerFirst, e.member.ID, false)
	if err := e.sched.CloseRun(ctx, ownerFirst, other.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, ownerFirst, domain.RunMerged)
	seen(ownerFirst, e.member.ID, false)
	if _, err := e.sched.Seen(ctx, "run_missing", e.member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Seen on a missing run = %v, want ErrNotFound", err)
	}

	var got []string
	deadline := time.After(200 * time.Millisecond)
	for done := false; !done; {
		select {
		case ev := <-sub.Events():
			switch p := ev.Payload.(type) {
			case events.RunOutcomeSeenPayload, events.RunFinishOpenedPayload:
				got = append(got, fmt.Sprintf("%s %s %s", ev.Type, ev.RunID, ev.ActorID))
			case events.TimelinePayload:
				if p.Message == "outcome seen by owner" {
					got = append(got, fmt.Sprintf("note %s %s", ev.RunID, ev.ActorID))
				}
			}
		case <-deadline:
			done = true
		}
	}
	want := []string{
		fmt.Sprintf("run.finish_opened %s %s", teammateFirst, other.ID),
		fmt.Sprintf("run.outcome_seen %s %s", teammateFirst, e.member.ID),
		fmt.Sprintf("note %s %s", teammateFirst, e.member.ID),
		fmt.Sprintf("run.outcome_seen %s %s", ownerFirst, e.member.ID),
		fmt.Sprintf("note %s %s", ownerFirst, e.member.ID),
		fmt.Sprintf("run.finish_opened %s %s", ownerFirst, e.member.ID),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("seen events =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// retainLegacyReportedRun models a retained terminal outcome written by an
// older server. Upgrades must still preserve its expiry and relaunch safeguards.
func (e *testEnv) retainLegacyReportedRun(t *testing.T, run domain.RunID) {
	t.Helper()
	if err := e.sched.CloseRun(t.Context(), run, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	if err := e.db.FinishRunReported(t.Context(), run, domain.RunCompleted, reportedSuccessRetainedReason, nil, nil); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	entry := e.sched.runs[run]
	entry.status, entry.reported = domain.RunCompleted, domain.RunCompleted
	if err := e.sched.writeSidecar(entry.sidecar()); err != nil {
		t.Fatal(err)
	}
}

// Input-only close settles the outcome only when execution also ended.
func TestOnlyATurnEndWaitShowsAnInteractiveOutcome(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	sub := e.subscribe(t)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}

	if err := e.sched.ReportAgentState(ctx, run.ID, permissionInput("open")); err != nil {
		t.Fatalf("permission wait: %v", err)
	}
	expectNoStatusEvent(t, sub, run.ID, "an input-only report on an armed run")
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("idle with a permission open: %v", err)
	}
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunNeedsAttention); p.Reason != agentstatus.ReasonIdle {
		t.Fatalf("park reason with a permission open = %q, want %q", p.Reason, agentstatus.ReasonIdle)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	armed, shown := entry.idleReason, entry.idleShown
	e.sched.mu.Unlock()
	if armed != reportedSuccessReason || shown {
		t.Fatalf("permission wait: outcome %q, shown %v", armed, shown)
	}

	if err := e.sched.ReportAgentState(ctx, run.ID, permissionInput("close")); err != nil {
		t.Fatalf("permission answered: %v", err)
	}
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunNeedsAttention); p.Reason != reportedSuccessReason || !p.OutcomeUnseen {
		t.Fatalf("settled outcome after input close = %+v", p)
	}
}

func TestInteractiveOutcomeWaitsForNativeTurnEndBeyondDeadline(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	sub := e.subscribe(t)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.sched.mu.Lock()
	e.sched.runs[run.ID].idleReportAt = time.Now().UTC().Add(-reportFinishDeadline)
	e.sched.mu.Unlock()
	e.sched.finishOverdueReports()
	expectNoStatusEvent(t, sub, run.ID, "an overdue outcome while the agent is still working")
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatalf("turn-end wait: %v", err)
	}
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunNeedsAttention); p.Reason != reportedSuccessReason || !p.OutcomeUnseen {
		t.Fatalf("settled interactive outcome = %+v", p)
	}
}

// TestBlockedReasonWaitsForTheTurnEnd: a permission wait does not park the
// run, and a stall on a harness that reports its turn end keeps its own
// reason; both leave the blocked reason pending, and the turn-end idle
// report shows it. On a harness with no
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
		if err := e.sched.ReportAgentState(ctx, run.ID, permissionInput("open")); err != nil {
			t.Fatalf("permission wait: %v", err)
		}
		if r, err := e.db.GetRun(ctx, run.ID); err != nil || r.Status != domain.RunRunning {
			t.Fatalf("run with a permission open = %+v, %v; want it still running", r, err)
		}
		if err := e.sched.ReportAgentState(ctx, run.ID, permissionInput("close")); err != nil {
			t.Fatalf("permission answered: %v", err)
		}
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
	if r := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); r.Reason != agentstatus.ReasonIdle {
		t.Fatalf("reason after a replayed blocked report = %q, want %q", r.Reason, agentstatus.ReasonIdle)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.IdleReportID != "report-blocked-1" || sc.IdleReason != "" {
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
	e.retainLegacyReportedRun(t, run.ID)
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
	armed, blocked := entry.reported, entry.idleReason
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
	e.retainLegacyReportedRun(t, run.ID)
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
	run, container := e.launchReporting(t)
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
		sc.Retained, sc.RetainedUntil, sc.Paused = true, &until, true
		if err := e.rt.Pause(t.Context(), container.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.sched.writeSidecar(sc); err != nil {
		t.Fatalf("writeSidecar: %v", err)
	}
	return run.ID
}

// Pending outcomes from an older server's terminal-finish path recover into a
// live park, including a crash between its retained marker and row transition.
func TestRecoveryParksLegacyArmedRunWhoseTurnEnded(t *testing.T) {
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
			waitFor(t, "recovered outcome park", func() bool {
				row, err := e.db.GetRun(t.Context(), run)
				return err == nil && row.Status == domain.RunNeedsAttention && row.Reason == reportedSuccessReason && row.OutcomeUnseen
			})
			if sc, err := s2.readSidecar(run); err != nil || sc.Retained || sc.Paused || sc.RetainedUntil != nil || sc.ReportedOutcome != "" {
				t.Fatalf("recovered live sidecar = %+v, %v", sc, err)
			}
		})
	}
}

// TestFailedRelaunchKeepsTheReportAndUnseenOutcome: a relaunch that rolls
// back leaves the run as it was - its terminal report still active, so the
// same agent cannot report a second outcome, its outcome still unseen, and
// the finish a member opened still opened.
func TestFailedRelaunchKeepsTheReportAndUnseenOutcome(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run, _ := e.launchFake(t, "failed relaunch")
	first := &store.CoordReport{WorkspaceID: e.ws.ID, RunID: run.ID, Outcome: store.CoordOutcomeSuccess, Summary: "done", IdempotencyKey: "success-1"}
	if err := e.db.AppendCoordReport(ctx, first); err != nil {
		t.Fatalf("report: %v", err)
	}
	e.retainLegacyReportedRun(t, run.ID)
	if changed, err := e.db.ClearRunFinishUnopened(ctx, run.ID); err != nil || !changed {
		t.Fatalf("ClearRunFinishUnopened = %v, %v; want the retained finish opened", changed, err)
	}
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
	if err != nil || row.Status != domain.RunCompleted || row.Reason != reportedSuccessRetainedReason || !row.OutcomeUnseen || row.FinishUnopened {
		t.Fatalf("row after a failed relaunch = %+v, %v; want the retained completed row, outcome unseen, finish opened", row, err)
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
		if row.Reason != "agent exited 1" || !row.FinishUnopened || row.OutcomeUnseen {
			t.Fatalf("exit row = %+v, want agent exited 1, unopened, with no outcome to review", row)
		}
		report := reported(t, e, row.ID, store.CoordOutcomeSuccess)
		sub := e.subscribe(t)
		if err := e.sched.FinishReported(ctx, row.ID, report.ID, domain.RunCompleted, *report.FinalizedAt); err != nil {
			t.Fatalf("FinishReported after exit: %v", err)
		}
		p := expectOnlyStatusEvent(t, sub, row.ID, domain.RunCompleted)
		if p.From != domain.RunFailed || p.Reason != reportedSuccessReason || !p.OutcomeUnseen || !p.FinishUnopened {
			t.Fatalf("override event = %+v, want failed -> completed because %q, outcome unseen, finish unopened", p, reportedSuccessReason)
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

// A hand-off arriving after turn end parks at once. Follow-up Working clears
// that boundary, so the next report waits for its own turn end.
func TestReportAfterTheTurnEndedParksAtOnce(t *testing.T) {
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
	p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p.From != domain.RunNeedsAttention || p.Reason != reportedSuccessReason || !p.OutcomeUnseen {
		t.Fatalf("settled outcome = %+v", p)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatal(err)
	}
	expectOnlyStatusEvent(t, sub, run.ID, domain.RunRunning)
	if err := e.sched.FinishReported(ctx, run.ID, "report-2", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported after follow-up: %v", err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	armed, shown := entry.idleReason, entry.idleShown
	e.sched.mu.Unlock()
	if armed != reportedSuccessReason || shown {
		t.Fatalf("follow-up outcome %q, shown %v; want pending", armed, shown)
	}
	expectNoStatusEvent(t, sub, run.ID, "a report before the follow-up turn ended")
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
			run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "background report", "claude", domain.LaunchHeadless)
			if err != nil {
				t.Fatal(err)
			}
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
			if row := e.waitStoreStatus(t, run.ID, domain.RunCompleted); row.Reason != reportedSuccessReason {
				t.Fatalf("retried finish reason = %q, want %q", row.Reason, reportedSuccessReason)
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
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, early.Add(time.Nanosecond)); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	armed, id := entry.idleReason, entry.idleReportID
	e.sched.mu.Unlock()
	if armed != reportedSuccessReason || id != "report-1" {
		t.Fatalf("reports finalized while provisioning: outcome %q, ID %q", armed, id)
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

// Stalled reporters settle after the fallback deadline, but an input request
// always leaves the outcome pending for the owner's answer.
func TestStalledInteractiveOutcomeParksAtTheDeadline(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, func(cfg *Config) { cfg.StallThreshold = time.Millisecond })
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatalf("FinishReported: %v", err)
	}
	sub := e.subscribe(t)
	if err := e.sched.ReportAgentState(ctx, run.ID, permissionInput("open")); err != nil {
		t.Fatalf("permission wait: %v", err)
	}
	waitFor(t, "the run to stall", func() bool {
		e.sched.checkStalls(ctx)
		r, err := e.db.GetRun(ctx, run.ID)
		return err == nil && r.Status == domain.RunNeedsAttention
	})
	expectOnlyStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	e.expireReportDeadline(t, run.ID)
	expectNoStatusEvent(t, sub, run.ID, "an overdue arm parked at a permission prompt")
	e.sched.mu.Lock()
	finishing := e.sched.runs[run.ID].reportFinishing
	e.sched.mu.Unlock()
	if finishing {
		t.Fatal("the deadline started finishing a run parked at a permission prompt")
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, permissionInput("close")); err != nil {
		t.Fatalf("permission answered: %v", err)
	}
	expectNoStatusEvent(t, sub, run.ID, "closing the input on a stalled, armed run")
	e.expireReportDeadline(t, run.ID)
	if row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); row.Reason != reportedSuccessReason {
		t.Fatalf("park reason = %q, want %q", row.Reason, reportedSuccessReason)
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
	reason := e.sched.runs[run.ID].idleReason
	e.sched.mu.Unlock()
	if reason != "blocked: need the prod key" {
		t.Fatalf("blocked reason = %q, want the newer report's", reason)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.IdleReportAt == nil || !sc.IdleReportAt.Equal(newer) {
		t.Fatalf("sidecar = %+v, %v; want the newer report's finalized time", sc, err)
	}
}

func TestClearedInteractiveOutcomeStaysClearedAfterRecovery(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	at := time.Now().UTC()
	if err := e.sched.FinishReported(ctx, run.ID, "done-before-reboot", domain.RunCompleted, at); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	waitFor(t, "recovered follow-up", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[run.ID] != nil
	})
	if err := s2.FinishReported(ctx, run.ID, "done-before-reboot", domain.RunCompleted, at); err != nil {
		t.Fatal(err)
	}
	if err := s2.FinishReported(ctx, run.ID, "older-outbox", domain.RunFailed, at.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s2.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if row.Reason != agentstatus.ReasonIdle || row.OutcomeUnseen || row.FinishedAt != nil {
		t.Fatalf("recovery resurrected outcome: %+v", row)
	}
	if sc, err := s2.readSidecar(run.ID); err != nil || sc.IdleReason != "" || sc.IdleReportID != "done-before-reboot" || sc.IdleReportAt == nil || !sc.IdleReportAt.Equal(at) {
		t.Fatalf("cleared outcome watermark = %+v, %v", sc, err)
	}
}

func TestNewerBlockedReportReplacesPendingInteractiveOutcome(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	at := time.Now().UTC()
	if err := e.sched.FinishReported(ctx, run.ID, "success-old", domain.RunCompleted, at); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportBlocked(ctx, run.ID, "blocked-new", "need credentials", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.FinishReported(ctx, run.ID, "success-old", domain.RunCompleted, at); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if row.Reason != "blocked: need credentials" || row.OutcomeUnseen {
		t.Fatalf("newest report did not decide park: %+v", row)
	}
}

func TestInteractiveOutcomeParkRetriesFailedRowWrite(t *testing.T) {
	t.Parallel()
	for _, parkedBefore := range []bool{false, true} {
		t.Run(map[bool]string{false: "turn-end", true: "late-handoff"}[parkedBefore], func(t *testing.T) {
			t.Parallel()
			failing := &failingReportedFinishStore{}
			e := newReportingEnv(t, func(cfg *Config) {
				failing.Store = cfg.Store
				cfg.Store = failing
			})
			run, _ := e.launchReporting(t)
			e.waitStoreStatus(t, run.ID, domain.RunRunning)
			if parkedBefore {
				if err := e.sched.ReportAgentState(t.Context(), run.ID, waitingForInput); err != nil {
					t.Fatal(err)
				}
			}
			failing.fail.Store(1)
			err := e.sched.FinishReported(t.Context(), run.ID, "retry-park", domain.RunCompleted, time.Now())
			if !parkedBefore {
				if err != nil {
					t.Fatal(err)
				}
				err = e.sched.ReportAgentState(t.Context(), run.ID, waitingForInput)
			}
			if err == nil {
				t.Fatal("expected the outcome row write to fail")
			}
			sc, err := e.sched.readSidecar(run.ID)
			if err != nil || sc.IdleShown || sc.IdleReason != reportedSuccessReason || !turnEnd(sc.agentReport()) {
				t.Fatalf("retry boundary not durable: %+v, %v", sc, err)
			}
			e.sched.finishOverdueReports()
			row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
			if row.Reason != reportedSuccessReason || !row.OutcomeUnseen || row.FinishedAt != nil {
				t.Fatalf("retried park = %+v", row)
			}
		})
	}
}

func TestNativeActivityClearsReportedOutcomeWithoutRestart(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = time.Hour
		cfg.turnTail = time.Millisecond
	})
	run, container := e.launchFake(t, "native follow-up")
	at := time.Now().UTC().Add(-reportFinishDeadline)
	if err := e.sched.FinishReported(t.Context(), run.ID, "native-done", domain.RunCompleted, at); err != nil {
		t.Fatal(err)
	}
	e.sched.finishOverdueReports()
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "continue in this terminal"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.pty.injected(); len(got) != 1 {
		t.Fatalf("follow-up terminal delivery = %+v", got)
	}
	waitFor(t, "native follow-up activity", func() bool {
		container.output("working on the follow-up\r\n")
		e.sched.checkStalls(t.Context())
		row, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && row.Status == domain.RunRunning
	})
	if err := e.sched.FinishReported(t.Context(), run.ID, "native-done", domain.RunCompleted, at); err != nil {
		t.Fatal(err)
	}
	e.sched.finishOverdueReports()
	row := e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if row.OutcomeUnseen || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("native follow-up lost its live execution: %+v", row)
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.IdleReason != "" || sc.IdleReportID != "native-done" {
		t.Fatalf("native activity did not clear visible outcome: %+v, %v", sc, err)
	}
}

func TestSeenInteractiveOutcomeStaysSeenAfterRecovery(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	at := time.Now().UTC()
	if err := e.sched.FinishReported(t.Context(), run.ID, "reviewed-outcome", domain.RunCompleted, at); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.Seen(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	waitFor(t, "reviewed outcome recovered", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[run.ID] != nil
	})
	if err := s2.FinishReported(t.Context(), run.ID, "reviewed-outcome", domain.RunCompleted, at); err != nil {
		t.Fatal(err)
	}
	if err := s2.ReportAgentState(t.Context(), run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	s2.finishOverdueReports()
	row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if row.Reason != reportedSuccessReason || row.OutcomeUnseen || row.FinishUnopened || row.FinishedAt != nil {
		t.Fatalf("restart changed reviewed outcome: %+v", row)
	}
}

func TestNoReporterDeadlineRespectsInputAndRunMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []domain.LaunchMode{domain.LaunchTUI, domain.LaunchHeadless} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = -time.Second })
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "deadline with input", "fake", mode)
			if err != nil {
				t.Fatal(err)
			}
			e.waitStoreStatus(t, run.ID, domain.RunRunning)
			if err := e.sched.FinishReported(t.Context(), run.ID, "deadline-report", domain.RunCompleted, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := e.sched.ReportAgentState(t.Context(), run.ID, permissionInput("open")); err != nil {
				t.Fatal(err)
			}
			e.expireReportDeadline(t, run.ID)
			row := e.waitStoreStatus(t, run.ID, domain.RunRunning)
			if row.OutcomeUnseen || len(e.sched.PendingInputs(run.ID)) != 1 {
				t.Fatalf("deadline consumed outstanding input: %+v", row)
			}
			if err := e.sched.ReportAgentState(t.Context(), run.ID, permissionInput("close")); err != nil {
				t.Fatal(err)
			}
			e.expireReportDeadline(t, run.ID)
			want := domain.RunCompleted
			if mode.Interactive() {
				want = domain.RunNeedsAttention
			}
			row = e.waitStoreStatus(t, run.ID, want)
			if row.Reason != reportedSuccessReason || !row.OutcomeUnseen {
				t.Fatalf("settled outcome = %+v", row)
			}
			if mode.Interactive() {
				if row.FinishedAt != nil || e.rt.byName(string(run.ID)) == nil {
					t.Fatalf("negative TTL closed a live interactive outcome: %+v", row)
				}
			} else {
				if row.FinishedAt == nil {
					t.Fatal("background report did not finish")
				}
				waitFor(t, "background container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
			}
		})
	}
}
