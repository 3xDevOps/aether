package coord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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

func (o *recordingOutcomes) ReportNeedsInput(_ context.Context, run domain.RunID, id, question string, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.reportedAt = append(o.reportedAt, at)
	o.calls = append(o.calls, outcomeCall{run: run, reason: "needs input: " + question, reportID: id})
	return nil
}

func (o *recordingOutcomes) ReportWaiting(_ context.Context, run domain.RunID, id, waitingFor, summary string, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.reportedAt = append(o.reportedAt, at)
	o.calls = append(o.calls, outcomeCall{run: run, reason: strings.TrimSpace("waiting: "+waitingFor) + " " + summary, reportID: id})
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

// Mission workers keep their own lifecycle, integrators use the ordinary
// outcome path, and a superseded report must not affect a reopened run.
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

func TestInteractiveRunReportsSuccessiveTasks(t *testing.T) {
	for _, mode := range []domain.LaunchMode{domain.LaunchTUI, domain.LaunchACP} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			outcomes := &recordingOutcomes{}
			capture := &coordReportEvidenceCapture{id: "ev_followup"}
			h := newHarness(t, 1, func(c *Config) {
				c.Evidence = capture
				c.Outcomes = outcomes
			})
			run := h.run(0)
			if err := h.db.SetRunMode(ctx, run, mode, mode == domain.LaunchACP); err != nil {
				t.Fatalf("SetRunMode: %v", err)
			}
			first := report(t, h, run, protocol.CoordOutcomeSuccess, "first task verified", "task-1")
			firstSnapshot, err := h.db.GetCoordReport(ctx, first.ReportID)
			if err != nil {
				t.Fatalf("first report: %v", err)
			}
			second := report(t, h, run, protocol.CoordOutcomeFailure, "second task cannot proceed", "task-2")
			replay := report(t, h, run, protocol.CoordOutcomeFailure, "second task cannot proceed", "task-2")
			if replay.ReportID != second.ReportID || capture.calls.Load() != 2 {
				t.Fatalf("replay = %+v after %d captures; want %s and 2", replay, capture.calls.Load(), second.ReportID)
			}
			if _, rpcErr := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
				Outcome: protocol.CoordOutcomeSuccess, Summary: "different", IdempotencyKey: "task-2",
			}); rpcErr == nil || rpcErr.Code != protocol.CodeConflict {
				t.Fatalf("changed-key payload = %v, want conflict", rpcErr)
			}
			if _, rpcErr := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
				Outcome: protocol.CoordOutcomeSuccess, Summary: "first task verified", IdempotencyKey: "task-1",
			}); rpcErr == nil || rpcErr.Code != protocol.CodeConflict {
				t.Fatalf("superseded replay = %v, want conflict", rpcErr)
			}
			if err := h.svc.applyRunOutcome(ctx, firstSnapshot); err != nil {
				t.Fatalf("stale loaded report delivery: %v", err)
			}
			blocked := report(t, h, run, protocol.CoordOutcomeBlocked, "need another decision", "task-3")
			last := report(t, h, run, protocol.CoordOutcomeSuccess, "follow-up verified", "task-4")
			want := []outcomeCall{
				{run: run, status: domain.RunCompleted, reportID: first.ReportID},
				{run: run, status: domain.RunFailed, reportID: second.ReportID},
				{run: run, reason: "need another decision", reportID: blocked.ReportID},
				{run: run, status: domain.RunCompleted, reportID: last.ReportID},
			}
			got := outcomes.recorded()
			if len(got) != len(want) {
				t.Fatalf("outcome calls = %+v, want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("outcome call %d = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestInteractiveReportOutboxSkipsOlderTaskOutcome(t *testing.T) {
	ctx := context.Background()
	outcomes := &recordingOutcomes{err: errors.New("scheduler not ready")}
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_older_task"}
		c.Outcomes = outcomes
	})
	run := h.run(0)
	first := report(t, h, run, protocol.CoordOutcomeSuccess, "first task done", "task-1")
	outcomes.fail(nil)
	second := report(t, h, run, protocol.CoordOutcomeFailure, "second task failed", "task-2")
	if _, _, err := h.svc.drainOutboxPage(ctx); err != nil {
		t.Fatalf("drain old report: %v", err)
	}
	if got := outcomes.recorded(); len(got) != 1 || got[0] != (outcomeCall{run: run, status: domain.RunFailed, reportID: second.ReportID}) {
		t.Fatalf("outcomes after delayed publication = %+v, want only the second task", got)
	}
	pub, err := h.db.GetCoordReportPublication(ctx, first.ReportID)
	if err != nil || pub.State != store.CoordReportPublicationPublished {
		t.Fatalf("older report publication = %+v, %v; want published without its outcome", pub, err)
	}
}

// A later report that rewrites a pinned outcome note publishes the unpin,
// attributed to the run's owner and the run.
func TestInteractiveReportUnpinsItsRewrittenOutcomeNote(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_outcome"}
		c.Outcomes = &recordingOutcomes{}
	})
	owner := h.runs[0]
	report(t, h, owner.ID, protocol.CoordOutcomeSuccess, "Fixed lease race", "task-1")
	notes, err := h.db.SearchMemory(ctx, store.MemoryQuery{Workspace: h.workspace, Kind: store.MemoryOutcome, Limit: 1})
	if err != nil || len(notes) != 1 {
		t.Fatalf("outcome notes = %v, %v", notes, err)
	}
	if _, err = h.db.PinMemory(ctx, h.workspace, notes[0].ID, owner.MemberID, ""); err != nil {
		t.Fatal(err)
	}
	sub, err := h.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeMemoryChange}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close() //nolint:errcheck // test cleanup
	report(t, h, owner.ID, protocol.CoordOutcomeSuccess, "Rewritten summary", "task-2")
	select {
	case ev := <-sub.Events():
		want := events.MemoryChangePayload{Action: events.MemoryActionUnpin, NoteID: notes[0].ID, ActorID: owner.MemberID}
		if ev.RunID != owner.ID || ev.ActorID != owner.MemberID || ev.Payload != want {
			t.Fatalf("memory.change = %+v, want %+v on run %s", ev, want, owner.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no memory.change event for the cleared pin")
	}
}

func TestInteractiveFollowupStillRequiresEvidence(t *testing.T) {
	ctx := context.Background()
	outcomes := &recordingOutcomes{}
	capture := &coordReportEvidenceCapture{id: "ev_first_task"}
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = capture
		c.Outcomes = outcomes
	})
	run := h.run(0)
	report(t, h, run, protocol.CoordOutcomeSuccess, "first task verified", "task-1")
	capture.id = ""
	params := protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "second task verified", IdempotencyKey: "task-2",
	}
	if _, rpcErr := h.svc.CoordReport(ctx, run, params); rpcErr == nil || rpcErr.Code != protocol.CodeInternal {
		t.Fatalf("missing follow-up evidence = %v, want internal error", rpcErr)
	}
	pending, err := h.db.GetCoordReportByIdempotency(ctx, run, "task-2")
	if err != nil || pending.State != store.CoordReportPending || pending.FinalizedAt != nil {
		t.Fatalf("follow-up report = %+v, %v; want pending evidence", pending, err)
	}
	if got := outcomes.recorded(); len(got) != 1 {
		t.Fatalf("outcomes before evidence accepted = %+v, want only first task", got)
	}
	capture.id = "ev_second_task"
	accepted := report(t, h, run, params.Outcome, params.Summary, params.IdempotencyKey)
	if accepted.ReportID != pending.ID || accepted.EvidenceRef != capture.id {
		t.Fatalf("follow-up retry = %+v, want pending report %s with new evidence", accepted, pending.ID)
	}
	if got := outcomes.recorded(); len(got) != 2 || got[1].reportID != accepted.ReportID {
		t.Fatalf("outcomes after evidence accepted = %+v, want both tasks", got)
	}
}

// A needs-input report is not durable: it reaches the scheduler directly with
// an id derived from the key, reserves nothing, and leaves the terminal slot
// free. Its question reaches the scheduler whole. A waiting report takes the
// same path, from any role.
func TestCoordReportNeedsInputAsksTheUser(t *testing.T) {
	ctx := context.Background()
	outcomes := &recordingOutcomes{}
	h := newHarness(t, 2, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_needs_input"}
		c.Outcomes = outcomes
	})
	run, worker := h.run(0), h.run(1)

	long := strings.Repeat("x", 400)
	result := report(t, h, run, protocol.CoordOutcomeNeedsInput, "which\nregion? "+long, "ask-1")
	report(t, h, run, protocol.CoordOutcomeNeedsInput, "which\nregion? "+long, "ask-1")
	if result.ReportID != "" || result.Outcome != protocol.CoordOutcomeNeedsInput || result.Summary != "which region? "+long {
		t.Fatalf("result = %+v, want an id-less needs-input echo of the question", result)
	}
	want := outcomeCall{run: run, reason: "needs input: which region? " + long, reportID: "needs-input:" + string(run) + ":ask-1"}
	if got := outcomes.recorded(); len(got) != 2 || got[0] != want || got[1] != want {
		t.Fatalf("scheduler calls = %+v, want the same id twice", got)
	}
	if _, err := h.db.GetCoordReportByIdempotency(ctx, run, "ask-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("durable report for needs-input = %v, want ErrNotFound", err)
	}
	report(t, h, run, protocol.CoordOutcomeSuccess, "done", "success-1")

	for name, params := range map[string]protocol.CoordReportParams{
		"evidence refs": {Outcome: protocol.CoordOutcomeNeedsInput, Summary: "q?", IdempotencyKey: "k", EvidenceRefs: []string{"ev"}},
		"no summary":    {Outcome: protocol.CoordOutcomeNeedsInput, IdempotencyKey: "k"},
		"no key":        {Outcome: protocol.CoordOutcomeNeedsInput, Summary: "q?"},
	} {
		if _, rpcErr := h.svc.CoordReport(ctx, run, params); rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams {
			t.Fatalf("%s: err = %+v, want invalid params", name, rpcErr)
		}
	}
	h.svc.cfg.Mission = missionTransportStub{mission: []domain.RunID{worker}}
	_, rpcErr := h.svc.CoordReport(ctx, worker, protocol.CoordReportParams{Outcome: protocol.CoordOutcomeNeedsInput, Summary: "q?", IdempotencyKey: "k"})
	if rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams || !strings.Contains(rpcErr.Message, "aether-internal ask") {
		t.Fatalf("worker needs-input = %+v, want invalid params naming ask", rpcErr)
	}
	if got := outcomes.recorded(); len(got) != 3 {
		t.Fatalf("scheduler calls = %+v, want only the accepted reports", got)
	}
	result = report(t, h, worker, protocol.CoordOutcomeWaiting, "waiting on\nthe build", "wait-1")
	if result.ReportID != "" || result.Outcome != protocol.CoordOutcomeWaiting {
		t.Fatalf("waiting result = %+v, want an id-less waiting echo", result)
	}
	want = outcomeCall{run: worker, reason: "waiting: waiting on the build", reportID: "waiting:" + string(worker) + ":wait-1"}
	if got := outcomes.recorded(); len(got) != 4 || got[3] != want {
		t.Fatalf("scheduler calls = %+v, want the worker's waiting report last", got)
	}
	if _, rpcErr := h.svc.CoordReport(ctx, run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeWaiting, WaitingFor: protocol.CoordWaitingForCI, Summary: "CI on PR 12", IdempotencyKey: "wait-ci",
	}); rpcErr != nil {
		t.Fatalf("waiting for ci: %v", rpcErr)
	}
	want = outcomeCall{run: run, reason: "waiting: ci CI on PR 12", reportID: "waiting:" + string(run) + ":wait-ci"}
	if got := outcomes.recorded(); len(got) != 5 || got[4] != want {
		t.Fatalf("scheduler calls = %+v, want the waiting for ci report last", got)
	}
	for name, params := range map[string]protocol.CoordReportParams{
		"unknown waiting_for":    {Outcome: protocol.CoordOutcomeWaiting, WaitingFor: "humans", Summary: "s", IdempotencyKey: "k"},
		"waiting_for on blocked": {Outcome: protocol.CoordOutcomeBlocked, WaitingFor: protocol.CoordWaitingForCI, Summary: "s", IdempotencyKey: "k"},
	} {
		if _, rpcErr := h.svc.CoordReport(ctx, run, params); rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams {
			t.Fatalf("%s: err = %+v, want invalid params", name, rpcErr)
		}
	}
}

func TestCoordReportNeedsInputFromAFinishedRun(t *testing.T) {
	outcomes := &recordingOutcomes{}
	outcomes.fail(ErrRunFinished)
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_finished"}
		c.Outcomes = outcomes
	})
	_, rpcErr := h.svc.CoordReport(context.Background(), h.run(0), protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeNeedsInput, Summary: "which region?", IdempotencyKey: "ask-1",
	})
	if rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams || !strings.Contains(rpcErr.Message, ErrRunFinished.Error()) {
		t.Fatalf("err = %+v, want invalid params carrying %q", rpcErr, ErrRunFinished)
	}
}

// A scheduler that cannot take the user's answer, a Background run, rejects
// the question with the reason the agent should read.
func TestCoordReportNeedsInputFromARunThatCannotAsk(t *testing.T) {
	outcomes := &recordingOutcomes{}
	outcomes.fail(ErrCannotAsk)
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_cannot_ask"}
		c.Outcomes = outcomes
	})
	_, rpcErr := h.svc.CoordReport(context.Background(), h.run(0), protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeNeedsInput, Summary: "which region?", IdempotencyKey: "ask-1",
	})
	if rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams || !strings.Contains(rpcErr.Message, ErrCannotAsk.Error()) {
		t.Fatalf("err = %+v, want invalid params carrying %q", rpcErr, ErrCannotAsk)
	}
}
