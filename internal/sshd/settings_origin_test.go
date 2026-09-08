package sshd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// workspace.origin is the push capability, not workspace administration: a
// collaborator records the upstream a run pushes to, a viewer cannot, an
// unusable URL never reaches the store, and every change lands on the
// workspace timeline.
func TestWorkspaceOrigin(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	collab, _ := addMember(t, e, "Cody", domain.RoleCollaborator, false)
	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)

	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeTimeline}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	wantDenied(t, controlAs(t, e, viewer).Call(protocol.MethodWorkspaceOrigin, protocol.WorkspaceOriginParams{
		WorkspaceID: string(e.ws.ID), Origin: "https://github.com/acme/app.git",
	}, nil), "viewer workspace.origin")

	cc := controlAs(t, e, collab)
	const origin = "https://github.com/acme/app.git"
	var res protocol.WorkspaceOriginResult
	if err = cc.Call(protocol.MethodWorkspaceOrigin, protocol.WorkspaceOriginParams{
		WorkspaceID: string(e.ws.ID), Origin: origin,
	}, &res); err != nil {
		t.Fatalf("collaborator workspace.origin: %v", err)
	}
	if res.Workspace.Origin != origin {
		t.Fatalf("result origin = %q, want %q", res.Workspace.Origin, origin)
	}
	if note := waitTimelineNote(t, sub); note != "workspace origin set to "+origin {
		t.Fatalf("timeline note = %q", note)
	}

	// An option-shaped URL is rejected before the store sees it.
	var pe *protocol.Error
	err = cc.Call(protocol.MethodWorkspaceOrigin, protocol.WorkspaceOriginParams{
		WorkspaceID: string(e.ws.ID), Origin: "--upload-pack=/bin/sh",
	}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("invalid origin = %v, want CodeInvalidParams", err)
	}
	stored, err := e.store.GetWorkspace(ctx, e.ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if stored.Origin != origin {
		t.Fatalf("stored origin = %q, want the rejected call to have changed nothing", stored.Origin)
	}

	var cleared protocol.WorkspaceOriginResult
	if err := cc.Call(protocol.MethodWorkspaceOrigin, protocol.WorkspaceOriginParams{
		WorkspaceID: string(e.ws.ID), Origin: "",
	}, &cleared); err != nil {
		t.Fatalf("clear workspace.origin: %v", err)
	}
	if cleared.Workspace.Origin != "" {
		t.Fatalf("result origin after clear = %q, want empty", cleared.Workspace.Origin)
	}
	if note := waitTimelineNote(t, sub); note != "workspace origin cleared" {
		t.Fatalf("clear timeline note = %q", note)
	}
}

func waitTimelineNote(t *testing.T, sub events.Subscription) string {
	t.Helper()
	for {
		select {
		case ev := <-sub.Events():
			p, ok := ev.Payload.(events.TimelinePayload)
			if ok && p.Kind == events.TimelineNote {
				return p.Message
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no timeline note published")
			return ""
		}
	}
}
