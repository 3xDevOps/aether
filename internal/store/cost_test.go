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

// costSchemaVersion is the slice position of the cost migration. The
// upgrade test builds a database at the version below it and opens it.
const costSchemaVersion = 6

// TestRunCostMeteredWins covers the record's one non-obvious rule: a
// metered result replaces anything stored, an unmetered marker never
// overwrites real numbers, whichever order they arrive in.
func TestRunCostMeteredWins(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	unmetered := &RunCost{RunID: r.ID, WorkspaceID: w.ID, MemberID: m.ID}
	if err := db.PutRunCost(ctx, unmetered); err != nil {
		t.Fatalf("PutRunCost unmetered: %v", err)
	}
	got, err := db.GetRunCost(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunCost: %v", err)
	}
	if got.Metered || got.CostUSD != 0 {
		t.Fatalf("stored record = %+v, want unmetered and empty", got)
	}

	// A late adapter result upgrades the record.
	if err = db.PutRunCost(ctx, &RunCost{
		RunID: r.ID, WorkspaceID: w.ID, MemberID: m.ID,
		InputTokens: 1200, OutputTokens: 340, CostUSD: 0.42, Metered: true,
	}); err != nil {
		t.Fatalf("PutRunCost metered: %v", err)
	}
	if got, err = db.GetRunCost(ctx, r.ID); err != nil {
		t.Fatalf("GetRunCost after metering: %v", err)
	}
	if !got.Metered || got.CostUSD != 0.42 || got.InputTokens != 1200 {
		t.Fatalf("record = %+v, want the metered numbers", got)
	}

	// A later unmetered marker must not erase them.
	if err = db.PutRunCost(ctx, &RunCost{RunID: r.ID, WorkspaceID: w.ID, MemberID: m.ID}); err != nil {
		t.Fatalf("PutRunCost unmetered again: %v", err)
	}
	if got, err = db.GetRunCost(ctx, r.ID); err != nil {
		t.Fatalf("GetRunCost after downgrade attempt: %v", err)
	}
	if !got.Metered || got.CostUSD != 0.42 {
		t.Fatalf("record = %+v, want the metered numbers preserved", got)
	}

	list, err := db.ListRunCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListRunCosts: %v", err)
	}
	if len(list) != 1 || list[0].RunID != r.ID {
		t.Fatalf("list = %+v, want one record for the run", list)
	}
	if _, err := db.GetRunCost(ctx, "run_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRunCost on missing run: %v, want ErrNotFound", err)
	}
	if err := db.PutRunCost(ctx, &RunCost{RunID: "run_missing", WorkspaceID: w.ID, MemberID: m.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PutRunCost for an unknown run: %v, want ErrNotFound", err)
	}
}

// TestWorkspaceBudgetRoundTrip covers the budget row: upsert, validation,
// and clearing.
func TestWorkspaceBudgetRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)

	if _, err := db.GetWorkspaceBudget(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetWorkspaceBudget with none set: %v, want ErrNotFound", err)
	}
	b := &WorkspaceBudget{WorkspaceID: w.ID, LimitUSD: 25, WarnUSD: 20, UpdatedBy: m.ID}
	if err := db.SetWorkspaceBudget(ctx, b); err != nil {
		t.Fatalf("SetWorkspaceBudget: %v", err)
	}
	got, err := db.GetWorkspaceBudget(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceBudget: %v", err)
	}
	if got.LimitUSD != 25 || got.WarnUSD != 20 || got.Override || got.UpdatedBy != m.ID {
		t.Fatalf("budget = %+v, want the values just written", got)
	}

	b.Override = true
	b.WarnUSD = 0
	if err = db.SetWorkspaceBudget(ctx, b); err != nil {
		t.Fatalf("SetWorkspaceBudget override: %v", err)
	}
	if got, err = db.GetWorkspaceBudget(ctx, w.ID); err != nil {
		t.Fatalf("GetWorkspaceBudget after override: %v", err)
	}
	if !got.Override || got.WarnUSD != 0 {
		t.Fatalf("budget = %+v, want override on and no warning threshold", got)
	}

	if err := db.SetWorkspaceBudget(ctx, &WorkspaceBudget{WorkspaceID: w.ID, LimitUSD: 0}); err == nil {
		t.Fatal("SetWorkspaceBudget accepted a non-positive limit")
	}
	if err := db.SetWorkspaceBudget(ctx, &WorkspaceBudget{WorkspaceID: w.ID, LimitUSD: 5, WarnUSD: 9}); err == nil {
		t.Fatal("SetWorkspaceBudget accepted a warning threshold above the limit")
	}
	if err := db.SetWorkspaceBudget(ctx, &WorkspaceBudget{WorkspaceID: "ws_missing", LimitUSD: 5}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetWorkspaceBudget for an unknown workspace: %v, want ErrNotFound", err)
	}

	if err := db.DeleteWorkspaceBudget(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkspaceBudget: %v", err)
	}
	if _, err := db.GetWorkspaceBudget(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetWorkspaceBudget after delete: %v, want ErrNotFound", err)
	}
	if err := db.DeleteWorkspaceBudget(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkspaceBudget is not idempotent: %v", err)
	}
}

// TestCostMigrationUpgradesExistingDatabase builds a database at the
// version before the cost migration, seeds rows, then opens it: the
// upgrade must add the cost tables without disturbing what is there.
func TestCostMigrationUpgradesExistingDatabase(t *testing.T) {
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
	for v := 1; v < costSchemaVersion; v++ {
		if _, execErr := raw.Exec(migrations[v-1]); execErr != nil {
			t.Fatalf("apply v%d: %v", v, execErr)
		}
		if _, execErr := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, v); execErr != nil {
			t.Fatalf("record v%d: %v", v, execErr)
		}
	}
	if _, execErr := raw.Exec(`INSERT INTO members (id, display_name, public_key, color, role, created_at)
		VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1)`, testKey(t, "")); execErr != nil {
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
		t.Fatalf("Open (cost migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if _, err = db.GetRun(ctx, "r1"); err != nil {
		t.Fatalf("GetRun after migration: %v", err)
	}
	if err = db.PutRunCost(ctx, &RunCost{
		RunID: "r1", WorkspaceID: "w1", MemberID: "m1", CostUSD: 1.5, Metered: true,
	}); err != nil {
		t.Fatalf("PutRunCost after migration: %v", err)
	}
	if err = db.SetWorkspaceBudget(ctx, &WorkspaceBudget{WorkspaceID: "w1", LimitUSD: 10, UpdatedBy: "m1"}); err != nil {
		t.Fatalf("SetWorkspaceBudget after migration: %v", err)
	}
	list, err := db.ListRunCosts(ctx, "w1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListRunCosts after migration: %v, %+v", err, list)
	}
}
