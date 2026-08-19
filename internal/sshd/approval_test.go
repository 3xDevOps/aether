package sshd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/approvals"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// inboxEnv builds a test env whose approval seam is the real service,
// consuming the env's bus exactly as the server wires it.
func inboxEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *Config) {
		svc, err := approvals.New(approvals.Config{Store: c.Store, Bus: c.Bus})
		if err != nil {
			t.Fatalf("approvals.New: %v", err)
		}
		if err := svc.Start(context.Background()); err != nil {
			t.Fatalf("approvals.Start: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		c.Services.Approvals = svc
	})
}

// eventually polls fn until it reports success or the deadline passes.
func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Two members, one run: Ada owns a run that pauses for approval, Bob
// decides it and his watcher indicator shows up on Ada's run while he is
// attached to it.
func TestApprovalInboxAndWatcherIndicatorAcrossClients(t *testing.T) {
	e := inboxEnv(t)
	ctx := context.Background()
	bobSigner, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)

	decisions, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeApproval}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = decisions.Close() }()

	// Ada's run pauses for a plan review: the adapter publishes the pause,
	// the inbox turns it into a request.
	if _, err := e.bus.Publish(ctx, events.Event{
		SessionID: e.sess.ID,
		RunID:     e.run.ID,
		Payload: events.AgentEventPayload{
			Kind: events.AgentPause, Tool: "ExitPlanMode",
			Detail: "1. fix the backoff\n2. run the tests",
		},
	}); err != nil {
		t.Fatalf("publish pause: %v", err)
	}

	bobControl := controlAs(t, e, bobSigner)
	var inbox protocol.ApprovalListResult
	eventually(t, "the pause to reach Bob's inbox", func() bool {
		inbox = protocol.ApprovalListResult{}
		if err := bobControl.Call(protocol.MethodApprovalList, protocol.ApprovalListParams{
			SessionID: string(e.sess.ID),
		}, &inbox); err != nil {
			t.Fatalf("approval.list: %v", err)
		}
		return len(inbox.Approvals) == 1
	})
	req := inbox.Approvals[0]
	if req.RunID != string(e.run.ID) || req.Action != "ExitPlanMode" || req.Decision != "requested" {
		t.Fatalf("inbox entry = %+v, want a pending ExitPlanMode request on Ada's run", req)
	}

	// A viewer holds no steer capability, so the queue is not open to
	// everyone.
	viewerSigner, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	wantDenied(t, controlAs(t, e, viewerSigner).Call(protocol.MethodApprovalDecide, protocol.ApprovalDecideParams{
		RunID: string(e.run.ID), RequestID: req.ID, Approve: true,
	}, nil), "viewer approval.decide")

	// Naming another run must not launder the capability check.
	other := &domain.Run{
		SessionID: e.sess.ID, MemberID: bob.ID, Task: "other",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if err := e.store.CreateRun(ctx, other); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := bobControl.Call(protocol.MethodApprovalDecide, protocol.ApprovalDecideParams{
		RunID: string(other.ID), RequestID: req.ID, Approve: true,
	}, nil); err == nil {
		t.Fatal("decide against a mismatched run succeeded")
	}

	// Bob approves Ada's request.
	var decided protocol.ApprovalDecideResult
	if err := bobControl.Call(protocol.MethodApprovalDecide, protocol.ApprovalDecideParams{
		RunID: string(e.run.ID), RequestID: req.ID, Approve: true,
	}, &decided); err != nil {
		t.Fatalf("approval.decide: %v", err)
	}
	if decided.Approval.Decision != "approved" || decided.Approval.DecidedBy != string(bob.ID) {
		t.Fatalf("decision = %+v, want approved by Bob", decided.Approval)
	}
	if err := bobControl.Call(protocol.MethodApprovalDecide, protocol.ApprovalDecideParams{
		RunID: string(e.run.ID), RequestID: req.ID, Approve: false,
	}, nil); err == nil {
		t.Fatal("deciding an already decided request succeeded")
	}

	// The decision is stamped into the timeline, attributed to Bob.
	var approved bool
	for !approved {
		select {
		case ev, ok := <-decisions.Events():
			if !ok {
				t.Fatal("approval event stream closed before the decision")
			}
			p, isApproval := ev.Payload.(events.ApprovalPayload)
			if !isApproval || p.Decision != events.ApprovalApproved {
				continue
			}
			if p.RequestID != req.ID || ev.ActorID != bob.ID || ev.RunID != e.run.ID {
				t.Fatalf("approval event = %+v (actor %s, run %s), want Bob deciding Ada's request",
					p, ev.ActorID, ev.RunID)
			}
			approved = true
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the approval event")
		}
	}

	// Ada sees the decided request in the session's history.
	var history protocol.ApprovalListResult
	if err := controlClient(t, e).Call(protocol.MethodApprovalList, protocol.ApprovalListParams{
		SessionID: string(e.sess.ID), All: true,
	}, &history); err != nil {
		t.Fatalf("approval.list --all: %v", err)
	}
	if len(history.Approvals) != 1 || history.Approvals[0].DecidedBy != string(bob.ID) {
		t.Fatalf("history = %+v, want one request decided by Bob", history.Approvals)
	}

	// Bob watches Ada's run: attaching is what makes him a watcher, and
	// the run's watcher list is what a run card renders.
	if ack := attachAs(t, e, bobSigner, e.run.ID, false); !ack.OK {
		t.Fatalf("Bob's attach = %+v, want ok", ack)
	}
	var ttl protocol.PresenceHeartbeatResult
	if err := bobControl.Call(protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{
		SessionID: string(e.sess.ID),
	}, &ttl); err != nil {
		t.Fatalf("presence.heartbeat: %v", err)
	}
	if ttl.TTLSeconds <= 0 {
		t.Fatalf("heartbeat ttl = %d, want a positive TTL", ttl.TTLSeconds)
	}

	ada := controlClient(t, e)
	var watchers protocol.PresenceRosterResult
	eventually(t, "Bob's watcher indicator on Ada's run", func() bool {
		watchers = protocol.PresenceRosterResult{}
		if err := ada.Call(protocol.MethodPresenceRoster, protocol.PresenceRosterParams{
			SessionID: string(e.sess.ID), RunID: string(e.run.ID),
		}, &watchers); err != nil {
			t.Fatalf("presence.roster: %v", err)
		}
		return len(watchers.Members) == 1
	})
	w := watchers.Members[0]
	if w.MemberID != string(bob.ID) || w.State != string(events.PresenceWatching) ||
		len(w.Watching) != 1 || w.Watching[0] != string(e.run.ID) {
		t.Fatalf("watcher = %+v, want Bob watching %s", w, e.run.ID)
	}
}

// Without the service wired the methods degrade to CodeUnavailable
// instead of failing the connection.
func TestApprovalMethodsUnavailableWithoutService(t *testing.T) {
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	for _, call := range []struct {
		method string
		params any
	}{
		{protocol.MethodApprovalList, protocol.ApprovalListParams{SessionID: string(e.sess.ID)}},
		{protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{SessionID: string(e.sess.ID)}},
		{protocol.MethodPresenceRoster, protocol.PresenceRosterParams{}},
	} {
		err := c.Call(call.method, call.params, nil)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
			t.Fatalf("%s = %v, want CodeUnavailable", call.method, err)
		}
	}
}
