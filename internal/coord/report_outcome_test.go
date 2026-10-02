package coord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type outcomeCall struct {
	run      domain.RunID
	status   domain.RunStatus
	reason   string
	reportID string
}

// recordingOutcomes is the scheduler side of OutcomeSink.
type recordingOutcomes struct {
	mu         sync.Mutex
	err        error
	calls      []outcomeCall
	reportedAt []time.Time
}

func (o *recordingOutcomes) FinishReported(_ context.Context, run domain.RunID, outcome domain.RunStatus, reportedAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.reportedAt = append(o.reportedAt, reportedAt)
	o.calls = append(o.calls, outcomeCall{run: run, status: outcome})
	return nil
}

func (o *recordingOutcomes) ReportBlocked(_ context.Context, run domain.RunID, reportID, summary string, reportedAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.reportedAt = append(o.reportedAt, reportedAt)
	o.calls = append(o.calls, outcomeCall{run: run, reason: summary, reportID: reportID})
	return nil
}

func (o *recordingOutcomes) fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.err = err
}

func (o *recordingOutcomes) recorded() []outcomeCall {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]outcomeCall(nil), o.calls...)
}

func report(t *testing.T, h *coordHarness, run domain.RunID, outcome, summary, key string) protocol.CoordReportResult {
	t.Helper()
	result, rpcErr := h.svc.CoordReport(context.Background(), run, protocol.CoordReportParams{
		Outcome: outcome, Summary: summary, IdempotencyKey: key,
	})
	if rpcErr != nil {
		t.Fatalf("CoordReport %s: %v", outcome, rpcErr)
	}
	return result
}

// TestCoordReportDrivesAnOrdinaryRun: a blocked report reaches the
// scheduler as a reason, a success as a finish request, and a replay of a
// published report reaches it no more.
func TestCoordReportDrivesAnOrdinaryRun(t *testing.T) {
	outcomes := &recordingOutcomes{}
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_outcome"}
		c.Outcomes = outcomes
	})
	run := h.run(0)

	blocked := report(t, h, run, protocol.CoordOutcomeBlocked, "need\nthe staging key", "blocked-1")
	report(t, h, run, protocol.CoordOutcomeSuccess, "done", "success-1")
	report(t, h, run, protocol.CoordOutcomeSuccess, "done", "success-1")

	want := []outcomeCall{
		{run: run, reason: "need the staging key", reportID: blocked.ReportID},
		{run: run, status: domain.RunCompleted},
	}
	if got := outcomes.recorded(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("scheduler calls = %+v, want %+v", got, want)
	}
	// Each call carries its report's finalization time, which the
	// scheduler compares with the run's current launch.
	for i, key := range []string{"blocked-1", "success-1"} {
		stored, err := h.db.GetCoordReportByIdempotency(context.Background(), run, key)
		if err != nil || stored.FinalizedAt == nil || !outcomes.reportedAt[i].Equal(*stored.FinalizedAt) {
			t.Fatalf("call %d reportedAt = %v, want report %q finalized_at (%+v, %v)", i, outcomes.reportedAt[i], key, stored, err)
		}
	}
}

// TestCoordReportOutboxRetriesTheFinish: a scheduler that cannot arm the
// run yet leaves the publication pending, and the outbox arms it later.
func TestCoordReportOutboxRetriesTheFinish(t *testing.T) {
	ctx := context.Background()
	outcomes := &recordingOutcomes{}
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_retry"}
		c.Outcomes = outcomes
	})
	run := h.run(0)
	outcomes.fail(errors.New("scheduler: the run has no live container yet"))

	result := report(t, h, run, protocol.CoordOutcomeFailure, "cannot build", "failure-1")
	pub, err := h.db.GetCoordReportPublication(ctx, result.ReportID)
	if err != nil || pub.State != store.CoordReportPublicationPending {
		t.Fatalf("publication after failed hand-off = %+v, %v; want pending", pub, err)
	}

	outcomes.fail(nil)
	if _, _, drainErr := h.svc.drainOutboxPage(ctx); drainErr != nil {
		t.Fatalf("drain: %v", drainErr)
	}
	if got := outcomes.recorded(); len(got) != 1 || got[0] != (outcomeCall{run: run, status: domain.RunFailed}) {
		t.Fatalf("scheduler calls after retry = %+v, want one failed finish", got)
	}
	pub, err = h.db.GetCoordReportPublication(ctx, result.ReportID)
	if err != nil || pub.State != store.CoordReportPublicationPublished {
		t.Fatalf("publication after retry = %+v, %v; want published", pub, err)
	}
}

// TestCoordReportSkipsMissionAndSupersededReports: mission runs keep their
// own lifecycle, and a report a relaunch superseded before it was published
// must not finish the reopened run.
func TestCoordReportSkipsMissionAndSupersededReports(t *testing.T) {
	ctx := context.Background()
	outcomes := &recordingOutcomes{}
	h := newHarness(t, 2, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_skip"}
		c.Outcomes = outcomes
	})
	worker, ordinary := h.run(0), h.run(1)
	h.svc.cfg.Mission = missionTransportStub{mission: []domain.RunID{worker}}

	report(t, h, worker, protocol.CoordOutcomeSuccess, "submitted", "worker-success")
	if got := outcomes.recorded(); len(got) != 0 {
		t.Fatalf("scheduler calls for a mission worker = %+v, want none", got)
	}

	outcomes.fail(errors.New("not yet"))
	result := report(t, h, ordinary, protocol.CoordOutcomeSuccess, "done", "ordinary-success")
	if err := h.db.SupersedeCoordTerminalReport(ctx, ordinary); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	outcomes.fail(nil)
	if _, _, err := h.svc.drainOutboxPage(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got := outcomes.recorded(); len(got) != 0 {
		t.Fatalf("scheduler calls for a superseded report = %+v, want none", got)
	}
	pub, err := h.db.GetCoordReportPublication(ctx, result.ReportID)
	if err != nil || pub.State != store.CoordReportPublicationPublished {
		t.Fatalf("superseded publication = %+v, %v; want published", pub, err)
	}
}

// TestCoordReportRefusesASupersededKey: after a relaunch superseded the
// run's success, the reopened agent reusing that key is told to pick a new
// one instead of being handed the old result, and a new key reports again.
func TestCoordReportRefusesASupersededKey(t *testing.T) {
	ctx := context.Background()
	outcomes := &recordingOutcomes{}
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_superseded"}
		c.Outcomes = outcomes
	})
	run := h.run(0)
	report(t, h, run, protocol.CoordOutcomeSuccess, "done", "success-1")
	if err := h.db.SupersedeCoordTerminalReport(ctx, run); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	_, rpcErr := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "done", IdempotencyKey: "success-1",
	})
	if rpcErr == nil || rpcErr.Code != protocol.CodeConflict ||
		!strings.Contains(rpcErr.Message, "superseded") || !strings.Contains(rpcErr.Message, "new idempotency key") {
		t.Fatalf("reused superseded key = %+v, want a conflict naming the relaunch and a new key", rpcErr)
	}
	report(t, h, run, protocol.CoordOutcomeSuccess, "done again", "success-2")
	if got := outcomes.recorded(); len(got) != 2 || got[1] != (outcomeCall{run: run, status: domain.RunCompleted}) {
		t.Fatalf("scheduler calls = %+v, want a second finish for the new key", got)
	}
}
