package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRunBaseProvenanceRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	checkedAt := time.Date(2026, 9, 11, 12, 34, 56, 789000000, time.UTC)

	run := &domain.Run{
		WorkspaceID:   workspace.ID,
		MemberID:      member.ID,
		Task:          "record base provenance",
		Harness:       "claude",
		Mode:          domain.LaunchTUI,
		Status:        domain.RunQueued,
		BaseCommit:    "0123456789abcdef0123456789abcdef01234567",
		BaseBranch:    "main",
		BaseSource:    "github.com/acme/project",
		BaseCheckedAt: checkedAt,
	}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	assertRunBaseProvenance(t, got, run)

	run.BaseCommit = "fedcba9876543210fedcba9876543210fedcba98"
	run.BaseBranch = "release/v2"
	run.BaseSource = "git.example.test/acme/project"
	run.BaseCheckedAt = checkedAt.Add(time.Hour)
	if updateErr := db.UpdateRun(ctx, run); updateErr != nil {
		t.Fatalf("UpdateRun: %v", updateErr)
	}
	got, err = db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after update: %v", err)
	}
	assertRunBaseProvenance(t, got, run)

	byWorkspace, err := db.ListRunsByWorkspace(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("ListRunsByWorkspace: %v", err)
	}
	if len(byWorkspace) != 1 {
		t.Fatalf("ListRunsByWorkspace length = %d, want 1", len(byWorkspace))
	}
	assertRunBaseProvenance(t, byWorkspace[0], run)

	byMember, err := db.ListRunsByMember(ctx, member.ID)
	if err != nil {
		t.Fatalf("ListRunsByMember: %v", err)
	}
	if len(byMember) != 1 {
		t.Fatalf("ListRunsByMember length = %d, want 1", len(byMember))
	}
	assertRunBaseProvenance(t, byMember[0], run)
}

func assertRunBaseProvenance(t *testing.T, got, want *domain.Run) {
	t.Helper()
	if got.BaseCommit != want.BaseCommit || got.BaseBranch != want.BaseBranch || got.BaseSource != want.BaseSource {
		t.Fatalf("base provenance mismatch: got commit=%q branch=%q source=%q, want commit=%q branch=%q source=%q",
			got.BaseCommit, got.BaseBranch, got.BaseSource,
			want.BaseCommit, want.BaseBranch, want.BaseSource)
	}
	if !got.BaseCheckedAt.Equal(want.BaseCheckedAt) || got.BaseCheckedAt.Location() != time.UTC {
		t.Fatalf("base checked at = %v (%v), want %v (UTC)", got.BaseCheckedAt, got.BaseCheckedAt.Location(), want.BaseCheckedAt)
	}
}

func TestRunBaseProvenanceMigrationFromV25(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 25)
	key := testKey(t, "legacy")
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, environment, base_branch, steer_others, origin, created_at)
			VALUES ('w1', 'legacy', '{}', 'main', '', '', 1);
		INSERT INTO runs (id, workspace_id, member_id, account_member_id, task, harness, mode, status,
		                  reason, branch, worktree, protected, created_at, started_at, finished_at,
		                  profile_snapshot_id, title, last_commit, last_commit_at, harness_session_id)
			VALUES ('r1', 'w1', 'm1', 'm1', 'legacy task', 'claude', 'tui', 'queued',
			        '', 'legacy-branch', '', 0, 1, NULL, NULL, '', '', '', NULL, '');
	`, key); err != nil {
		_ = raw.Close()
		t.Fatalf("seed v25 row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close v25 database: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated v25 database: %v", err)
	}
	defer func() { _ = db.Close() }()

	var version int
	if queryErr := db.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); queryErr != nil {
		t.Fatalf("read schema version: %v", queryErr)
	}
	if version != 26 {
		t.Fatalf("schema version = %d, want 26", version)
	}

	got, err := db.GetRun(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetRun migrated row: %v", err)
	}
	if got.BaseCommit != "" || got.BaseBranch != "" || got.BaseSource != "" || !got.BaseCheckedAt.IsZero() {
		t.Fatalf("migrated base provenance = commit=%q branch=%q source=%q checked=%v, want empty/zero",
			got.BaseCommit, got.BaseBranch, got.BaseSource, got.BaseCheckedAt)
	}
	if got.BaseCheckedAt.Location() != time.UTC {
		t.Fatalf("migrated BaseCheckedAt location = %v, want UTC", got.BaseCheckedAt.Location())
	}

	var baseCommit, baseBranch, baseSource string
	var baseCheckedAt sql.NullInt64
	if err := db.db.QueryRow(`SELECT base_commit, base_branch, base_source, base_checked_at FROM runs WHERE id = 'r1'`).Scan(
		&baseCommit, &baseBranch, &baseSource, &baseCheckedAt); err != nil {
		t.Fatalf("inspect migrated columns: %v", err)
	}
	if baseCommit != "" || baseBranch != "" || baseSource != "" || baseCheckedAt.Valid {
		t.Fatalf("migrated columns = %q, %q, %q, %v; want empty, empty, empty, NULL",
			baseCommit, baseBranch, baseSource, baseCheckedAt)
	}
}
