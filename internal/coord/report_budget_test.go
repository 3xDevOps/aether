package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordcli"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestLifecycleReportsPreserveForegroundAndHookBudgets(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, 2, func(c *Config) { c.Reports = sink })
	h.start()
	ctx := context.Background()
	a, b := h.run(0), h.run(1)
	h.peers.pair(a, b, "shared.go")
	dir, err := h.svc.Provision(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, a)
	for i := range requestBurst {
		if err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{State: string(agentstatus.Working)}, nil); err != nil {
			t.Fatalf("lifecycle report %d: %v", i, err)
		}
	}
	// Another connection must share the same bounded lifecycle allowance.
	reconnected := h.dial(t, a)
	if err := reconnected.Call(protocol.MethodRunReport, protocol.RunReportParams{State: string(agentstatus.Working)}, nil); err == nil {
		t.Fatal("lifecycle flood escaped its per-run budget")
	}
	socket := filepath.Join(dir, coordtransport.SocketName)
	t.Setenv("TMPDIR", t.TempDir())
	if code, err := runContextHook(t, socket); err != nil || code != coordcli.ExitOK {
		t.Fatalf("context hook after lifecycle burst: exit %d, error %v", code, err)
	}
	var output bytes.Buffer
	cli := func(args ...string) {
		t.Helper()
		output.Reset()
		code, err := coordcli.Run(ctx, args, coordcli.Config{Socket: socket, Out: &output, ErrOut: io.Discard})
		if err != nil || code != coordcli.ExitOK {
			t.Fatalf("foreground %v after lifecycle burst: exit %d, error %v, output %s", args, code, err, output.String())
		}
	}
	cli("status")
	cli("send", "--to", string(b), "--body", "READY", "--idempotency-key", "ready-after-reports")
	peer, rpcErr := h.svc.Inbox(ctx, b, protocol.CoordInboxParams{})
	if rpcErr != nil || len(peer.Messages) != 1 || peer.Messages[0].Body != "READY" || peer.Messages[0].FromRunID != string(a) {
		t.Fatalf("foreground delivery after lifecycle burst: %+v, %v", peer, rpcErr)
	}
	if _, rpcErr := h.svc.Send(ctx, b, sendParams(a, "acknowledged")); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	cli("inbox")
	var reply struct {
		Result protocol.CoordInboxResult `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Result.Messages) != 1 || reply.Result.Messages[0].Body != "acknowledged" || reply.Result.AckToken == "" {
		t.Fatalf("foreground inbox after lifecycle burst: %s", output.String())
	}
	h.advance(requestRefill)
	if err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{State: string(agentstatus.Working)}, nil); err != nil {
		t.Fatalf("lifecycle state after refill: %v", err)
	}
	runs, reports := sink.reports()
	if len(reports) != requestBurst+1 || reports[len(reports)-1].State != agentstatus.Working || runs[len(runs)-1] != a {
		t.Fatalf("admitted lifecycle states: runs=%v reports=%v", runs, reports)
	}
}

func TestForegroundExhaustionPreservesLifecycleReports(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, 1, func(c *Config) { c.Reports = sink })
	h.start()
	if _, err := h.svc.Provision(t.Context(), h.run(0), nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, h.run(0))
	for range requestBurst {
		if err := client.Call(protocol.MethodCoordStatus, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	requireTransportConflict(t, client.Call(protocol.MethodCoordStatus, nil, nil))
	if err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{State: string(agentstatus.Idle), Reason: agentstatus.ReasonIdle}, nil); err != nil {
		t.Fatalf("idle report after foreground burst: %v", err)
	}
	requireTransportConflict(t, client.Call(protocol.MethodCoordStatus, nil, nil))
}

// A native harness reports every tool use, so a turn whose tools outrun the
// lifecycle budget would spend the allowance its own end needs: a dropped
// turn end leaves the run working, with the question it reported never
// shown (internal/scheduler/report.go). Reports that decide the run's
// state (a turn start or end, a question, a background-held stop) have
// their own budget, and that budget still bounds them.
func TestTurnEndSurvivesALifecycleFlood(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, 1, func(c *Config) { c.Reports = sink })
	h.start()
	run := h.run(0)
	if _, err := h.svc.Provision(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	client := h.dial(t, run)
	// pi and omp attach a full input snapshot to every heartbeat.
	working := protocol.RunReportParams{State: string(agentstatus.Working),
		InputUpdates: []domain.RunInputUpdate{{Operation: "replace", Requests: []domain.RunInputRequest{}}}}
	for i := range requestBurst {
		if err := client.Call(protocol.MethodRunReport, working, nil); err != nil {
			t.Fatalf("tool-use report %d: %v", i, err)
		}
	}
	if err := client.Call(protocol.MethodRunReport, working, nil); err == nil {
		t.Fatal("tool-use reports escaped their per-run budget")
	}
	turnStart := protocol.RunReportParams{State: string(agentstatus.Working), TurnStart: true}
	if err := client.Call(protocol.MethodRunReport, turnStart, nil); err != nil {
		t.Fatalf("turn start after a tool-use flood: %v", err)
	}
	question := protocol.RunReportParams{InputUpdates: []domain.RunInputUpdate{{Operation: "open", SessionID: "s", Kind: "question", ID: "q1"}}}
	if err := client.Call(protocol.MethodRunReport, question, nil); err != nil {
		t.Fatalf("question after a tool-use flood: %v", err)
	}
	held := protocol.RunReportParams{State: string(agentstatus.Working), Reason: agentstatus.BackgroundReason(1)}
	if err := client.Call(protocol.MethodRunReport, held, nil); err != nil {
		t.Fatalf("background-held stop after a tool-use flood: %v", err)
	}
	turnEnd := protocol.RunReportParams{State: string(agentstatus.Idle), Reason: agentstatus.ReasonIdle, Stop: true}
	if err := client.Call(protocol.MethodRunReport, turnEnd, nil); err != nil {
		t.Fatalf("turn end after a tool-use flood: %v", err)
	}
	_, reports := sink.reports()
	if len(reports) != requestBurst+4 || reports[len(reports)-1].State != agentstatus.Idle {
		t.Fatalf("admitted reports = %+v, want the turn start, the question, the held stop and the turn end after %d tool-use reports", reports, requestBurst)
	}
	for i := range requestBurst - 4 {
		if err := client.Call(protocol.MethodRunReport, turnEnd, nil); err != nil {
			t.Fatalf("turn end %d within its budget: %v", i, err)
		}
	}
	if err := client.Call(protocol.MethodRunReport, turnEnd, nil); err == nil {
		t.Fatal("turn ends escaped their per-run budget")
	}
}
