package sshd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// A non-admin cannot change anyone's role, their own included.
func TestMemberRoleRequiresAdmin(t *testing.T) {
	e := newTestEnv(t, nil)
	bobSigner, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bobC := controlAs(t, e, bobSigner)

	wantDenied(t, bobC.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
		MemberID: string(bob.ID), Role: string(domain.RoleAdmin),
	}, nil), "collaborator self-promotion")

	got, err := e.store.GetMember(context.Background(), bob.ID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if got.Role != domain.RoleCollaborator {
		t.Errorf("role after denied call = %q, want collaborator", got.Role)
	}
}

// An admin promotes a collaborator to admin and demotes them to viewer;
// both land in the store and come back on the wire.
func TestMemberRolePromoteAndDemote(t *testing.T) {
	e := newTestEnv(t, nil)
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	adminC := controlClient(t, e)
	ctx := context.Background()

	var res protocol.MemberRoleResult
	if err := adminC.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
		MemberID: string(bob.ID), Role: string(domain.RoleAdmin),
	}, &res); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if res.Member.ID != string(bob.ID) || res.Member.Role != string(domain.RoleAdmin) {
		t.Errorf("promote result = %+v, want %s admin", res.Member, bob.ID)
	}
	got, err := e.store.GetMember(ctx, bob.ID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if got.Role != domain.RoleAdmin {
		t.Errorf("persisted role = %q, want admin", got.Role)
	}

	// Bob is an admin now, so demoting him leaves one admin behind and is
	// allowed.
	if cerr := adminC.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
		MemberID: string(bob.ID), Role: string(domain.RoleViewer),
	}, &res); cerr != nil {
		t.Fatalf("demote: %v", cerr)
	}
	if res.Member.Role != string(domain.RoleViewer) {
		t.Errorf("demote result role = %q, want viewer", res.Member.Role)
	}
	got, err = e.store.GetMember(ctx, bob.ID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if got.Role != domain.RoleViewer {
		t.Errorf("persisted role = %q, want viewer", got.Role)
	}
}

// Setting a member to the role they already hold succeeds unchanged.
func TestMemberRoleIdempotent(t *testing.T) {
	e := newTestEnv(t, nil)
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	adminC := controlClient(t, e)

	var res protocol.MemberRoleResult
	if err := adminC.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
		MemberID: string(bob.ID), Role: string(domain.RoleCollaborator),
	}, &res); err != nil {
		t.Fatalf("no-op role set: %v", err)
	}
	if res.Member.ID != string(bob.ID) || res.Member.Role != string(domain.RoleCollaborator) {
		t.Errorf("no-op result = %+v, want %s collaborator", res.Member, bob.ID)
	}
}

// The deployment must keep one member able to administer it.
func TestMemberRoleRefusesDemotingLastAdmin(t *testing.T) {
	e := newTestEnv(t, nil)
	adminC := controlClient(t, e)

	var pe *protocol.Error
	err := adminC.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
		MemberID: string(e.member.ID), Role: string(domain.RoleViewer),
	}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("demote last admin = %v, want CodeDenied", err)
	}
	if !strings.Contains(pe.Message, "last admin") {
		t.Errorf("message %q does not mention last admin", pe.Message)
	}
	got, gerr := e.store.GetMember(context.Background(), e.member.ID)
	if gerr != nil {
		t.Fatalf("get member: %v", gerr)
	}
	if got.Role != domain.RoleAdmin {
		t.Errorf("role after refused demotion = %q, want admin", got.Role)
	}
}

// A pending member's role may be set: approval and role are orthogonal.
func TestMemberRolePendingMember(t *testing.T) {
	e := newTestEnv(t, nil)
	_, pending := addMember(t, e, "Pat", domain.RoleViewer, true)
	adminC := controlClient(t, e)

	var res protocol.MemberRoleResult
	if err := adminC.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
		MemberID: string(pending.ID), Role: string(domain.RoleCollaborator),
	}, &res); err != nil {
		t.Fatalf("role of pending member: %v", err)
	}
	if res.Member.Role != string(domain.RoleCollaborator) || !res.Member.Pending {
		t.Errorf("result = %+v, want a still-pending collaborator", res.Member)
	}
}

// Bad input is rejected before anything is written.
func TestMemberRoleRejectsBadInput(t *testing.T) {
	e := newTestEnv(t, nil)
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	adminC := controlClient(t, e)

	cases := []struct {
		name   string
		params protocol.MemberRoleParams
		code   int
	}{
		{"missing member_id", protocol.MemberRoleParams{Role: string(domain.RoleViewer)}, protocol.CodeInvalidParams},
		{"missing role", protocol.MemberRoleParams{MemberID: string(bob.ID)}, protocol.CodeInvalidParams},
		{"unknown role", protocol.MemberRoleParams{MemberID: string(bob.ID), Role: "superuser"}, protocol.CodeInvalidParams},
		{"unknown member", protocol.MemberRoleParams{MemberID: "nobody", Role: string(domain.RoleViewer)}, protocol.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pe *protocol.Error
			err := adminC.Call(protocol.MethodMemberRole, tc.params, nil)
			if !errors.As(err, &pe) || pe.Code != tc.code {
				t.Fatalf("%s = %v, want code %d", tc.name, err, tc.code)
			}
		})
	}
}

// Two admins demoting each other at the same instant must not both win.
// The guard reads the member table and then writes to it, so without
// serialization both calls count two admins, both proceed, and the
// deployment is left with none and no way to administer it again.
func TestConcurrentDemotionsKeepAnAdmin(t *testing.T) {
	e := newTestEnv(t, nil)
	// e.member is the seeded admin; Bea is the second one.
	beaSigner, bea := addMember(t, e, "Bea", domain.RoleAdmin, false)
	adminC := controlClient(t, e)
	beaC := controlAs(t, e, beaSigner)

	// Each admin demotes the other, as simultaneously as we can arrange.
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	demote := func(c *protocol.Client, target domain.MemberID) {
		<-start
		_ = c.Call(protocol.MethodMemberRole, protocol.MemberRoleParams{
			MemberID: string(target), Role: string(domain.RoleCollaborator),
		}, nil)
		done <- struct{}{}
	}
	go demote(adminC, bea.ID)
	go demote(beaC, e.member.ID)
	close(start)
	<-done
	<-done

	members, err := e.store.ListMembers(context.Background())
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	admins := 0
	for _, m := range members {
		if m.Role == domain.RoleAdmin {
			admins++
		}
	}
	if admins == 0 {
		t.Fatal("both demotions succeeded: the deployment has no admin left")
	}
}
