package approvals

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// nopStore satisfies the inbox's persistence seam; the presence roster
// never touches it.
type nopStore struct{}

func (nopStore) CreateApproval(context.Context, *store.Approval) error { return nil }
func (nopStore) GetApproval(context.Context, string) (*store.Approval, error) {
	return nil, store.ErrNotFound
}

func (nopStore) ListApprovals(context.Context, domain.SessionID, string) ([]*store.Approval, error) {
	return nil, nil
}

func (nopStore) DecideApproval(context.Context, string, string, domain.MemberID, time.Time) error {
	return nil
}

// A member who stops heartbeating falls offline, while one holding an
// attach stays: the live channel is its own heartbeat, and the SSH server
// publishes the closing event when it drops.
func TestStaleHeartbeatGoesOfflineWatcherStays(t *testing.T) {
	ctx := context.Background()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	defer func() { _ = bus.Close() }()

	svc, err := New(Config{Store: nopStore{}, Bus: bus, TTL: 60 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if serr := svc.Start(ctx); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	defer func() { _ = svc.Close() }()

	presence, err := bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypePresence}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = presence.Close() }()

	const session = domain.SessionID("sess")
	if err := svc.Heartbeat(ctx, "ada", session); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if _, err := bus.Publish(ctx, events.Event{
		SessionID: session, RunID: "run", ActorID: "bob",
		Payload: events.PresencePayload{State: events.PresenceWatching},
	}); err != nil {
		t.Fatalf("publish watching: %v", err)
	}

	states := map[domain.MemberID][]events.PresenceState{}
	deadline := time.After(5 * time.Second)
	for {
		var offline bool
		select {
		case ev := <-presence.Events():
			p := ev.Payload.(events.PresencePayload)
			states[ev.ActorID] = append(states[ev.ActorID], p.State)
			offline = ev.ActorID == "ada" && p.State == events.PresenceOffline
		case <-deadline:
			t.Fatalf("timed out; saw %v", states)
		}
		if offline {
			break
		}
	}
	if got := states["ada"]; len(got) != 2 || got[0] != events.PresenceOnline || got[1] != events.PresenceOffline {
		t.Fatalf("ada's presence = %v, want online then offline", got)
	}

	roster := svc.Roster(session, "run")
	if len(roster) != 1 || roster[0].Member != "bob" || roster[0].State != events.PresenceWatching {
		t.Fatalf("roster = %+v, want bob still watching", roster)
	}
}

// Presence is per session and per attach: a member attached twice to one
// run keeps watching it until the second attach ends, and their presence
// in one session survives activity in another.
func TestWatchingSurvivesSecondAttachAndOtherSession(t *testing.T) {
	ctx := context.Background()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	defer func() { _ = bus.Close() }()

	svc, err := New(Config{Store: nopStore{}, Bus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if serr := svc.Start(ctx); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	defer func() { _ = svc.Close() }()

	const sessA, sessB = domain.SessionID("sess-a"), domain.SessionID("sess-b")
	presence := func(member domain.MemberID, run domain.RunID, state events.PresenceState) {
		t.Helper()
		if _, perr := bus.Publish(ctx, events.Event{
			SessionID: sessA, RunID: run, ActorID: member,
			Payload: events.PresencePayload{State: state},
		}); perr != nil {
			t.Fatalf("publish %s: %v", state, perr)
		}
	}
	presence("bob", "r1", events.PresenceWatching)
	presence("bob", "r1", events.PresenceWatching)
	presence("bob", "r1", events.PresenceOnline)
	// Delivery is ordered, so this last event marks the point where all of
	// bob's are handled.
	presence("ada", "r2", events.PresenceWatching)

	// Bob also touches another session, which must not move his presence.
	if err := svc.Heartbeat(ctx, "bob", sessB); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for len(svc.Roster(sessA, "r2")) == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the presence events to be handled")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := svc.Roster(sessA, "r1"); len(got) != 1 || got[0].Member != "bob" || got[0].State != events.PresenceWatching {
		t.Fatalf("roster(sess-a, r1) = %+v, want bob still watching his second attach", got)
	}
	if got := svc.Roster(sessB, "r1"); len(got) != 0 {
		t.Fatalf("roster(sess-b, r1) = %+v, want empty: r1 is not that session's run", got)
	}
}
