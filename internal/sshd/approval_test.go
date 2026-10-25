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

// presenceEnv builds a test env whose presence seam is the real service,
// consuming the env's bus exactly as the server wires it.
func presenceEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *Config) {
		svc, err := approvals.New(approvals.Config{Bus: c.Bus})
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

// eventually polls fn until it reports success or the deadline passes. The
// deadline is generous because CI runners are small and run every package
// in parallel: a background loop on a short ticker can be starved for far
// longer than its period, and the cost of a slow pass is a few seconds
// while the cost of a tight bound is a flake that fails the whole run.
func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Bob's watcher indicator shows up on Ada's run while he is attached to it.
func TestWatcherIndicatorAcrossClients(t *testing.T) {
	t.Parallel()
	e := presenceEnv(t)
	bobSigner, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bobControl := controlAs(t, e, bobSigner)

	// Bob watches Ada's run: attaching is what makes him a watcher, and
	// the run's watcher list is what a run card renders.
	if ack := attachAs(t, e, bobSigner, e.run.ID, false); !ack.OK {
		t.Fatalf("Bob's attach = %+v, want ok", ack)
	}
	var ttl protocol.PresenceHeartbeatResult
	if err := bobControl.Call(protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{
		WorkspaceID: string(e.ws.ID),
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
			WorkspaceID: string(e.ws.ID), RunID: string(e.run.ID),
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
func TestPresenceMethodsUnavailableWithoutService(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	c := controlClient(t, e)
	for _, call := range []struct {
		method string
		params any
	}{
		{protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{WorkspaceID: string(e.ws.ID)}},
		{protocol.MethodPresenceRoster, protocol.PresenceRosterParams{}},
	} {
		err := c.Call(call.method, call.params, nil)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
			t.Fatalf("%s = %v, want CodeUnavailable", call.method, err)
		}
	}
}
