package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestAccountSharesAreDirectionalAndCascade(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestRunHomeMemberIsTheLauncherAndSurvivesHandoff(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	launcher := mustCreateMember(t, db)
	owner := mustCreateMember(t, db)
	recipient := mustCreateMember(t, db)
	run := &domain.Run{
		WorkspaceID: workspace.ID, MemberID: launcher.ID, AccountMemberID: owner.ID,
		Task: "shared login", Harness: "claude", Mode: domain.LaunchTUI,
		Status: domain.RunRunning,
	}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := db.TransferRun(ctx, run.ID, recipient.ID); err != nil {
		t.Fatalf("TransferRun: %v", err)
	}
	if err := db.TransferRunWithHandoff(ctx, &HandoffOutbox{
		ID: "handoff-home", WorkspaceID: workspace.ID, RunID: run.ID,
		ActorID: recipient.ID, FromMemberID: recipient.ID, ToMemberID: owner.ID,
	}); err != nil {
		t.Fatalf("TransferRunWithHandoff: %v", err)
	}
	got, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	got.HomeMemberID = recipient.ID
	if err = db.UpdateRun(ctx, got); err != nil {
		t.Fatalf("UpdateRun: %v", err)
	}
	if got, err = db.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.MemberID != owner.ID || got.HomeMemberID != launcher.ID || got.HomeMember() != launcher.ID {
		t.Fatalf("after handoffs owner/home = %s/%s (%s), want %s/%s", got.MemberID, got.HomeMemberID, got.HomeMember(), owner.ID, launcher.ID)
	}
	if err = db.DeleteMember(ctx, launcher.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("DeleteMember of a run's home member: %v, want ErrInUse", err)
	}
}

func TestRunHomeMemberMigrationLeavesOldRowsOnTheAccountHome(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, len(migrations)-1)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('owner', 'Ada', ?, '#e6194b', 'admin', 1),
			       ('launcher', 'Grace', ?, '#3cb44b', 'collaborator', 1);
		INSERT INTO workspaces (id, name, environment, base_branch, steer_others, origin, created_at)
			VALUES ('w1', 'legacy', '{}', 'main', '', '', 1);
		INSERT INTO runs (id, workspace_id, member_id, account_member_id, task, harness, mode, status,
		                  reason, branch, worktree, protected, created_at)
			VALUES ('shared', 'w1', 'launcher', 'owner', 'legacy', 'claude', 'tui', 'running', '', 'b1', '', 0, 1),
			       ('own', 'w1', 'launcher', 'launcher', 'legacy', 'claude', 'tui', 'running', '', 'b2', '', 0, 1);
	`, testKey(t, "owner"), testKey(t, "launcher")); err != nil {
		_ = raw.Close()
		t.Fatalf("seed legacy runs: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	for id, home := range map[domain.RunID]domain.MemberID{"shared": "owner", "own": "launcher"} {
		got, err := db.GetRun(context.Background(), id)
		if err != nil {
			t.Fatalf("GetRun %s: %v", id, err)
		}
		if got.HomeMemberID != "" || got.HomeMember() != got.AccountMember() || got.HomeMember() != home {
			t.Fatalf("migrated run %s home = %q (%s), want empty falling back to %s", id, got.HomeMemberID, got.HomeMember(), home)
		}
	}
}
