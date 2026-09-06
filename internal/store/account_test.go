package store

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestAccountSharesAreDirectionalAndCascade(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	owner := mustCreateMember(t, db)
	grantee := &domain.Member{
		DisplayName: "Grace", PublicKey: testKey(t, "grace"),
		Color: "#3cb44b", Role: domain.RoleCollaborator,
	}
	if err := db.CreateMember(ctx, grantee); err != nil {
		t.Fatal(err)
	}

	if err := db.ShareAccount(ctx, owner.ID, grantee.ID); err != nil {
		t.Fatalf("ShareAccount: %v", err)
	}
	if err := db.ShareAccount(ctx, owner.ID, grantee.ID); err != nil {
		t.Fatalf("idempotent ShareAccount: %v", err)
	}
	shared, err := db.AccountSharedWith(ctx, owner.ID, grantee.ID)
	if err != nil || !shared {
		t.Fatalf("AccountSharedWith = %v, %v, want true", shared, err)
	}
	reverse, err := db.AccountSharedWith(ctx, grantee.ID, owner.ID)
	if err != nil || reverse {
		t.Fatalf("reverse AccountSharedWith = %v, %v, want false", reverse, err)
	}
	owners, err := db.ListAccountOwners(ctx, grantee.ID)
	if err != nil || len(owners) != 1 || owners[0].ID != owner.ID {
		t.Fatalf("ListAccountOwners = %+v, %v", owners, err)
	}
	grantees, err := db.ListAccountGrantees(ctx, owner.ID)
	if err != nil || len(grantees) != 1 || grantees[0].ID != grantee.ID {
		t.Fatalf("ListAccountGrantees = %+v, %v", grantees, err)
	}

	if err = db.DeleteMember(ctx, grantee.ID); err != nil {
		t.Fatalf("DeleteMember: %v", err)
	}
	shared, err = db.AccountSharedWith(ctx, owner.ID, grantee.ID)
	if err != nil || shared {
		t.Fatalf("share after member delete = %v, %v, want false", shared, err)
	}
}

func TestSharedRunKeepsAccountMemberInUse(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	actor := mustCreateMember(t, db)
	account := mustCreateMember(t, db)
	run := &domain.Run{
		WorkspaceID:     workspace.ID,
		MemberID:        actor.ID,
		AccountMemberID: account.ID,
		Mode:            domain.LaunchTUI,
		Status:          domain.RunQueued,
	}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := db.DeleteMember(ctx, account.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteMember account with dependent run: %v, want ErrInUse", err)
	}
}

func TestRunAccountMemberRoundTrips(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	actor := mustCreateMember(t, db)
	account := &domain.Member{
		DisplayName: "Account owner", PublicKey: testKey(t, "owner"),
		Color: "#4363d8", Role: domain.RoleCollaborator,
	}
	if err := db.CreateMember(ctx, account); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{
		WorkspaceID: workspace.ID, MemberID: actor.ID, AccountMemberID: account.ID,
		Task: "shared quota", Harness: "codex", Mode: domain.LaunchTUI,
		Status: domain.RunQueued,
	}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.MemberID != actor.ID || got.AccountMember() != account.ID {
		t.Fatalf("run actor/account = %s/%s, want %s/%s", got.MemberID, got.AccountMember(), actor.ID, account.ID)
	}
}
