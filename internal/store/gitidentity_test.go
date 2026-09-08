package store

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestMemberGitIdentityRoundTrip covers the v22 columns: they persist,
// they are independent of the display name, and clearing them restores
// the fallback identity.
func TestMemberGitIdentityRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	m := mustCreateMember(t, db)

	if got := m.GitIdentity(); got.Name != "Ada" || got.Email != string(m.ID)+"@aether.local" {
		t.Fatalf("fallback identity = %v, want the display name and the aether.local address", got)
	}
	if err := db.UpdateMemberGitIdentity(ctx, m.ID, "Ada Lovelace", "ada@example.com"); err != nil {
		t.Fatalf("UpdateMemberGitIdentity: %v", err)
	}
	got, err := db.GetMember(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.GitName != "Ada Lovelace" || got.GitEmail != "ada@example.com" || got.DisplayName != "Ada" {
		t.Fatalf("member after set = %+v", got)
	}

	// UpdateMember writes the other columns and must leave these alone.
	got.DisplayName = "Ada L"
	if uerr := db.UpdateMember(ctx, got); uerr != nil {
		t.Fatalf("UpdateMember: %v", uerr)
	}
	if again, gerr := db.GetMember(ctx, m.ID); gerr != nil || again.GitEmail != "ada@example.com" {
		t.Fatalf("git identity after UpdateMember = %+v, %v", again, gerr)
	}

	if cerr := db.UpdateMemberGitIdentity(ctx, m.ID, "", ""); cerr != nil {
		t.Fatalf("UpdateMemberGitIdentity clear: %v", cerr)
	}
	cleared, err := db.GetMember(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMember after clear: %v", err)
	}
	if id := cleared.GitIdentity(); id.Name != "Ada L" || id.Email != string(m.ID)+"@aether.local" {
		t.Fatalf("identity after clear = %v, want the fallback back", id)
	}
	if nerr := db.UpdateMemberGitIdentity(ctx, "nobody", "X", "x@example.com"); !errors.Is(nerr, ErrNotFound) {
		t.Fatalf("UpdateMemberGitIdentity for an unknown member = %v, want ErrNotFound", nerr)
	}
}

// TestRunSteerers covers the v22 table: a member is added once however
// often they steer, the set reads back with their git identity, and it
// goes away with the run.
func TestRunSteerers(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	owner := mustCreateMember(t, db)
	bob := &domain.Member{
		DisplayName: "Bob", PublicKey: testKey(t, "bob@laptop"),
		Color: "#3cb44b", Role: domain.RoleCollaborator,
		GitName: "Bob Steer", GitEmail: "bob@example.com",
	}
	if err := db.CreateMember(ctx, bob); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	run := mustCreateRun(t, db, w.ID, owner.ID, domain.RunRunning)

	added, err := db.AddRunSteerer(ctx, run.ID, bob.ID)
	if err != nil || !added {
		t.Fatalf("AddRunSteerer = (%v, %v), want (true, nil)", added, err)
	}
	again, err := db.AddRunSteerer(ctx, run.ID, bob.ID)
	if err != nil || again {
		t.Fatalf("second AddRunSteerer = (%v, %v), want (false, nil)", again, err)
	}
	steerers, err := db.ListRunSteerers(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunSteerers: %v", err)
	}
	if len(steerers) != 1 || steerers[0].GitIdentity().Trailer() != "Co-authored-by: Bob Steer <bob@example.com>" {
		t.Fatalf("steerers = %+v", steerers)
	}

	if derr := db.DeleteRun(ctx, run.ID); derr != nil {
		t.Fatalf("DeleteRun: %v", derr)
	}
	left, err := db.ListRunSteerers(ctx, run.ID)
	if err != nil || len(left) != 0 {
		t.Fatalf("steerers after DeleteRun = (%+v, %v), want none", left, err)
	}
}
