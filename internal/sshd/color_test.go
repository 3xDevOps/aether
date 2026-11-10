package sshd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestMemberColor(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(c *Config) { c.InvitesDir = filepath.Join(t.TempDir(), "invites") })
	bobSigner, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	bobC := controlAs(t, e, bobSigner)
	adminC := controlClient(t, e)

	// Self recolor: allowed, normalized to lowercase with #.
	var res protocol.MemberColorResult
	if err := bobC.Call(protocol.MethodMemberColor, protocol.MemberColorParams{Color: "F58231"}, &res); err != nil {
		t.Fatalf("self member.color: %v", err)
	}
	if res.Member.ID != string(bob.ID) || res.Member.Color != "#f58231" {
		t.Errorf("self recolor = %+v, want id %s color #f58231", res.Member, bob.ID)
	}
	got, err := e.store.GetMember(context.Background(), bob.ID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if got.Color != "#f58231" {
		t.Errorf("persisted color = %q, want #f58231", got.Color)
	}

	// Non-admin recoloring someone else: denied.
	var pe *protocol.Error
	err = bobC.Call(protocol.MethodMemberColor, protocol.MemberColorParams{
		MemberID: string(e.member.ID), Color: "#911eb4",
	}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("non-admin recolor other = %v, want CodeDenied", err)
	}

	// Admin recoloring another member: allowed.
	if aerr := adminC.Call(protocol.MethodMemberColor, protocol.MemberColorParams{
		MemberID: string(bob.ID), Color: "#911EB4",
	}, &res); aerr != nil {
		t.Fatalf("admin member.color: %v", aerr)
	}
	if res.Member.ID != string(bob.ID) || res.Member.Color != "#911eb4" {
		t.Errorf("admin recolor = %+v, want id %s color #911eb4", res.Member, bob.ID)
	}

	// Bad hex: rejected with invalid params.
	err = bobC.Call(protocol.MethodMemberColor, protocol.MemberColorParams{Color: "red"}, nil)
	if !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("bad hex = %v, want CodeInvalidParams", err)
	}

	// Explicit self by ID does not require admin.
	if err := bobC.Call(protocol.MethodMemberColor, protocol.MemberColorParams{
		MemberID: string(bob.ID), Color: "#46f0f0",
	}, &res); err != nil {
		t.Fatalf("explicit self member.color: %v", err)
	}
	if res.Member.Color != "#46f0f0" {
		t.Errorf("explicit self recolor = %q, want #46f0f0", res.Member.Color)
	}
}

// An admin edits the profile of a member below admin, never a super
// admin's: their name, color and commit identity are theirs.
func TestAdminCannotEditASuperAdminsProfile(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	_, super := addMember(t, e, "Sam", domain.RoleSuperAdmin, false)
	adminC := controlClient(t, e)
	id := string(super.ID)
	wantDenied(t, adminC.Call(protocol.MethodMemberRename, protocol.MemberRenameParams{MemberID: id, DisplayName: "Mallory"}, nil), "admin member.rename of a super admin")
	wantDenied(t, adminC.Call(protocol.MethodMemberColor, protocol.MemberColorParams{MemberID: id, Color: "#911eb4"}, nil), "admin member.color of a super admin")
	wantDenied(t, adminC.Call(protocol.MethodMemberGit, protocol.MemberGitParams{MemberID: id, Name: "Mallory", Email: "mallory@example.com"}, nil), "admin member.git of a super admin")
	got, err := e.store.GetMember(context.Background(), super.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Sam" || got.Color != "#3cb44b" || got.GitName != "" || got.GitEmail != "" {
		t.Fatalf("super admin after refused edits = %+v, want unchanged", got)
	}
}
