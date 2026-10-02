package coord

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type outcomeCall struct {
	run    domain.RunID
	status domain.RunStatus
	reason string
}

// recordingOutcomes is the scheduler side of OutcomeSink.
type recordingOutcomes struct {
	mu    sync.Mutex
	err   error
	calls []outcomeCall
}

func (o *recordingOutcomes) FinishReported(_ context.Context, run domain.RunID, outcome domain.RunStatus) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.calls = append(o.calls, outcomeCall{run: run, status: outcome})
	return nil
}

func (o *recordingOutcomes) ReportBlocked(_ context.Context, run domain.RunID, summary string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.calls = append(o.calls, outcomeCall{run: run, reason: summary})
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

	report(t, h, run, protocol.CoordOutcomeBlocked, "need\nthe staging key", "blocked-1")
	report(t, h, run, protocol.CoordOutcomeSuccess, "done", "success-1")
	report(t, h, run, protocol.CoordOutcomeSuccess, "done", "success-1")

	want := []outcomeCall{
		{run: run, reason: "need the staging key"},
		{run: run, status: domain.RunCompleted},
	}
	if got := outcomes.recorded(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("scheduler calls = %+v, want %+v", got, want)
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
