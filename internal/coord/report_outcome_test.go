package coord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
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

func (o *recordingOutcomes) FinishReported(_ context.Context, run domain.RunID, reportID string, outcome domain.RunStatus, reportedAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.reportedAt = append(o.reportedAt, reportedAt)
	o.calls = append(o.calls, outcomeCall{run: run, status: outcome, reportID: reportID})
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
	success := report(t, h, run, protocol.CoordOutcomeSuccess, "done", "success-1")
	report(t, h, run, protocol.CoordOutcomeSuccess, "done", "success-1")

	want := []outcomeCall{
		{run: run, reason: "need the staging key", reportID: blocked.ReportID},
		{run: run, status: domain.RunCompleted, reportID: success.ReportID},
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
	if got := outcomes.recorded(); len(got) != 1 || got[0] != (outcomeCall{run: run, status: domain.RunFailed, reportID: result.ReportID}) {
		t.Fatalf("scheduler calls after retry = %+v, want one failed finish", got)
	}
	pub, err = h.db.GetCoordReportPublication(ctx, result.ReportID)
	if err != nil || pub.State != store.CoordReportPublicationPublished {
		t.Fatalf("publication after retry = %+v, %v; want published", pub, err)
	}
}

// TestCoordReportSkipsWorkerAndSupersededReports: a mission worker keeps its
// own lifecycle while a mission integrator finishes like an ordinary run, and
// a report a relaunch superseded before it was published must not finish the
// reopened run.
func TestCoordReportSkipsWorkerAndSupersededReports(t *testing.T) {
	ctx := context.Background()
	outcomes := &recordingOutcomes{}
	h := newHarness(t, 3, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_skip"}
		c.Outcomes = outcomes
	})
	worker, ordinary, integrator := h.run(0), h.run(1), h.run(2)
	h.svc.cfg.Mission = missionTransportStub{mission: []domain.RunID{worker, integrator}, integrator: integrator}

	report(t, h, worker, protocol.CoordOutcomeSuccess, "submitted", "worker-success")
	if got := outcomes.recorded(); len(got) != 0 {
		t.Fatalf("scheduler calls for a mission worker = %+v, want none", got)
	}
	done := report(t, h, integrator, protocol.CoordOutcomeSuccess, "delivered", "integrator-success")
	if got := outcomes.recorded(); len(got) != 1 || got[0] != (outcomeCall{run: integrator, status: domain.RunCompleted, reportID: done.ReportID}) {
		t.Fatalf("scheduler calls for a mission integrator = %+v, want one completed finish", got)
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
	if got := outcomes.recorded(); len(got) != 1 {
		t.Fatalf("scheduler calls after a superseded report = %+v, want only the integrator's", got)
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
	again := report(t, h, run, protocol.CoordOutcomeSuccess, "done again", "success-2")
	if got := outcomes.recorded(); len(got) != 2 || got[1] != (outcomeCall{run: run, status: domain.RunCompleted, reportID: again.ReportID}) {
		t.Fatalf("scheduler calls = %+v, want a second finish for the new key", got)
	}
}

// relaunchingCapture supersedes the run's terminal report while its
// evidence is captured, as a relaunch landing mid-capture does.
type relaunchingCapture struct {
	coordReportEvidenceCapture
	db *store.DB
}

func (c *relaunchingCapture) Capture(ctx context.Context, req evidence.Request) (protocol.EvidencePacket, error) {
	if err := c.db.SupersedeCoordTerminalReport(ctx, req.RunID); err != nil {
		return protocol.EvidencePacket{}, err
	}
	return c.coordReportEvidenceCapture.Capture(ctx, req)
}

// TestCoordReportRefusesAReservationSupersededDuringCapture: a relaunch
// that supersedes the reservation while its evidence is captured leaves
// nothing to finalize. The agent is told to report again under a new key,
// and the scheduler never sees the stale report.
func TestCoordReportRefusesAReservationSupersededDuringCapture(t *testing.T) {
	ctx := context.Background()
	outcomes := &recordingOutcomes{}
	capture := &relaunchingCapture{coordReportEvidenceCapture: coordReportEvidenceCapture{id: "ev_mid_capture"}}
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = capture
		c.Outcomes = outcomes
	})
	capture.db = h.db
	run := h.run(0)

	_, rpcErr := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "done", IdempotencyKey: "success-1",
	})
	if rpcErr == nil || rpcErr.Code != protocol.CodeConflict ||
		!strings.Contains(rpcErr.Message, "superseded") || !strings.Contains(rpcErr.Message, "new idempotency key") {
		t.Fatalf("report superseded mid-capture = %+v, want a conflict naming the relaunch and a new key", rpcErr)
	}
	stored, err := h.db.GetCoordReportByIdempotency(ctx, run, "success-1")
	if err != nil || stored.State != store.CoordReportPending || stored.FinalizedAt != nil {
		t.Fatalf("superseded reservation = %+v, %v; want it left pending", stored, err)
	}
	if _, _, err := h.svc.drainOutboxPage(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got := outcomes.recorded(); len(got) != 0 {
		t.Fatalf("scheduler calls = %+v, want none", got)
	}
}
