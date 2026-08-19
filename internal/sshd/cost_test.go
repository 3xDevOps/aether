package sshd

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/cost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// costEnv builds a test env whose cost seam is the real service, wired
// the way the server wires it: it consumes the env's bus and guards the
// run controller's admission path.
func costEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *Config) {
		svc, err := cost.New(cost.Config{Store: c.Store, Bus: c.Bus})
		if err != nil {
			t.Fatalf("cost.New: %v", err)
		}
		if err := svc.Start(context.Background()); err != nil {
			t.Fatalf("cost.Start: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		c.Services.Costs = svc
		c.Runs = GuardRuns(c.Runs, svc, c.Store)
	})
}

// A session hits its cap: the new run is refused with a clear error, the
// run already running finishes untouched and keeps its metered record,
// and an admin's override admits the next run.
func TestSessionBudgetRefusesNewRunsUntilAdminOverrides(t *testing.T) {
	e := costEnv(t)
	ctx := context.Background()
	control := controlAs(t, e, e.signer)

	budgetEvents, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeBudget}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = budgetEvents.Close() }()

	var budget protocol.BudgetResult
	if err := control.Call(protocol.MethodBudgetSet, protocol.BudgetSetParams{
		SessionID: string(e.sess.ID), LimitUSD: 1, WarnUSD: 0.5,
	}, &budget); err != nil {
		t.Fatalf("budget.set: %v", err)
	}
	if budget.Budget == nil || budget.Budget.LimitUSD != 1 || budget.State != string(events.BudgetOK) {
		t.Fatalf("budget after set = %+v, want a $1 cap in state ok", budget)
	}

	// The adapter meters Ada's running run past the cap.
	if _, err := e.bus.Publish(ctx, events.Event{
		SessionID: e.sess.ID,
		RunID:     e.run.ID,
		Payload: events.RunCostPayload{
			InputTokens: 40124, OutputTokens: 1834, CostUSD: 1.25, Metered: true,
		},
	}); err != nil {
		t.Fatalf("publish run cost: %v", err)
	}
	eventually(t, "the session's spend to reach the cap", func() bool {
		budget = protocol.BudgetResult{}
		if err := control.Call(protocol.MethodBudgetGet, protocol.BudgetGetParams{
			SessionID: string(e.sess.ID),
		}, &budget); err != nil {
			t.Fatalf("budget.get: %v", err)
		}
		return budget.State == string(events.BudgetExceeded)
	})
	if budget.Spend.CostUSD != 1.25 || budget.Spend.Metered != 1 || budget.Advisory {
		t.Fatalf("spend = %+v, want $1.25 fully metered", budget.Spend)
	}

	// The crossing is announced on the bus for the timeline and notifications.
	wantBudgetEvent(t, budgetEvents, events.BudgetExceeded)

	// A new run is refused, and the refusal happens before the scheduler
	// is ever asked.
	launchErr := control.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: string(e.sess.ID), Task: "another thing", Harness: "claude", Mode: "tui",
	}, nil)
	if launchErr == nil {
		t.Fatal("run.launch succeeded past the cap")
	}
	if !strings.Contains(launchErr.Error(), "session budget exceeded") ||
		!strings.Contains(launchErr.Error(), "$1.25 of its $1.00 cap") {
		t.Fatalf("refusal = %v, want a clear budget message", launchErr)
	}
	for _, call := range e.runs.Calls() {
		if strings.HasPrefix(call, "launch:") {
			t.Fatalf("scheduler was asked to launch past the cap: %q", call)
		}
	}

	// The run already running is never touched: it finishes on its own and
	// its metered record survives the terminal transition.
	if _, err := e.bus.Publish(ctx, events.Event{
		SessionID: e.sess.ID,
		RunID:     e.run.ID,
		Payload:   events.RunStatusPayload{From: domain.RunRunning, To: domain.RunMerged},
	}); err != nil {
		t.Fatalf("publish terminal status: %v", err)
	}
	for _, call := range e.runs.Calls() {
		if strings.HasPrefix(call, "kill:") || strings.HasPrefix(call, "close:") {
			t.Fatalf("budget enforcement stopped a running run: %q", call)
		}
	}
	var report protocol.CostReportResult
	eventually(t, "the finished run's usage to settle", func() bool {
		report = protocol.CostReportResult{}
		if err := control.Call(protocol.MethodCostReport, protocol.CostReportParams{
			SessionID: string(e.sess.ID),
		}, &report); err != nil {
			t.Fatalf("cost.report: %v", err)
		}
		return len(report.Runs) == 1
	})
	if !report.Runs[0].Metered || report.Runs[0].CostUSD != 1.25 || report.Total.Unmetered != 0 {
		t.Fatalf("report = %+v, want the run still metered at $1.25", report)
	}
	if report.Runs[0].MemberID != string(e.member.ID) || len(report.Members) != 1 {
		t.Fatalf("attribution = %+v, want the run's usage on its owner", report.Members)
	}

	// Only an admin may move the budget.
	viewerSigner, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	wantDenied(t, controlAs(t, e, viewerSigner).Call(protocol.MethodBudgetSet, protocol.BudgetSetParams{
		SessionID: string(e.sess.ID), LimitUSD: 100,
	}, nil), "viewer budget.set")

	// The admin overrides the cap; the next run is admitted.
	if err := control.Call(protocol.MethodBudgetSet, protocol.BudgetSetParams{
		SessionID: string(e.sess.ID), LimitUSD: 1, Override: true,
	}, &budget); err != nil {
		t.Fatalf("budget.set override: %v", err)
	}
	if budget.State != string(events.BudgetExceeded) || budget.Budget == nil || !budget.Budget.Override {
		t.Fatalf("budget after override = %+v, want still exceeded but overridden", budget)
	}
	var launched protocol.RunResult
	if err := control.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: string(e.sess.ID), Task: "another thing", Harness: "claude", Mode: "tui",
	}, &launched); err != nil {
		t.Fatalf("run.launch under override: %v", err)
	}
	if launched.Run.ID == "" {
		t.Fatal("override admitted the run but returned nothing")
	}
}

// A run whose harness has no adapter is recorded as unmetered when it
// finishes - not as free - and every surface that reports it says so.
func TestRunWithoutAdapterIsUnmeteredNotZero(t *testing.T) {
	e := costEnv(t)
	ctx := context.Background()
	control := controlAs(t, e, e.signer)

	costs, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeRunCost}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = costs.Close() }()

	if _, err := e.bus.Publish(ctx, events.Event{
		SessionID: e.sess.ID,
		RunID:     e.run.ID,
		Payload:   events.RunStatusPayload{From: domain.RunRunning, To: domain.RunFailed},
	}); err != nil {
		t.Fatalf("publish terminal status: %v", err)
	}

	var report protocol.CostReportResult
	eventually(t, "the unmetered run to be recorded", func() bool {
		report = protocol.CostReportResult{}
		if err := control.Call(protocol.MethodCostReport, protocol.CostReportParams{
			SessionID: string(e.sess.ID),
		}, &report); err != nil {
			t.Fatalf("cost.report: %v", err)
		}
		return len(report.Runs) == 1
	})
	if report.Runs[0].Metered || report.Total.Unmetered != 1 || report.Total.CostUSD != 0 {
		t.Fatalf("report = %+v, want one unmetered run and no invented numbers", report)
	}

	// The unmetered signal reaches the bus, so consumers can tell "not
	// measured" from "cost nothing".
	signal := waitForPayload(t, costs, "an unmetered run.cost signal", func(p events.Payload) bool {
		_, ok := p.(events.RunCostPayload)
		return ok
	}).(events.RunCostPayload)
	if signal.Metered {
		t.Fatalf("run.cost signal = %+v, want it marked unmetered", signal)
	}

	// A budget over that spend is advisory and says so.
	var budget protocol.BudgetResult
	if err := control.Call(protocol.MethodBudgetSet, protocol.BudgetSetParams{
		SessionID: string(e.sess.ID), LimitUSD: 5,
	}, &budget); err != nil {
		t.Fatalf("budget.set: %v", err)
	}
	if !budget.Advisory || budget.State != string(events.BudgetOK) {
		t.Fatalf("budget = %+v, want an advisory budget in state ok", budget)
	}
}

func wantBudgetEvent(t *testing.T, sub events.Subscription, want events.BudgetState) {
	t.Helper()
	waitForPayload(t, sub, fmt.Sprintf("a %q budget event", want), func(p events.Payload) bool {
		b, ok := p.(events.BudgetPayload)
		return ok && b.State == want
	})
}

// waitForPayload consumes a subscription until match accepts a payload,
// failing on a deadline rather than blocking the test run.
func waitForPayload(t *testing.T, sub events.Subscription, what string, match func(events.Payload) bool) events.Payload {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				t.Fatalf("subscription closed before %s", what)
			}
			if match(e.Payload) {
				return e.Payload
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
