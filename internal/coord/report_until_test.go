package coord

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// recordingWaits is the watch service: it stores the receipt as the real
// one does in the same transaction as the conditions.
type recordingWaits struct {
	db      *store.DB
	refuse  string
	mu      sync.Mutex
	owners  []domain.RunID
	specs   [][]string
	receipt []string
	read    []domain.RunID
	ended   []string
	settled []protocol.WaitCondition
}

func (r *recordingWaits) RegisterWait(ctx context.Context, owner *domain.Run, report *store.CoordStateReport, specs []protocol.WaitSpec) (bool, error) {
	if r.refuse != "" {
		return false, &InvalidWait{Reason: r.refuse}
	}
	inserted, err := r.db.RecordCoordStateReport(ctx, report)
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	texts := make([]string, 0, len(specs))
	for _, s := range specs {
		texts = append(texts, s.Spec)
	}
	r.owners, r.specs, r.receipt = append(r.owners, owner.ID), append(r.specs, texts), report.Until
	return inserted, nil
}

func (r *recordingWaits) SettleRunWaits(_ context.Context, run domain.RunID) ([]protocol.WaitCondition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.read = append(r.read, run)
	return r.settled, nil
}

func (r *recordingWaits) EndRunWaits(_ context.Context, run domain.RunID, cause string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended = append(r.ended, string(run)+": "+cause)
	return nil
}

func TestUntilRegistersWaitsForTheReportingRunOnly(t *testing.T) {
	outcomes := &recordingOutcomes{}
	h, _ := newStateReportHarness(t, outcomes)
	waits := &recordingWaits{db: h.db}
	h.svc.cfg.Waits = waits
	run := h.run(0)

	for name, params := range map[string]protocol.CoordReportParams{
		"until on answered": {Outcome: protocol.CoordOutcomeAnswered, Summary: "s", IdempotencyKey: "a", Until: []string{"pr:acme/app#7:merged"}},
		"until on success":  {Outcome: protocol.CoordOutcomeSuccess, Summary: "s", IdempotencyKey: "b", Until: []string{"pr:acme/app#7:merged"}},
		"prose":             {Outcome: protocol.CoordOutcomeWaiting, Summary: "s", IdempotencyKey: "c", Until: []string{"when CI passes"}},
		"listed twice":      {Outcome: protocol.CoordOutcomeWaiting, Summary: "s", IdempotencyKey: "d", Until: []string{"pr:acme/app#7:merged", "pr:Acme/app#7:merged"}},
		"five":              {Outcome: protocol.CoordOutcomeWaiting, Summary: "s", IdempotencyKey: "e", Until: []string{"run:a:finished", "run:b:finished", "run:c:finished", "run:d:finished", "run:e:finished"}},
		"uppercase keyword": {Outcome: protocol.CoordOutcomeWaiting, Summary: "s", IdempotencyKey: "f", Until: []string{"pr:acme/app#7:MERGED"}},
		"missing pr number": {Outcome: protocol.CoordOutcomeWaiting, Summary: "s", IdempotencyKey: "g", Until: []string{"pr:acme/app:checks"}},
	} {
		_, rpcErr := h.svc.CoordReport(t.Context(), run, params)
		if rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams || !strings.Contains(rpcErr.Message, "until") {
			t.Fatalf("%s: err = %+v, want invalid params about until", name, rpcErr)
		}
	}
	if len(waits.owners) != 0 || len(outcomes.recorded()) != 0 {
		t.Fatalf("refused reports registered %v and handed off %v", waits.owners, outcomes.recorded())
	}

	until := []string{"pr:Acme/App#7:checks", "run:run-0000000001:finished"}
	res, rpcErr := h.svc.CoordReport(t.Context(), run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeWaiting, WaitingFor: protocol.CoordWaitingForCI, Summary: "CI on #7", IdempotencyKey: "w", Until: until,
	})
	if rpcErr != nil {
		t.Fatalf("waiting until: %v", rpcErr)
	}
	want := []string{"pr:acme/app#7:checks", "run:run-0000000001:finished"}
	if !slices.Equal(waits.owners, []domain.RunID{run}) || !slices.Equal(waits.specs[0], want) || !slices.Equal(waits.receipt, want) ||
		!slices.Equal(waits.read, []domain.RunID{run}) || !strings.Contains(res.NextAction, "pr:acme/app#7:checks or run:run-0000000001:finished") {
		t.Fatalf("registered %v %v receipt %v read %v next %q", waits.owners, waits.specs, waits.receipt, waits.read, res.NextAction)
	}
	if _, rpcErr = h.svc.CoordReport(t.Context(), run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeWaiting, WaitingFor: protocol.CoordWaitingForCI, Summary: "CI on #7", IdempotencyKey: "w", Until: until[:1],
	}); rpcErr == nil || rpcErr.Code != protocol.CodeConflict {
		t.Fatalf("same key with other conditions: %+v, want a conflict", rpcErr)
	}

	if rpcErr = stateReport(h, run, protocol.CoordOutcomeWaiting, "", "still on CI", "w2"); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if len(waits.ended) != 0 {
		t.Fatalf("waiting without until ended %v, want the conditions kept", waits.ended)
	}
	if rpcErr = stateReport(h, run, protocol.CoordOutcomeAnswered, "", "replied", "a2"); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	report(t, h, run, protocol.CoordOutcomeBlocked, "need the staging key", "b2")
	if !slices.Equal(waits.ended, []string{string(run) + ": the run reported answered", string(run) + ": the run reported blocked"}) {
		t.Fatalf("ended = %v, want answered then blocked to cancel", waits.ended)
	}

	waits.settled = []protocol.WaitCondition{{Spec: "run:run-0000000001:finished", State: protocol.WaitResolved, Outcome: "failed"}}
	res, rpcErr = h.svc.CoordReport(t.Context(), run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeWaiting, Summary: "s", IdempotencyKey: "done", Until: []string{"run:run-0000000001:finished"},
	})
	if rpcErr != nil || len(res.Resolved) != 1 || !strings.Contains(res.NextAction, "do not end your turn waiting: run:run-0000000001:finished is already resolved (failed)") {
		t.Fatalf("already resolved: %+v, %v; want it returned with what to do", res, rpcErr)
	}

	waits.refuse = "until run:run-x:finished: no such run"
	_, rpcErr = h.svc.CoordReport(t.Context(), run, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeWaiting, Summary: "s", IdempotencyKey: "r", Until: []string{"run:run-x:finished"},
	})
	if rpcErr == nil || rpcErr.Code != protocol.CodeInvalidParams || !strings.HasSuffix(rpcErr.Message, waits.refuse) {
		t.Fatalf("refused target: %+v", rpcErr)
	}
}
