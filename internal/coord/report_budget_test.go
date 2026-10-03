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
	if err := reconnected.Call(protocol.MethodRunReport, protocol.RunReportParams{State: string(agentstatus.Idle)}, nil); err == nil {
		t.Fatal("lifecycle flood escaped its per-run budget")
	}
	socket := filepath.Join(dir, coordtransport.SocketName)
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
	if err := client.Call(protocol.MethodRunReport, protocol.RunReportParams{State: string(agentstatus.Idle), Reason: agentstatus.ReasonIdle}, nil); err != nil {
		t.Fatalf("lifecycle state after refill: %v", err)
	}
	runs, reports := sink.reports()
	if len(reports) != requestBurst+1 || reports[len(reports)-1].State != agentstatus.Idle || runs[len(runs)-1] != a {
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
