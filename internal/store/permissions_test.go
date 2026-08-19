package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestSessionSteerOthersRoundTrip covers create/update/narrow-mutator
// paths for the steer_others column, plus rejection of undefined values.
func TestSessionSteerOthersRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)

	s := &domain.Session{WorkspaceID: w.ID, Name: "locked", BaseBranch: "main",
		SteerOthers: domain.SteerOthersAdminsOnly}
	if err := db.CreateSession(ctx, s); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := db.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.SteerOthers != domain.SteerOthersAdminsOnly {
		t.Fatalf("steer_others = %q, want %q", got.SteerOthers, domain.SteerOthersAdminsOnly)
	}

	if serr := db.SetSessionSteerOthers(ctx, s.ID, ""); serr != nil {
		t.Fatalf("SetSessionSteerOthers: %v", serr)
	}
	got, err = db.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetSession after set: %v", err)
	}
	if got.SteerOthers != "" {
		t.Fatalf("steer_others = %q, want empty", got.SteerOthers)
	}
	// The narrow mutator touches nothing else.
	if got.Name != "locked" || got.BaseBranch != "main" || !got.CreatedAt.Equal(s.CreatedAt) {
		t.Fatalf("SetSessionSteerOthers clobbered fields: %+v", got)
	}

	if err := db.SetSessionSteerOthers(ctx, s.ID, "sometimes"); err == nil {
		t.Fatal("SetSessionSteerOthers accepted an undefined value")
	}
	s.SteerOthers = "sometimes"
	if err := db.UpdateSession(ctx, s); err == nil {
		t.Fatal("UpdateSession accepted an undefined steer_others")
	}

	if err := db.SetSessionSteerOthers(ctx, "sess_missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetSessionSteerOthers on missing row: %v, want ErrNotFound", err)
	}
}

// TestSetRunProtected covers the narrow protected mutator.
func TestSetRunProtected(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, s.ID, m.ID, domain.RunRunning)

	if r.Protected {
		t.Fatal("new run is protected by default")
	}
	if err := db.SetRunProtected(ctx, r.ID, true); err != nil {
		t.Fatalf("SetRunProtected: %v", err)
	}
	got, err := db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !got.Protected {
		t.Fatal("protected did not persist")
	}
	// Nothing else moved.
	r.Protected = true
	assertRunEqual(t, r, got)

	if perr := db.SetRunProtected(ctx, r.ID, false); perr != nil {
		t.Fatalf("SetRunProtected off: %v", perr)
	}
	got, err = db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Protected {
		t.Fatal("protected did not clear")
	}

	if err := db.SetRunProtected(ctx, "run_missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRunProtected on missing row: %v, want ErrNotFound", err)
	}
}

// TestPermissionsMigrationUpgradesV3 builds a genuine v3 database, seeds
// rows, then opens it: v4 must add the columns with permissive defaults
// and lose nothing.
func TestPermissionsMigrationUpgradesV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, execErr := raw.Exec(`CREATE TABLE schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); execErr != nil {
		t.Fatalf("create schema_migrations: %v", execErr)
	}
	for v := 1; v <= 3; v++ {
		if _, execErr := raw.Exec(migrations[v-1]); execErr != nil {
			t.Fatalf("apply v%d: %v", v, execErr)
		}
		if _, execErr := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, v); execErr != nil {
			t.Fatalf("record v%d: %v", v, execErr)
		}
	}
	key := testKey(t, "")
	if _, execErr := raw.Exec(`INSERT INTO members (id, display_name, public_key, color, role, created_at)
		VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1)`, key); execErr != nil {
		t.Fatalf("seed member: %v", execErr)
	}
	if _, execErr := raw.Exec(`
		INSERT INTO workspaces (id, name, image, env, setup_script, created_at)
			VALUES ('w1', 'proj', 'img', '{}', '', 1);
		INSERT INTO sessions (id, workspace_id, name, base_branch, created_at)
			VALUES ('s1', 'w1', 'effort', 'main', 1);
		INSERT INTO runs (id, session_id, member_id, task, harness, mode, status, branch, worktree, created_at, profile_snapshot_id)
			VALUES ('r1', 's1', 'm1', 'task', 'claude', 'tui', 'running', '', '', 1, '');
	`); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (v4 migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	sess, err := db.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession after migration: %v", err)
	}
	if sess.SteerOthers != "" {
		t.Fatalf("migrated steer_others = %q, want permissive default", sess.SteerOthers)
	}
	run, err := db.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun after migration: %v", err)
	}
	if run.Protected {
		t.Fatal("migrated run is protected, want permissive default")
	}
	// The upgraded schema accepts the new values.
	if err := db.SetSessionSteerOthers(ctx, "s1", domain.SteerOthersAdminsOnly); err != nil {
		t.Fatalf("SetSessionSteerOthers after migration: %v", err)
	}
	if err := db.SetRunProtected(ctx, "r1", true); err != nil {
		t.Fatalf("SetRunProtected after migration: %v", err)
	}
}
