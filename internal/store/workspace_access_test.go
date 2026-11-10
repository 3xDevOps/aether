package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// The upgrade keeps every approved member on every workspace they reached,
// which was all of them, and hands the server to its earliest admin.
func TestWorkspaceAccessMigrationKeepsTodaysAccess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, len(migrations)-1)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, pending, created_at)
			VALUES ('creator', 'Ada', ?, '#e6194b', 'admin', 0, 1),
			       ('admin2', 'Bo', ?, '#3cb44b', 'admin', 0, 2),
			       ('user', 'Cam', ?, '#ffe119', 'collaborator', 0, 3),
			       ('watcher', 'Dee', ?, '#4363d8', 'viewer', 0, 4),
			       ('waiting', 'Eve', ?, '#f58231', 'collaborator', 1, 5),
			       ('gone', 'Fay', ?, '#911eb4', 'admin', 0, 0);
		UPDATE members SET merged_into = 'creator' WHERE id = 'gone';
		INSERT INTO workspaces (id, name, environment, base_branch, steer_others, origin, created_at)
			VALUES ('w1', 'one', '{}', 'main', '', '', 1),
			       ('w2', 'two', '{}', 'main', '', '', 2);
	`, testKey(t, "creator"), testKey(t, "admin2"), testKey(t, "user"), testKey(t, "watcher"),
		testKey(t, "waiting"), testKey(t, "gone")); err != nil {
		_ = raw.Close()
		t.Fatalf("seed legacy members: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	roles := map[domain.MemberID]domain.Role{
		"creator": domain.RoleSuperAdmin, "admin2": domain.RoleAdmin,
		"user": domain.RoleCollaborator, "watcher": domain.RoleViewer, "waiting": domain.RoleCollaborator,
	}
	for id, want := range roles {
		m, err := db.GetMember(ctx, id)
		if err != nil {
			t.Fatalf("GetMember %s: %v", id, err)
		}
		if m.Role != want {
			t.Fatalf("%s role = %s, want %s", id, m.Role, want)
		}
		wantGrants := 2
		if id == "creator" || id == "waiting" {
			wantGrants = 0
		}
		if len(m.Access.Granted) != wantGrants {
			t.Fatalf("%s grants = %v, want %d", id, m.Access.Granted, wantGrants)
		}
	}
	for _, id := range []domain.WorkspaceID{"w1", "w2"} {
		w, err := db.GetWorkspace(ctx, id)
		if err != nil {
			t.Fatalf("GetWorkspace %s: %v", id, err)
		}
		if w.OwnerID != "creator" {
			t.Fatalf("%s owner = %q, want creator", id, w.OwnerID)
		}
	}
	creator, err := db.GetMember(ctx, "creator")
	if err != nil {
		t.Fatalf("GetMember creator: %v", err)
	}
	if !creator.Access.Owned["w1"] || !creator.Access.Owned["w2"] {
		t.Fatalf("creator owns %v, want w1 and w2", creator.Access.Owned)
	}
}

func TestWorkspaceGrants(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	owner := &domain.Member{DisplayName: "Ada", PublicKey: testKey(t, "ada"), Color: "#e6194b", Role: domain.RoleAdmin}
	user := &domain.Member{DisplayName: "Cam", PublicKey: testKey(t, "cam"), Color: "#3cb44b", Role: domain.RoleCollaborator}
	for _, m := range []*domain.Member{owner, user} {
		if err := db.CreateMember(ctx, m); err != nil {
			t.Fatalf("CreateMember: %v", err)
		}
	}
	w := &domain.Workspace{Name: "web", OwnerID: owner.ID}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	for range 2 {
		if err := db.GrantWorkspace(ctx, w.ID, user.ID, owner.ID); err != nil {
			t.Fatalf("GrantWorkspace: %v", err)
		}
	}
	grants, err := db.ListWorkspaceGrants(ctx, w.ID)
	if err != nil || len(grants) != 1 || grants[0].MemberID != user.ID || grants[0].GrantedBy != owner.ID {
		t.Fatalf("ListWorkspaceGrants = %+v, %v; want one grant to %s by %s", grants, err, user.ID, owner.ID)
	}
	got, err := db.GetMember(ctx, user.ID)
	if err != nil || !got.Access.Granted[w.ID] || len(got.Access.Owned) != 0 {
		t.Fatalf("user access = %+v, %v; want granted %s", got.Access, err, w.ID)
	}
	if err := db.GrantWorkspace(ctx, "ws_missing", user.ID, owner.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("grant on a missing workspace = %v, want ErrNotFound", err)
	}
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, user.ID); err != nil {
		t.Fatalf("RevokeWorkspaceGrant: %v", err)
	}
	if err := db.RevokeWorkspaceGrant(ctx, w.ID, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v, want ErrNotFound", err)
	}
	if err := db.SetWorkspaceOwner(ctx, w.ID, user.ID); err != nil {
		t.Fatalf("SetWorkspaceOwner: %v", err)
	}
	if ws, err := db.GetWorkspace(ctx, w.ID); err != nil || ws.OwnerID != user.ID {
		t.Fatalf("owner after set = %+v, %v", ws, err)
	}
}
