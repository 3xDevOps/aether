package coord

import (
	"context"
	"errors"
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
	err  error
}

func (f *fakeSink) ReportAgentState(_ context.Context, run domain.RunID, r agentstatus.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	f.got = append(f.got, r)
	return f.err
}

func (f *fakeSink) reports() ([]domain.RunID, []agentstatus.Report) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs, f.got
}

// TestRunReportReachesTheSink drives run.report over the real socket the
// way the in-container reporter does: the run is the socket it arrived on,
// the two legal states pass through, and anything else is refused before
// the sink is touched.
func TestRunReportReachesTheSink(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, 1, func(c *Config) { c.Reports = sink })
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(context.Background(), run, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	client := h.dial(t, run)

	var result protocol.RunReportResult
	if err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{
		State: string(agentstatus.Waiting), Reason: agentstatus.ReasonInput,
	}, &result); err != nil {
		t.Fatalf("run.report waiting: %v", err)
	}
	if err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{
		State: string(agentstatus.Working),
	}, nil); err != nil {
		t.Fatalf("run.report working: %v", err)
	}
	runs, got := sink.reports()
	want := []agentstatus.Report{
		{State: agentstatus.Waiting, Reason: agentstatus.ReasonInput},
		{State: agentstatus.Working},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sink saw %+v, want %+v", got, want)
	}
	for _, r := range runs {
		if r != run {
			t.Errorf("report attributed to run %s, want %s: the socket is the identity", r, run)
		}
	}

	// A state outside the two is a parameter error, and never reaches the
	// sink: an agent must not be able to invent a run status.
	for _, bad := range []string{"", "running", "Waiting", "needs-attention"} {
		err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{State: bad}, nil)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidParams {
			t.Errorf("run.report state %q = %v, want a %d (invalid params) error", bad, err, protocol.CodeInvalidParams)
		}
	}
	if _, after := sink.reports(); len(after) != len(want) {
		t.Errorf("sink saw %d reports after the refusals, want %d", len(after), len(want))
	}

	// The reason is rendered on a run card and in a terminal listing, so
	// control bytes and unbounded length do not survive the wire.
	if err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{
		State:  string(agentstatus.Waiting),
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
