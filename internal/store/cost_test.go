package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

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

// TestSummarizeRunCostsMatchesRollupSemantics covers the scalar history
// query: metered rows contribute all numeric fields, unmetered rows count
// but contribute no numbers, and rows from another or empty workspace do not
// leak into the result.
func TestSummarizeRunCostsMatchesRollupSemantics(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w1 := mustCreateWorkspace(t, db)
	w2 := mustCreateWorkspace(t, db)
	w3 := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	r1 := mustCreateRun(t, db, w1.ID, m.ID, domain.RunRunning)
	r2 := mustCreateRun(t, db, w1.ID, m.ID, domain.RunRunning)
	r3 := mustCreateRun(t, db, w1.ID, m.ID, domain.RunRunning)
	r4 := mustCreateRun(t, db, w1.ID, m.ID, domain.RunRunning)
	r5 := mustCreateRun(t, db, w2.ID, m.ID, domain.RunRunning)
	at := time.Unix(100, 0).UTC()
	put := func(c *RunCost) {
		t.Helper()
		if err := db.PutRunCost(ctx, c); err != nil {
			t.Fatalf("PutRunCost %s: %v", c.RunID, err)
		}
	}
	put(&RunCost{
		RunID: r1.ID, WorkspaceID: w1.ID, MemberID: m.ID,
		InputTokens: 10, OutputTokens: 1, CostUSD: 1.25, Metered: true,
		RecordedAt: at,
	})
	put(&RunCost{
		RunID: r2.ID, WorkspaceID: w1.ID, MemberID: m.ID,
		InputTokens: 20, OutputTokens: 2, CostUSD: 2.50, Metered: true,
		RecordedAt: at.Add(time.Second),
	})
	put(&RunCost{
		RunID: r3.ID, WorkspaceID: w1.ID, MemberID: m.ID,
		InputTokens: 30, OutputTokens: 3, CostUSD: 3.75, Metered: true,
		RecordedAt: at.Add(2 * time.Second),
	})
	// Numeric fields on an unmetered row are ignored just as Rollup.Add
	// ignores them, even if a malformed or legacy row contains values.
	put(&RunCost{
		RunID: r4.ID, WorkspaceID: w1.ID, MemberID: m.ID,
		InputTokens: 999, OutputTokens: 999, CostUSD: 99, Metered: false,
		RecordedAt: at.Add(3 * time.Second),
	})
	put(&RunCost{
		RunID: r5.ID, WorkspaceID: w2.ID, MemberID: m.ID,
		InputTokens: 500, OutputTokens: 50, CostUSD: 100, Metered: true,
		RecordedAt: at,
	})

	got, err := db.SummarizeRunCosts(ctx, w1.ID)
	if err != nil {
		t.Fatalf("SummarizeRunCosts: %v", err)
	}
	if got.Runs != 4 || got.Metered != 3 || got.Unmetered != 1 {
		t.Fatalf("summary counts = %+v, want 4 runs / 3 metered / 1 unmetered", got)
	}
	if got.InputTokens != 60 || got.OutputTokens != 6 {
		t.Fatalf("summary tokens = %d in / %d out, want 60 / 6", got.InputTokens, got.OutputTokens)
	}
	if got.CostUSD != 7.5 {
		t.Fatalf("summary cost = %v, want 7.5", got.CostUSD)
	}

	other, err := db.SummarizeRunCosts(ctx, w2.ID)
	if err != nil {
		t.Fatalf("SummarizeRunCosts other workspace: %v", err)
	}
	if other.Runs != 1 || other.Metered != 1 || other.Unmetered != 0 ||
		other.InputTokens != 500 || other.OutputTokens != 50 || other.CostUSD != 100 {
		t.Fatalf("other workspace summary = %+v, want only its own run", other)
	}
	empty, err := db.SummarizeRunCosts(ctx, w3.ID)
	if err != nil {
		t.Fatalf("SummarizeRunCosts empty workspace: %v", err)
	}
	if empty != (RunCostSummary{}) {
		t.Fatalf("empty workspace summary = %+v, want zero", empty)
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
	summary, err := db.SummarizeRunCosts(ctx, "w1")
	if err != nil || summary != (RunCostSummary{Runs: 1, Metered: 1, InputTokens: 0, OutputTokens: 0, CostUSD: 1.5}) {
		t.Fatalf("SummarizeRunCosts after migration: %v, %+v", err, summary)
	}
}
