package sshd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestMemberRename(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	bobSigner, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bobC := controlAs(t, e, bobSigner)
	adminC := controlClient(t, e)
	sub, err := e.bus.Subscribe(context.Background(), events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeMemberChanged}},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close() //nolint:errcheck

	var res protocol.MemberRenameResult
	if err := bobC.Call(protocol.MethodMemberRename, protocol.MemberRenameParams{DisplayName: "  Robert  "}, &res); err != nil {
		t.Fatalf("self member.rename: %v", err)
	}
	if res.Member.ID != string(bob.ID) || res.Member.DisplayName != "Robert" {
		t.Fatalf("self rename = %+v, want %s trimmed to Robert", res.Member, bob.ID)
	}
	if got, gerr := e.store.GetMember(context.Background(), bob.ID); gerr != nil || got.DisplayName != "Robert" {
		t.Fatalf("persisted member = %+v, %v; want Robert", got, gerr)
	}
	select {
	case ev := <-sub.Events():
		p, ok := ev.Payload.(events.MemberChangedPayload)
		if !ok || p.MemberID != bob.ID || p.DisplayName != "Robert" || ev.ActorID != bob.ID || ev.WorkspaceID != e.ws.ID {
			t.Fatalf("member.changed = %+v %+v, want Robert by Bob in %s", ev, ev.Payload, e.ws.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no member.changed event")
	}

	var pe *protocol.Error
	for _, bad := range []string{"   ", strings.Repeat("x", maxDisplayNameRunes+1), "Bob\nby"} {
		if err := bobC.Call(protocol.MethodMemberRename, protocol.MemberRenameParams{DisplayName: bad}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
			t.Fatalf("rename to %q = %v, want CodeInvalidParams", bad, err)
		}
	}
	if err := bobC.Call(protocol.MethodMemberRename, protocol.MemberRenameParams{MemberID: string(e.member.ID), DisplayName: "Mallory"}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("non-admin rename of another member = %v, want CodeDenied", err)
	}
	if err := adminC.Call(protocol.MethodMemberRename, protocol.MemberRenameParams{MemberID: string(bob.ID), DisplayName: "Bobby"}, &res); err != nil || res.Member.DisplayName != "Bobby" {
		t.Fatalf("admin rename = %+v, %v; want Bobby", res.Member, err)
	}
}
