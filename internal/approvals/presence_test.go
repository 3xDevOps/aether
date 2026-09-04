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

func (nopStore) ListApprovals(context.Context, domain.WorkspaceID, string) ([]*store.Approval, error) {
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

	const workspace = domain.WorkspaceID("ws")
	svc.ConnectionOpened("bob")
	svc.ConnectionOpened("ada")
	if err := svc.Heartbeat(ctx, "ada", workspace); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if _, err := bus.Publish(ctx, events.Event{
		WorkspaceID: workspace, RunID: "run", ActorID: "bob",
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

	roster := svc.Roster(workspace, "run")
	if len(roster) != 1 || roster[0].Member != "bob" || roster[0].State != events.PresenceWatching {
		t.Fatalf("roster = %+v, want bob still watching", roster)
	}
}

// Presence is per workspace and per attach: a member attached twice to
// one run keeps watching it until the second attach ends, and their
// presence in one workspace survives activity in another.
func TestWatchingSurvivesSecondAttachAndOtherWorkspace(t *testing.T) {
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

	const wsA, wsB = domain.WorkspaceID("ws-a"), domain.WorkspaceID("ws-b")
	svc.ConnectionOpened("bob")
	svc.ConnectionOpened("ada")
	presence := func(member domain.MemberID, run domain.RunID, state events.PresenceState) {
		t.Helper()
		if _, perr := bus.Publish(ctx, events.Event{
			WorkspaceID: wsA, RunID: run, ActorID: member,
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

	// Bob also touches another workspace, which must not move his presence.
	if err := svc.Heartbeat(ctx, "bob", wsB); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for len(svc.Roster(wsA, "r2")) == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the presence events to be handled")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := svc.Roster(wsA, "r1"); len(got) != 1 || got[0].Member != "bob" || got[0].State != events.PresenceWatching {
		t.Fatalf("roster(ws-a, r1) = %+v, want bob still watching his second attach", got)
	}
	if got := svc.Roster(wsB, "r1"); len(got) != 0 {
		t.Fatalf("roster(ws-b, r1) = %+v, want empty: r1 is not that workspace's run", got)
	}
}

func TestConnectionClosedPublishesOfflineWhenLastConnectionCloses(t *testing.T) {
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
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = svc.Close() }()

	presence, err := bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypePresence}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = presence.Close() }()

	const member = domain.MemberID("ada")
	const workspace = domain.WorkspaceID("ws")
	svc.ConnectionOpened(member)
	if err := svc.Heartbeat(ctx, member, workspace); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	waitPresenceState(t, presence, member, events.PresenceOnline)

	svc.ConnectionClosed(member)
	waitPresenceState(t, presence, member, events.PresenceOffline)
	if got := svc.Roster(workspace, ""); len(got) != 0 {
		t.Fatalf("roster after close = %+v, want empty", got)
	}
}

func TestSecondConnectionKeepsMemberOnline(t *testing.T) {
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
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = svc.Close() }()

	presence, err := bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypePresence}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = presence.Close() }()

	const member = domain.MemberID("ada")
	const workspace = domain.WorkspaceID("ws")
	svc.ConnectionOpened(member)
	svc.ConnectionOpened(member)
	if err := svc.Heartbeat(ctx, member, workspace); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	waitPresenceState(t, presence, member, events.PresenceOnline)

	svc.ConnectionClosed(member)
	if got := svc.Roster(workspace, ""); len(got) != 1 || got[0].State != events.PresenceOnline {
		t.Fatalf("roster after first close = %+v, want online", got)
	}
	svc.ConnectionClosed(member)
	waitPresenceState(t, presence, member, events.PresenceOffline)
}

func TestHeartbeatWithNoConnectionIsIgnored(t *testing.T) {
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
	if err := svc.Heartbeat(ctx, "ada", "ws"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if got := svc.Roster("ws", ""); len(got) != 0 {
		t.Fatalf("roster = %+v, want empty", got)
	}
}

func TestLastConnectionCloseWithLiveWatcherUnwatchPublishesOffline(t *testing.T) {
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
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = svc.Close() }()

	presence, err := bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypePresence}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = presence.Close() }()

	const member = domain.MemberID("ada")
	const workspace = domain.WorkspaceID("ws")
	const run = domain.RunID("run")
	svc.ConnectionOpened(member)
	if _, err := bus.Publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     member,
		Payload:     events.PresencePayload{State: events.PresenceWatching},
	}); err != nil {
		t.Fatalf("publish watching: %v", err)
	}
	waitRosterState(t, svc, workspace, run, events.PresenceWatching)

	svc.ConnectionClosed(member)
	if got := svc.Roster(workspace, run); len(got) != 1 || got[0].State != events.PresenceWatching {
		t.Fatalf("roster after close = %+v, want watcher retained until unwatch", got)
	}
	if _, err := bus.Publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     member,
		Payload:     events.PresencePayload{State: events.PresenceOnline},
	}); err != nil {
		t.Fatalf("publish unwatch: %v", err)
	}
	waitPresenceStateWithin(t, presence, member, events.PresenceOffline, time.Second)
	if got := svc.Roster(workspace, ""); len(got) != 0 {
		t.Fatalf("roster after unwatch = %+v, want empty", got)
	}
}

func TestLateWatchAfterConnectionClosedDoesNotResurrectMember(t *testing.T) {
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
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = svc.Close() }()

	presence, err := bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypePresence}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = presence.Close() }()

	const member = domain.MemberID("ada")
	const workspace = domain.WorkspaceID("ws")
	const run = domain.RunID("run")
	svc.ConnectionOpened(member)
	if err := svc.Heartbeat(ctx, member, workspace); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	waitPresenceState(t, presence, member, events.PresenceOnline)
	svc.ConnectionClosed(member)
	waitPresenceState(t, presence, member, events.PresenceOffline)

	if _, err := bus.Publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     member,
		Payload:     events.PresencePayload{State: events.PresenceWatching},
	}); err != nil {
		t.Fatalf("publish late watching: %v", err)
	}
	waitPresenceStateWithin(t, presence, member, events.PresenceWatching, time.Second)

	deadline := time.After(250 * time.Millisecond)
	for {
		if got := svc.Roster(workspace, ""); len(got) != 0 {
			t.Fatalf("roster after late watch = %+v, want empty", got)
		}
		select {
		case <-deadline:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitRosterState(t *testing.T, svc *Service, workspace domain.WorkspaceID, run domain.RunID, want events.PresenceState) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		got := svc.Roster(workspace, run)
		if len(got) == 1 && got[0].State == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for roster state %s; got %+v", want, got)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitPresenceStateWithin(t *testing.T, sub events.Subscription, member domain.MemberID, want events.PresenceState, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-sub.Events():
			if ev.ActorID == member && ev.Payload.(events.PresencePayload).State == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s presence", want)
		}
	}
}

// waitPresenceState waits up to five seconds for one member's transition.
func waitPresenceState(t *testing.T, sub events.Subscription, member domain.MemberID, want events.PresenceState) {
	t.Helper()
	waitPresenceStateWithin(t, sub, member, want, 5*time.Second)
}
