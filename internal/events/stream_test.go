package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestStreamWireSendsAWatchChangeToItsOwnerAlone(t *testing.T) {
	ctx := context.Background()
	bus, err := NewInProc(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close() //nolint:errcheck // test cleanup
	for _, member := range []domain.MemberID{"mem-ada", "mem-bob"} {
		sub, err := bus.Subscribe(ctx, SubscribeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, owner := range []domain.MemberID{"mem-ada", "mem-bob"} {
			if _, err := bus.Publish(ctx, Event{WorkspaceID: ServerScope, Payload: WatchChangedPayload{WatchID: "wat-" + string(owner), MemberID: owner}}); err != nil {
				t.Fatal(err)
			}
		}
		var got []string
		go func() { time.Sleep(100 * time.Millisecond); _ = sub.Close() }()
		if err := StreamWire(sub, member, Scope{Workspace: func(domain.WorkspaceID) bool { return true }}, func(e protocol.Event) error {
			got = append(got, string(e.Payload))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0], `"member_id":"`+string(member)+`"`) {
			t.Fatalf("%s streamed %q, want only their own watch change", member, got)
		}
	}
}

func TestStreamWireSendsOnlyReachableWorkspaces(t *testing.T) {
	ctx := context.Background()
	bus, err := NewInProc(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close() //nolint:errcheck // test cleanup
	sub, err := bus.Subscribe(ctx, SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ws := range []domain.WorkspaceID{"ws-mine", "ws-foreign", ServerScope} {
		if _, err := bus.Publish(ctx, Event{WorkspaceID: ws, Payload: MemberChangedPayload{MemberID: "mem-ada", DisplayName: string(ws)}}); err != nil {
			t.Fatal(err)
		}
	}
	go func() { time.Sleep(100 * time.Millisecond); _ = sub.Close() }()
	var got []string
	reaches := func(ws domain.WorkspaceID) bool { return ws == "ws-mine" }
	if err := StreamWire(sub, "mem-ada", Scope{Workspace: reaches}, func(e protocol.Event) error {
		got = append(got, e.WorkspaceID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "ws-mine,"+string(ServerScope) {
		t.Fatalf("streamed workspaces %q, want ws-mine and the server scope", got)
	}
}

func TestStreamWireLeavesOutAnUnreachableExtraRepository(t *testing.T) {
	ctx := context.Background()
	bus, err := NewInProc(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close() //nolint:errcheck // test cleanup
	sub, err := bus.Subscribe(ctx, SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Payload{
		GitBranchPayload{WorkspaceID: "ws-foreign", Repo: "ws-foreign", Branch: "foreign"},
		RunDiffPayload{Repo: "ws-foreign"},
		RunPRPayload{Repo: "ws-foreign", Number: 1},
		GitBranchPayload{WorkspaceID: "ws-mine", Branch: "mine"},
		RunDiffPayload{},
		RunPRPayload{Number: 2},
	} {
		if _, err := bus.Publish(ctx, Event{WorkspaceID: "ws-mine", RunID: "run-1", Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	go func() { time.Sleep(100 * time.Millisecond); _ = sub.Close() }()
	var got []string
	reaches := func(ws domain.WorkspaceID) bool { return ws == "ws-mine" }
	if err := StreamWire(sub, "mem-ada", Scope{Workspace: reaches}, func(e protocol.Event) error {
		got = append(got, e.Type)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != string(TypeGitBranch)+","+string(TypeRunDiff)+","+string(TypeRunPR) {
		t.Fatalf("streamed %q, want only the primary repository's branch, diff and pull request", got)
	}
}

// An overlap set is delivered with the peers a reader cannot fully reach
// left out, an emptied set included, so its other changes still arrive.
func TestScopedLeavesOutUnreachableOverlapPeers(t *testing.T) {
	hidden := domain.RunID("run-hidden")
	scope := Scope{
		Workspace: func(domain.WorkspaceID) bool { return true },
		Run:       func(id domain.RunID) bool { return id != hidden },
	}
	ev := Event{WorkspaceID: "ws-a", RunID: "run-mine", Payload: OverlapPayload{With: []OverlapPeer{
		{RunID: "run-seen", Files: []string{"a.go"}}, {RunID: hidden, Files: []string{"a.go"}},
	}}}
	if !Reachable(ev, scope) {
		t.Fatal("an overlap set naming a hidden peer was withheld")
	}
	got := Scoped(ev, scope).Payload.(OverlapPayload)
	if len(got.With) != 1 || got.With[0].RunID != "run-seen" {
		t.Fatalf("scoped overlap = %+v, want only the reachable peer", got.With)
	}
	ev.Payload = OverlapPayload{With: []OverlapPeer{{RunID: hidden}}}
	if got := Scoped(ev, scope).Payload.(OverlapPayload); len(got.With) != 0 {
		t.Fatalf("scoped overlap of a hidden peer = %+v, want an empty set", got.With)
	}
}
