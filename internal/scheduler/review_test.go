package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// nextReview is the next run.review event for run.
func nextReview(t *testing.T, sub events.Subscription, run domain.RunID) events.RunReviewPayload {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev := <-sub.Events():
			if p, ok := ev.Payload.(events.RunReviewPayload); ok && ev.RunID == run {
				return p
			}
		case <-deadline:
			t.Fatalf("timed out waiting for run.review on %s", run)
		}
	}
}

// TestReviewFollowsTheReportedSuccess: a success report asks the owner to
// review it; later puts it off and survives a restart; a new success report
// asks again; complete closes the live run as completed and records it.
func TestReviewFollowsTheReportedSuccess(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, func(cfg *Config) { cfg.StallThreshold = time.Hour })
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	sub := e.subscribe(t)

	if err := e.sched.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
		t.Fatal(err)
	}
	parked := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if parked.Activity != domain.ActivityWaiting || parked.Reason != turnEndedReason || parked.Review != "" || parked.NeedsAttention(false) {
		t.Fatalf("turn end without a report = %s %q review %q, want waiting, no review and no need", parked.Activity, parked.Reason, parked.Review)
	}
	if _, err := e.sched.Review(ctx, run.ID, e.member.ID, protocol.RunReviewLater); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("later with no reported success = %v, want ErrInvalidTransition", err)
	}

	reportedAt := time.Now().UTC()
	reportSuccess := func(s *Scheduler, id string) *domain.Run {
		t.Helper()
		if err := s.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
			t.Fatal(err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunRunning)
		reportedAt = time.Now().UTC()
		if err := s.FinishReported(ctx, run.ID, id, domain.RunCompleted, reportedAt); err != nil {
			t.Fatal(err)
		}
		if err := s.ReportAgentState(ctx, run.ID, waitingForInput); err != nil {
			t.Fatal(err)
		}
		var p events.RunStatusPayload
		for p.Activity != domain.ActivityFinished {
			p = waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention).Payload.(events.RunStatusPayload)
		}
		row := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
		if p.Review != domain.ReviewReady || p.ReviewChangedAt == nil || row.Review != domain.ReviewReady ||
			row.Activity != domain.ActivityFinished || !row.NeedsAttention(false) {
			t.Fatalf("success park = event %+v, row %s review %q, want finished and ready for review", p, row.Activity, row.Review)
		}
		return row
	}
	first := reportSuccess(e.sched, "report-1")

	later, err := e.sched.Review(ctx, run.ID, e.member.ID, protocol.RunReviewLater)
	if err != nil {
		t.Fatal(err)
	}
	if p := nextReview(t, sub, run.ID); p.Review != domain.ReviewLater || p.StatusSeq <= first.StatusSeq {
		t.Fatalf("later event = %+v, want later above status_seq %d", p, first.StatusSeq)
	}
	if later.Review != domain.ReviewLater || later.Status != domain.RunNeedsAttention || later.NeedsAttention(false) {
		t.Fatalf("after later = %s review %q, want still parked and not needing anyone", later.Status, later.Review)
	}

	// A restart re-derives the activity and keeps the review; the outbox
	// replaying the same report does not ask again.
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	waitFor(t, "run recovered", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[run.ID] != nil
	})
	if err = s2.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, reportedAt); err != nil {
		t.Fatal(err)
	}
	s2.refreshActivities(ctx)
	recovered, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Activity != domain.ActivityFinished || recovered.Review != domain.ReviewLater || recovered.NeedsAttention(false) {
		t.Fatalf("recovered run = %s review %q, want finished and still later", recovered.Activity, recovered.Review)
	}

	second := reportSuccess(s2, "report-2")
	if second.NeedKey() == first.NeedKey() {
		t.Fatal("a new success report kept the first one's need key, so it would not be announced")
	}

	completed, err := s2.Review(ctx, run.ID, e.member.ID, protocol.RunReviewComplete)
	if err != nil {
		t.Fatal(err)
	}
	if p := waitStatusEvent(t, sub, run.ID, domain.RunCompleted).Payload.(events.RunStatusPayload); p.FinishUnopened {
		t.Fatalf("complete event = %+v, want the member's close to open the finish", p)
	}
	if p := nextReview(t, sub, run.ID); p.Review != domain.ReviewAccepted {
		t.Fatalf("complete review event = %+v, want accepted", p)
	}
	if completed.Status != domain.RunCompleted || completed.Review != domain.ReviewAccepted || completed.NeedsAttention(false) {
		t.Fatalf("completed run = %s review %q, want completed and accepted", completed.Status, completed.Review)
	}
	if sc, err := s2.readSidecar(run.ID); err != nil || !sc.Retained || !retainedReason(completed.Status, completed.Reason) {
		t.Fatalf("completed run retention = %+v, %v, reason %q; want the container retained as run.close retains it", sc, err, completed.Reason)
	}
	if again, err := s2.Review(ctx, run.ID, e.member.ID, protocol.RunReviewComplete); err != nil || again.Review != domain.ReviewAccepted {
		t.Fatalf("second complete = %+v, %v; want accepted again", again, err)
	}
	if _, err := s2.Review(ctx, run.ID, e.member.ID, protocol.RunReviewLater); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("later on an accepted run = %v, want ErrInvalidTransition", err)
	}
}

// TestReviewOfABackgroundSuccess: a Background run its success finished asks
// for review; complete records the acceptance and leaves it completed.
func TestReviewOfABackgroundSuccess(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "write docs", "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if err = e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.rt.byName(string(run.ID)).exitNow(0)
	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Review != domain.ReviewReady || !row.NeedsAttention(false) {
		t.Fatalf("background success = review %q, want ready and needing its owner", row.Review)
	}
	accepted, err := e.sched.Review(ctx, run.ID, e.member.ID, protocol.RunReviewComplete)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != domain.RunCompleted || accepted.Review != domain.ReviewAccepted || accepted.NeedsAttention(false) {
		t.Fatalf("accepted background run = %s review %q", accepted.Status, accepted.Review)
	}

	failed, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "write more docs", "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, failed.ID, domain.RunRunning)
	e.rt.byName(string(failed.ID)).exitNow(1)
	if row := e.waitStoreStatus(t, failed.ID, domain.RunFailed); row.Review != "" {
		t.Fatalf("exit without a success report = review %q, want none", row.Review)
	}
	if _, err := e.sched.Review(ctx, failed.ID, e.member.ID, protocol.RunReviewComplete); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("complete on a failed run = %v, want ErrInvalidTransition", err)
	}
}

// TestFailedRelaunchKeepsTheReview:a relaunch that fails puts the retained
// row back with the review it had, so a success still awaits its owner.
func TestFailedRelaunchKeepsTheReview(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run, _ := e.launchFake(t, "relaunch a reviewed success")
	e.retainLegacyReportedRun(t, run.ID)
	ready, err := e.db.GetRun(ctx, run.ID)
	if err != nil || ready.Review != domain.ReviewReady || ready.ReviewChangedAt == nil {
		t.Fatalf("retained success = %+v, %v; want review ready", ready, err)
	}
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	resumeErr := errors.New("runtime resume unavailable")
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = &resumeFailureRuntime{Runtime: e.rt, resumeErr: resumeErr}
	t.Cleanup(func() { _ = s2.Close() })
	if err = s2.recoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, resumeErr) {
		t.Fatalf("Relaunch error = %v, want resume error", err)
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != domain.RunCompleted || row.Review != domain.ReviewReady || row.ReviewChangedAt == nil ||
		!row.ReviewChangedAt.Equal(*ready.ReviewChangedAt) || row.NeedKey() != ready.NeedKey() {
		t.Fatalf("row after the failed relaunch = %s review %q at %v, want completed and ready at %v", row.Status, row.Review, row.ReviewChangedAt, ready.ReviewChangedAt)
	}
}
