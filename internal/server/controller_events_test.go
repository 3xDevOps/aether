package server

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func TestControlLeaseChangesPublishRunController(t *testing.T) {
	t.Parallel()
	s, _, member, ws := newWorkspaceDeletionServer(t)
	ctx := t.Context()
	run := &domain.Run{WorkspaceID: ws.ID, MemberID: member.ID, Task: "work", Harness: "fake", Mode: domain.LaunchHeadless, Status: domain.RunRunning}
	if err := s.db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	sub, err := s.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunController}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close() //nolint:errcheck
	next := func() events.RunControllerPayload {
		t.Helper()
		select {
		case ev := <-sub.Events():
			if ev.RunID != run.ID || ev.WorkspaceID != ws.ID {
				t.Fatalf("event %+v", ev)
			}
			return ev.Payload.(events.RunControllerPayload)
		case <-time.After(5 * time.Second):
			t.Fatal("no run.controller event")
		}
		return events.RunControllerPayload{}
	}

	lease, _, err := s.control.Acquire(string(run.ID), "mem-bob", "bob-tab", false)
	if err != nil {
		t.Fatal(err)
	}
	if p := next(); p.MemberID != "mem-bob" || p.LastMemberID != "mem-bob" {
		t.Fatalf("after acquire %+v", p)
	}
	if err := s.control.Release(string(run.ID), "mem-bob", lease.SessionID, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if p := next(); p.MemberID != "" || p.LastMemberID != "mem-bob" {
		t.Fatalf("after release %+v", p)
	}
}
