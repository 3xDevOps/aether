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
	defer db.Close()
	workspace := &domain.Workspace{Name: "workspace", Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	member := &domain.Member{DisplayName: "member", TailnetLogin: "member@example.com", Role: domain.RoleCollaborator}
	if err := db.CreateMember(ctx, member); err != nil {
		t.Fatal(err)
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
	if err := db.CreatePendingWorkspaceShell(ctx, pending); err != nil {
		t.Fatal(err)
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
		t.Fatalf("pending sessions remain after reset: %d", len(remaining))
	}
}
