package cost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// Admit checks the launch in the workspace it admits into: a member who
// cannot reach it is refused as if it did not exist.
func TestAdmitNeedsTheWorkspace(t *testing.T) {
	ctx := context.Background()
	db, err := storetest.Open(filepath.Join(t.TempDir(), "cost.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("NewInProc: %v", err)
	}
	defer func() { _ = bus.Close() }()
	svc, err := New(Config{Store: db, Bus: bus})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ws := &domain.Workspace{Name: "one"}
	if err = db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	member := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example", Role: domain.RoleCollaborator}
	if err = db.CreateMember(ctx, member); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	if err = svc.Admit(ctx, ws.ID, member.ID); !errors.Is(err, permissions.ErrNoAccess) {
		t.Fatalf("Admit without a grant = %v, want ErrNoAccess", err)
	}
	if err = db.GrantWorkspace(ctx, ws.ID, member.ID, "", true); err != nil {
		t.Fatalf("GrantWorkspace: %v", err)
	}
	if err = svc.Admit(ctx, ws.ID, member.ID); err != nil {
		t.Fatalf("Admit with a grant = %v, want admitted", err)
	}
}
