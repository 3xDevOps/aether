package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestWorkspaceOriginRoundTrip pins the origin through create, the narrow
// mutator, and a full update: it survives, it clears, and setting it
// touches nothing else.
func TestWorkspaceOriginRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	w := &domain.Workspace{
		Name:        "app",
		BaseBranch:  "main",
		Origin:      "https://github.com/acme/app.git",
		Environment: domain.WorkspaceEnvironment{},
	}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	got, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.Origin != w.Origin {
		t.Fatalf("origin = %q, want %q", got.Origin, w.Origin)
	}

	const next = "git@github.com:acme/app.git"
	if err = db.SetWorkspaceOrigin(ctx, w.ID, next); err != nil {
		t.Fatalf("SetWorkspaceOrigin: %v", err)
	}
	got, err = db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after set: %v", err)
	}
	if got.Origin != next {
		t.Fatalf("origin = %q, want %q", got.Origin, next)
	}
	if got.Name != "app" || got.BaseBranch != "main" || !got.CreatedAt.Equal(w.CreatedAt) {
		t.Fatalf("SetWorkspaceOrigin clobbered fields: %+v", got)
	}

	if err = db.SetWorkspaceOrigin(ctx, w.ID, ""); err != nil {
		t.Fatalf("SetWorkspaceOrigin clear: %v", err)
	}
	if got, err = db.GetWorkspace(ctx, w.ID); err != nil || got.Origin != "" {
		t.Fatalf("origin after clear = %q (%v), want empty", got.Origin, err)
	}

	if err = db.SetWorkspaceOrigin(ctx, w.ID, "--upload-pack=/bin/sh"); err == nil {
		t.Fatal("SetWorkspaceOrigin accepted an option-shaped url")
	}
	if err = db.SetWorkspaceOrigin(ctx, "ws_missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetWorkspaceOrigin on missing row: %v, want ErrNotFound", err)
	}

	// A full update carries the column too, rather than resetting it.
	got.Origin = "ssh://git@github.com/acme/app.git"
	if err = db.UpdateWorkspace(ctx, got); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}
	after, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspace after update: %v", err)
	}
	if after.Origin != got.Origin {
		t.Fatalf("origin after update = %q, want %q", after.Origin, got.Origin)
	}
}

// TestWorkspaceOriginMigrationDefaultsEmpty pins v23 on an existing
// database: rows written before the column existed come back with an
// empty origin rather than failing the scan.
func TestWorkspaceOriginMigrationDefaultsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 22)
	if _, err := raw.Exec(`
		INSERT INTO workspaces (id, name, environment, base_branch, steer_others, created_at)
		VALUES ('w1', 'proj', '{}', 'main', '', 1);
	`); err != nil {
		t.Fatalf("seed v22 workspace: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (v23 migration): %v", err)
	}
	defer func() { _ = db.Close() }()

	ws, err := db.GetWorkspace(context.Background(), "w1")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.Origin != "" {
		t.Fatalf("migrated origin = %q, want empty", ws.Origin)
	}
	if err := db.SetWorkspaceOrigin(context.Background(), "w1", "https://github.com/acme/app.git"); err != nil {
		t.Fatalf("SetWorkspaceOrigin on the migrated row: %v", err)
	}
}
