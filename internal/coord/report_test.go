package coord

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// fakeSink is the scheduler's side of run.report.
type fakeSink struct {
	mu   sync.Mutex
	got  []agentstatus.Report
	runs []domain.RunID
}

func (f *fakeSink) ReportAgentState(_ context.Context, run domain.RunID, r agentstatus.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	f.got = append(f.got, r)
	return nil
}

func (f *fakeSink) reports() ([]domain.RunID, []agentstatus.Report) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs, f.got
}

func TestRunReportRejectsInvalidStatesAndSanitizesReason(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, 1, func(c *Config) { c.Reports = sink })
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(context.Background(), run, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	client := h.dial(t, run)

	// A state outside the two is a parameter error, and never reaches the
	// sink: an agent must not be able to invent a run status.
	for _, bad := range []string{"", "running", "Waiting", "needs-attention"} {
		err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{State: bad}, nil)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidParams {
			t.Errorf("run.report state %q = %v, want a %d (invalid params) error", bad, err, protocol.CodeInvalidParams)
		}
	}
	if _, after := sink.reports(); len(after) != 0 {
		t.Errorf("sink saw %d reports after the refusals, want none", len(after))
	}

	// The reason is rendered on a run card and in a terminal listing, so
	// control bytes and unbounded length do not survive the wire.
	if err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{
		State:  string(agentstatus.Idle),
		Reason: "waiting\x1b[2J for\nyour input " + strings.Repeat("x", 400),
	}, nil); err != nil {
		t.Fatalf("run.report with a hostile reason: %v", err)
	}
	_, all := sink.reports()
	reason := all[len(all)-1].Reason
	if strings.ContainsAny(reason, "\x1b\n") {
		t.Errorf("reason %q kept control bytes", reason)
	}
	if n := len([]rune(reason)); n != maxReportReason {
		t.Errorf("reason is %d runes, want it capped at %d", n, maxReportReason)
	}

	// The method set is still closed.
	err := client.Call("run.kill", struct{}{}, nil)
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeMethodNotFound {
		t.Errorf("run.kill = %v, want method not found", err)
	}
}

// TestRunReportWithoutSink: a server with nothing behind run.report says
// so rather than telling the agent's hook its state was recorded.
func TestRunReportWithoutSink(t *testing.T) {
	h := newHarness(t, 1)
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(context.Background(), run, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	err := h.dial(t, run).Call(protocol.MethodRunReport,
		protocol.RunReportParams{State: string(agentstatus.Working)}, nil)
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeInternal {
		t.Fatalf("run.report with no sink = %v, want a %d (internal) error", err, protocol.CodeInternal)
	}
}

func TestRunReportRejectsInvalidInputMetadataBeforeSink(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, 1, func(c *Config) { c.Reports = sink })
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, run)
	valid := domain.RunInputRequest{ID: "request-1", SessionID: "session-1", Kind: "question"}
	tooManyUpdates := make([]domain.RunInputUpdate, 129)
	for i := range tooManyUpdates {
		tooManyUpdates[i] = domain.RunInputUpdate{Operation: "open", ID: valid.ID, SessionID: valid.SessionID, Kind: valid.Kind}
	}
	tooManyRequests := make([]domain.RunInputRequest, 129)
	for i := range tooManyRequests {
		tooManyRequests[i] = valid
	}
	tests := []struct {
		name    string
		updates []domain.RunInputUpdate
	}{
		{"missing id", []domain.RunInputUpdate{{Operation: "open", SessionID: valid.SessionID, Kind: valid.Kind}}},
		{"blank id", []domain.RunInputUpdate{{Operation: "open", ID: " \t ", SessionID: valid.SessionID, Kind: valid.Kind}}},
		{"control id", []domain.RunInputUpdate{{Operation: "close", ID: "request\x1b", SessionID: valid.SessionID, Kind: valid.Kind}}},
		{"oversized id", []domain.RunInputUpdate{{Operation: "open", ID: strings.Repeat("x", 257), SessionID: valid.SessionID, Kind: valid.Kind}}},
		{"oversized utf8 id", []domain.RunInputUpdate{{Operation: "open", ID: strings.Repeat("é", 129), SessionID: valid.SessionID, Kind: valid.Kind}}},
		{"missing session", []domain.RunInputUpdate{{Operation: "open", ID: valid.ID, Kind: valid.Kind}}},
		{"blank session", []domain.RunInputUpdate{{Operation: "clear", SessionID: " "}}},
		{"control session", []domain.RunInputUpdate{{Operation: "clear", SessionID: "session\n1"}}},
		{"oversized session", []domain.RunInputUpdate{{Operation: "clear", SessionID: strings.Repeat("x", 257)}}},
		{"unsupported operation", []domain.RunInputUpdate{{Operation: "idle", ID: valid.ID, SessionID: valid.SessionID, Kind: valid.Kind}}},
		{"missing kind", []domain.RunInputUpdate{{Operation: "open", ID: valid.ID, SessionID: valid.SessionID}}},
		{"unsupported kind", []domain.RunInputUpdate{{Operation: "open", ID: valid.ID, SessionID: valid.SessionID, Kind: "turn_end"}}},
		{"invalid replacement", []domain.RunInputUpdate{{Operation: "replace", Requests: []domain.RunInputRequest{{ID: valid.ID, Kind: valid.Kind}}}}},
		{"too many updates", tooManyUpdates},
		{"too many requests", []domain.RunInputUpdate{{Operation: "replace", Requests: tooManyRequests}}},
		{"invalid later update", []domain.RunInputUpdate{
			{Operation: "open", ID: valid.ID, SessionID: valid.SessionID, Kind: valid.Kind},
			{Operation: "close", ID: valid.ID, Kind: valid.Kind},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{
				State: string(agentstatus.Working), InputUpdates: tt.updates,
			}, nil)
			var perr *protocol.Error
			if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidParams {
				t.Fatalf("invalid metadata = %v, want invalid params", err)
			}
		})
	}
	if _, reports := sink.reports(); len(reports) != 0 {
		t.Fatalf("invalid metadata reached sink: %+v", reports)
	}
}

func TestRunReportRejectsInvalidExecutionWithInput(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, 1, func(c *Config) { c.Reports = sink })
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, run)
	for _, params := range []protocol.RunReportParams{
		{Reason: "agent idle", InputUpdates: []domain.RunInputUpdate{}},
		{State: "idle", InputUpdates: []domain.RunInputUpdate{{Operation: "replace", Requests: []domain.RunInputRequest{}}}},
	} {
		err := client.Call(protocol.MethodRunReport, params, nil)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidParams {
			t.Fatalf("invalid execution state = %v, want invalid params", err)
		}
	}
	if _, after := sink.reports(); len(after) != 0 {
		t.Fatal("invalid execution state reached sink")
	}
}

type reportFileSink struct {
	path   string
	errors chan error
}

func (s reportFileSink) ReportAgentState(_ context.Context, _ domain.RunID, report agentstatus.Report) error {
	data, err := json.Marshal(report)
	if err == nil {
		err = os.WriteFile(s.path, data, 0o600)
	}
	s.errors <- err
	return err
}

func TestRunReportReturnsSinkPersistenceError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sink := reportFileSink{path: filepath.Join(blocker, "pending-inputs.json"), errors: make(chan error, 1)}
	h := newHarness(t, 1, func(c *Config) { c.Reports = sink })
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, run)
	for _, state := range []string{"", string(agentstatus.Working)} {
		err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{
			State: state,
			InputUpdates: []domain.RunInputUpdate{{
				Operation: "open", ID: "request-1", SessionID: "session-1", Kind: "question",
			}},
		}, nil)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInternal {
			t.Fatalf("persistence failure = %v, want internal error", err)
		}
		var persistenceErr error
		select {
		case persistenceErr = <-sink.errors:
		default:
			t.Fatal("report failed before reaching persistence")
		}
		var pathErr *os.PathError
		if !errors.As(persistenceErr, &pathErr) {
			t.Fatalf("sink did not encounter a filesystem failure: %v", persistenceErr)
		}
		if !strings.Contains(perr.Message, persistenceErr.Error()) {
			t.Fatalf("persistence failure lost its cause: %v, want %v", perr, persistenceErr)
		}
	}
}
