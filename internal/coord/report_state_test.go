package coord

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// newStateReportHarness serves coord.report with a persisted event log, so
// timeline entries can be read back and their retries reconcile by ID.
func newStateReportHarness(t *testing.T, outcomes OutcomeSink) (*coordHarness, events.EventLog) {
	t.Helper()
	h := newHarness(t, 1, func(c *Config) {
		c.Evidence = &coordReportEvidenceCapture{id: "ev_state"}
		c.Outcomes = outcomes
	})
	log, err := events.OpenSQLiteLog(filepath.Join(h.dir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(t.Context(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	h.svc.cfg.Bus = bus
	return h, log
}

func timelineReports(t *testing.T, log events.EventLog, run domain.RunID) []events.TimelinePayload {
	t.Helper()
	stored, err := log.Read(t.Context(), events.Filter{Run: run, Types: []events.Type{events.TypeTimeline}}, 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []events.TimelinePayload
	for _, e := range stored {
		if p, ok := e.Payload.(events.TimelinePayload); ok && p.Kind == events.TimelineReport {
			out = append(out, p)
		}
	}
	return out
}

func stateReport(h *coordHarness, run domain.RunID, outcome, waitingFor, summary, key string) *protocol.Error {
	_, rpcErr := h.svc.CoordReport(context.Background(), run, protocol.CoordReportParams{
		Outcome: outcome, WaitingFor: waitingFor, Summary: summary, IdempotencyKey: key,
	})
	return rpcErr
}

// Every accepted waiting or needs-input report is a timeline entry, once per
// report however often it is retried.
func TestStateReportsAreTimelineEntries(t *testing.T) {
	outcomes := &recordingOutcomes{}
	h, log := newStateReportHarness(t, outcomes)
	run := h.run(0)

	for range 2 {
		if rpcErr := stateReport(h, run, protocol.CoordOutcomeWaiting, protocol.CoordWaitingForCI, "CI on PR 12", "wait-1"); rpcErr != nil {
			t.Fatalf("waiting: %v", rpcErr)
		}
	}
	if rpcErr := stateReport(h, run, protocol.CoordOutcomeNeedsInput, "", "which region?", "ask-1"); rpcErr != nil {
		t.Fatalf("needs-input: %v", rpcErr)
	}
	want := []events.TimelinePayload{
		{Kind: events.TimelineReport, ReportID: "waiting:" + string(run) + ":wait-1", Outcome: protocol.CoordOutcomeWaiting,
			WaitingFor: protocol.CoordWaitingForCI, Summary: "CI on PR 12", NextAction: "end your turn; the next prompt or mail resumes you"},
		{Kind: events.TimelineReport, ReportID: "needs-input:" + string(run) + ":ask-1", Outcome: protocol.CoordOutcomeNeedsInput,
			Summary: "which region?", NextAction: "answer the agent's question"},
	}
	got := timelineReports(t, log, run)
	if len(got) != len(want) {
		t.Fatalf("timeline reports = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].ReportID != want[i].ReportID || got[i].Outcome != want[i].Outcome ||
			got[i].WaitingFor != want[i].WaitingFor || got[i].Summary != want[i].Summary || got[i].NextAction != want[i].NextAction {
			t.Fatalf("timeline report %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if calls := outcomes.recorded(); len(calls) != 3 || calls[0] != calls[1] {
		t.Fatalf("scheduler calls = %+v, want the waiting report handed off twice, then the question", calls)
	}
	if outcomes.reportedAt[0] != outcomes.reportedAt[1] {
		t.Fatalf("retry reported at %v, first at %v: want the receipt's time both times", outcomes.reportedAt[1], outcomes.reportedAt[0])
	}
}

// A key belongs to the first report that used it: another outcome, target or
// summary under it is refused rather than accepted and dropped, whichever
// path stored the key.
func TestStateReportRefusesAReusedKey(t *testing.T) {
	outcomes := &recordingOutcomes{}
	h, log := newStateReportHarness(t, outcomes)
	run := h.run(0)

	if rpcErr := stateReport(h, run, protocol.CoordOutcomeWaiting, "", "on 3 workers", "wait"); rpcErr != nil {
		t.Fatalf("waiting: %v", rpcErr)
	}
	report(t, h, run, protocol.CoordOutcomeBlocked, "need the staging key", "blocked-1")
	for name, params := range map[string]protocol.CoordReportParams{
		"new summary":         {Outcome: protocol.CoordOutcomeWaiting, Summary: "on 2 workers", IdempotencyKey: "wait"},
		"new waiting_for":     {Outcome: protocol.CoordOutcomeWaiting, WaitingFor: protocol.CoordWaitingForCI, Summary: "on 3 workers", IdempotencyKey: "wait"},
		"needs-input":         {Outcome: protocol.CoordOutcomeNeedsInput, Summary: "on 3 workers", IdempotencyKey: "wait"},
		"success":             {Outcome: protocol.CoordOutcomeSuccess, Summary: "on 3 workers", IdempotencyKey: "wait"},
		"waiting on blocked":  {Outcome: protocol.CoordOutcomeWaiting, Summary: "need the staging key", IdempotencyKey: "blocked-1"},
		"question on blocked": {Outcome: protocol.CoordOutcomeNeedsInput, Summary: "need the staging key", IdempotencyKey: "blocked-1"},
	} {
		if _, rpcErr := h.svc.CoordReport(t.Context(), run, params); rpcErr == nil || rpcErr.Code != protocol.CodeConflict {
			t.Fatalf("%s: err = %+v, want a conflict", name, rpcErr)
		}
	}
	if calls := outcomes.recorded(); len(calls) != 2 {
		t.Fatalf("scheduler calls = %+v, want only the waiting and blocked reports", calls)
	}
	if got := timelineReports(t, log, run); len(got) != 2 {
		t.Fatalf("timeline reports = %+v, want only the waiting and blocked reports", got)
	}
}

// A hand-off the scheduler refuses is an error to the agent and no timeline
// entry; a retry after a transient failure takes effect.
func TestStateReportThatDoesNotTakeEffectIsAnError(t *testing.T) {
	outcomes := &recordingOutcomes{}
	h, log := newStateReportHarness(t, outcomes)
	run := h.run(0)

	for _, tc := range []struct {
		err  error
		code int
	}{
		{ErrReportSuperseded, protocol.CodeConflict},
		{ErrRunFinished, protocol.CodeInvalidParams},
		{errors.New("the run is provisioning and has no live container yet"), protocol.CodeInternal},
	} {
		outcomes.fail(tc.err)
		if rpcErr := stateReport(h, run, protocol.CoordOutcomeWaiting, "", "on 3 workers", "wait"); rpcErr == nil || rpcErr.Code != tc.code {
			t.Fatalf("%v: err = %+v, want code %d", tc.err, rpcErr, tc.code)
		}
	}
	if got := timelineReports(t, log, run); len(got) != 0 {
		t.Fatalf("timeline reports = %+v, want none for refused hand-offs", got)
	}
	outcomes.fail(nil)
	if rpcErr := stateReport(h, run, protocol.CoordOutcomeWaiting, "", "on 3 workers", "wait"); rpcErr != nil {
		t.Fatalf("retry: %v", rpcErr)
	}
	if got := timelineReports(t, log, run); len(got) != 1 {
		t.Fatalf("timeline reports = %+v, want the retried report", got)
	}
}

// A report refused outright does not keep its key: the agent may follow the
// refusal's advice under the same key.
func TestRefusedStateReportReleasesItsKey(t *testing.T) {
	outcomes := &recordingOutcomes{}
	h, _ := newStateReportHarness(t, outcomes)
	run := h.run(0)

	outcomes.fail(ErrCannotAsk)
	if rpcErr := stateReport(h, run, protocol.CoordOutcomeNeedsInput, "", "which region?", "k"); rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams {
		t.Fatalf("needs-input = %+v, want invalid params", rpcErr)
	}
	outcomes.fail(nil)
	report(t, h, run, protocol.CoordOutcomeBlocked, "need the region", "k")
}
