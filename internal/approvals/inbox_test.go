package approvals

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

const testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIF3jVX1WCbXCEjHVFVBExpFvhOsSJfLNJDDXCM4Q3xJd test"

// A consumer that falls behind recovers the gap by replaying the stream,
// so the inbox sees the same pauses a second time. A request is identified
// by its run and the pause's own tool-use id, so the replay raises nothing
// new: one row per pause, however often the pause is delivered.
func TestReplayedPausesRaiseOneRequestEach(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	defer func() { _ = bus.Close() }()

	m := &domain.Member{DisplayName: "Ada", PublicKey: testPublicKey, Color: "#e6194b", Role: domain.RoleCollaborator}
	if err = db.CreateMember(ctx, m); err != nil {
		t.Fatalf("create member: %v", err)
	}
	ws := &domain.Workspace{Name: "proj", Environment: domain.WorkspaceEnvironment{CustomImage: "img"}}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	sess := &domain.Session{WorkspaceID: ws.ID, Name: "effort", BaseBranch: "main"}
	if err = db.CreateSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	run := &domain.Run{
		SessionID: sess.ID, MemberID: m.ID, Task: "plan the thing",
		Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if err = db.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	svc, err := New(Config{Store: db, Bus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if serr := svc.Start(ctx); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	defer func() { _ = svc.Close() }()

	announced, err := bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeApproval}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = announced.Close() }()

	pauses := []events.AgentEventPayload{
		{Kind: events.AgentPause, Tool: "ExitPlanMode", ToolUseID: "toolu_1", Detail: "1. do the thing"},
		{Kind: events.AgentPause, Tool: "ExitPlanMode", ToolUseID: "toolu_2", Detail: "1. do the other thing"},
	}
	publish := func() {
		t.Helper()
		for _, p := range pauses {
			if _, perr := bus.Publish(ctx, events.Event{
				SessionID: sess.ID, RunID: run.ID, ActorID: m.ID, Payload: p,
			}); perr != nil {
				t.Fatalf("publish pause: %v", perr)
			}
		}
	}
	// The second round is the replay: the same pause events, delivered again.
	publish()
	publish()

	seen := map[string]int{}
	deadline := time.After(5 * time.Second)
	for total := 0; total < len(pauses); {
		select {
		case ev := <-announced.Events():
			p := ev.Payload.(events.ApprovalPayload)
			if seen[p.RequestID]++; seen[p.RequestID] == 1 {
				total++
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the inbox to raise both pauses; saw %v", seen)
		}
	}

	// Delivery is ordered, so both replayed pauses are handled by the time
	// this one is: it is the marker that the replay is fully drained.
	if _, err = bus.Publish(ctx, events.Event{
		SessionID: sess.ID, RunID: run.ID, ActorID: m.ID,
		Payload: events.AgentEventPayload{
			Kind: events.AgentPause, Tool: "ExitPlanMode", ToolUseID: "toolu_3", Detail: "1. and one more",
		},
	}); err != nil {
		t.Fatalf("publish marker pause: %v", err)
	}
	for len(seen) < len(pauses)+1 {
		select {
		case ev := <-announced.Events():
			seen[ev.Payload.(events.ApprovalPayload).RequestID]++
		case <-deadline:
			t.Fatalf("timed out waiting for the marker pause; saw %v", seen)
		}
	}

	list, err := svc.List(ctx, sess.ID, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != len(pauses)+1 {
		t.Fatalf("inbox holds %d requests, want %d: the replay duplicated them\n%+v",
			len(list), len(pauses)+1, list)
	}
}
