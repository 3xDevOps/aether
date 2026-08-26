package sshd

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/toolenv"
)

func TestWorkspaceToolsResetUsesToolManager(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	workspace := &domain.Workspace{Name: "workspace", Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	if createWorkspaceErr := db.CreateWorkspace(ctx, workspace); createWorkspaceErr != nil {
		t.Fatal(createWorkspaceErr)
	}
	member := &domain.Member{DisplayName: "member", TailnetLogin: "member@example.com", Role: domain.RoleCollaborator}
	if createMemberErr := db.CreateMember(ctx, member); createMemberErr != nil {
		t.Fatal(createMemberErr)
	}
	manager, err := toolenv.NewManager(filepath.Join(t.TempDir(), "tools"), db)
	if err != nil {
		t.Fatal(err)
	}
	staging, err := manager.CreateStaging(string(member.ID), string(workspace.ID))
	if err != nil {
		t.Fatal(err)
	}
	pending := &store.PendingWorkspaceShell{WorkspaceID: workspace.ID, MemberID: member.ID, StagingID: filepath.Base(staging)}
	if createPendingErr := db.CreatePendingWorkspaceShell(ctx, pending); createPendingErr != nil {
		t.Fatal(createPendingErr)
	}
	server := &Server{cfg: Config{Store: db, Toolenv: manager}}
	raw, err := json.Marshal(protocol.ToolSnapshotResetParams{
		Workspace: protocol.WorkspaceSelector{ID: string(workspace.ID)},
		Confirm:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := server.workspaceToolsReset(ctx, member.ID, raw); rpcErr != nil {
		t.Fatalf("workspaceToolsReset: %+v", rpcErr)
	}
	remaining, err := db.ListPendingWorkspaceShells(ctx, member.ID, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("pending workspace shells remain after reset: %d", len(remaining))
	}
}
