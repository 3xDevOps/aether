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
	t.Parallel()
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

	list, _, err := db.ListWorkspaceCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceCosts: %v", err)
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
	t.Parallel()
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
	t.Parallel()
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
	list, _, err := db.ListWorkspaceCosts(ctx, "w1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListWorkspaceCosts after migration: %v, %+v", err, list)
	}
	summary, err := db.SummarizeRunCosts(ctx, "w1")
	if err != nil || summary != (RunCostSummary{Runs: 1, Metered: 1, InputTokens: 0, OutputTokens: 0, CostUSD: 1.5}) {
		t.Fatalf("SummarizeRunCosts after migration: %v, %+v", err, summary)
	}
}

// TestDeleteRunFoldsCostIntoWorkspaceAndMemberTotals proves that deleting
// a run must not lower the workspace's or a member's counted spend, even
// though the run's own run_costs row (and its per-run listing) is gone.
func TestDeleteRunFoldsCostIntoWorkspaceAndMemberTotals(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m1 := mustCreateMember(t, db)
	m2 := mustCreateMember(t, db)
	r1 := mustCreateRun(t, db, w.ID, m1.ID, domain.RunRunning)
	r2 := mustCreateRun(t, db, w.ID, m1.ID, domain.RunRunning)
	r3 := mustCreateRun(t, db, w.ID, m2.ID, domain.RunRunning)
	r4 := mustCreateRun(t, db, w.ID, m2.ID, domain.RunRunning)
	r5 := mustCreateRun(t, db, w.ID, m2.ID, domain.RunRunning) // never metered or recorded

	put := func(c *RunCost) {
		t.Helper()
		if err := db.PutRunCost(ctx, c); err != nil {
			t.Fatalf("PutRunCost %s: %v", c.RunID, err)
		}
	}
	put(&RunCost{RunID: r1.ID, WorkspaceID: w.ID, MemberID: m1.ID, InputTokens: 100, OutputTokens: 10, CostUSD: 1, Metered: true})
	put(&RunCost{RunID: r2.ID, WorkspaceID: w.ID, MemberID: m1.ID, InputTokens: 50, OutputTokens: 5, CostUSD: 0.5, Metered: true})
	put(&RunCost{RunID: r3.ID, WorkspaceID: w.ID, MemberID: m2.ID}) // unmetered
	put(&RunCost{RunID: r4.ID, WorkspaceID: w.ID, MemberID: m2.ID, InputTokens: 200, OutputTokens: 20, CostUSD: 2, Metered: true})

	before, err := db.SummarizeRunCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("SummarizeRunCosts: %v", err)
	}
	wantBefore := RunCostSummary{Runs: 4, Metered: 3, Unmetered: 1, InputTokens: 350, OutputTokens: 35, CostUSD: 3.5}
	if before != wantBefore {
		t.Fatalf("summary before delete = %+v, want %+v", before, wantBefore)
	}

	// Deleting a metered run of m1's must not move the workspace total,
	// and must fold exactly that run's numbers into m1's accumulator row.
	if err = db.DeleteRun(ctx, r1.ID); err != nil {
		t.Fatalf("DeleteRun r1: %v", err)
	}
	afterR1, err := db.SummarizeRunCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("SummarizeRunCosts after deleting r1: %v", err)
	}
	if afterR1 != wantBefore {
		t.Fatalf("summary after deleting r1 = %+v, want unchanged %+v", afterR1, wantBefore)
	}
	_, deleted, err := db.ListWorkspaceCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceCosts: %v", err)
	}
	if len(deleted) != 1 || deleted[0].MemberID != m1.ID {
		t.Fatalf("deleted totals = %+v, want one row for m1", deleted)
	}
	wantM1 := RunCostSummary{Runs: 1, Metered: 1, InputTokens: 100, OutputTokens: 10, CostUSD: 1}
	if deleted[0].RunCostSummary != wantM1 {
		t.Fatalf("m1 deleted totals = %+v, want %+v", deleted[0].RunCostSummary, wantM1)
	}
	// m1's remaining live row (r2) plus the folded total from r1 must equal
	// what m1 had before anything was deleted.
	remaining, _, err := db.ListWorkspaceCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceCosts: %v", err)
	}
	var m1Live RunCostSummary
	for _, c := range remaining {
		if c.MemberID != m1.ID {
			continue
		}
		m1Live.Runs++
		m1Live.Metered++
		m1Live.InputTokens += c.InputTokens
		m1Live.OutputTokens += c.OutputTokens
		m1Live.CostUSD += c.CostUSD
	}
	m1Total := m1Live
	m1Total.Runs += deleted[0].Runs
	m1Total.Metered += deleted[0].Metered
	m1Total.InputTokens += deleted[0].InputTokens
	m1Total.OutputTokens += deleted[0].OutputTokens
	m1Total.CostUSD += deleted[0].CostUSD
	wantM1Total := RunCostSummary{Runs: 2, Metered: 2, InputTokens: 150, OutputTokens: 15, CostUSD: 1.5}
	if m1Total != wantM1Total {
		t.Fatalf("m1 total (live + deleted) = %+v, want %+v", m1Total, wantM1Total)
	}

	// Deleting an unmetered run of m2's must also leave the workspace
	// total untouched, and must not add anything numeric to m2's row.
	if err = db.DeleteRun(ctx, r3.ID); err != nil {
		t.Fatalf("DeleteRun r3: %v", err)
	}
	afterR3, err := db.SummarizeRunCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("SummarizeRunCosts after deleting r3: %v", err)
	}
	if afterR3 != wantBefore {
		t.Fatalf("summary after deleting r3 = %+v, want unchanged %+v", afterR3, wantBefore)
	}
	_, deleted, err = db.ListWorkspaceCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceCosts after r3: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted totals after r3 = %+v, want rows for both members", deleted)
	}
	var m2Deleted RunCostSummary
	for _, d := range deleted {
		if d.MemberID == m2.ID {
			m2Deleted = d.RunCostSummary
		}
	}
	if want := (RunCostSummary{Runs: 1, Unmetered: 1}); m2Deleted != want {
		t.Fatalf("m2 deleted totals = %+v, want %+v", m2Deleted, want)
	}

	// Deleting a run that never had a run_costs row folds nothing.
	if err = db.DeleteRun(ctx, r5.ID); err != nil {
		t.Fatalf("DeleteRun r5: %v", err)
	}
	afterR5, err := db.SummarizeRunCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("SummarizeRunCosts after deleting r5: %v", err)
	}
	if afterR5 != wantBefore {
		t.Fatalf("summary after deleting r5 = %+v, want unchanged %+v", afterR5, wantBefore)
	}
	_, deleted, err = db.ListWorkspaceCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListWorkspaceCosts after r5: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted totals after r5 = %+v, want no new row for a run without a cost record", deleted)
	}
}

// TestDeleteMemberAfterDeletedRunCost covers the defect run_cost_deletions
// must not reintroduce: the accumulator's member_id carries no foreign
// key (see migrate.go v30), so a member with a deleted run's cost folded
// in must still be removable, and the workspace total must keep counting
// that spend afterward.
func TestDeleteMemberAfterDeletedRunCost(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	if err := db.PutRunCost(ctx, &RunCost{
		RunID: r.ID, WorkspaceID: w.ID, MemberID: m.ID,
		InputTokens: 100, OutputTokens: 10, CostUSD: 1, Metered: true,
	}); err != nil {
		t.Fatalf("PutRunCost: %v", err)
	}
	if err := db.DeleteRun(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if err := db.DeleteMember(ctx, m.ID); err != nil {
		t.Fatalf("DeleteMember after a deleted run folded its cost: %v", err)
	}
	got, err := db.SummarizeRunCosts(ctx, w.ID)
	if err != nil {
		t.Fatalf("SummarizeRunCosts after DeleteMember: %v", err)
	}
	want := RunCostSummary{Runs: 1, Metered: 1, InputTokens: 100, OutputTokens: 10, CostUSD: 1}
	if got != want {
		t.Fatalf("workspace total after DeleteMember = %+v, want %+v (the deleted member's spend must stay)", got, want)
	}
}

// TestDeleteWorkspaceAfterDeletedRunCost covers the workspace side of the
// same defect: run_cost_deletions.workspace_id keeps its foreign key, so
// DeleteWorkspace must clear the workspace's accumulator rows itself
// instead of being blocked by them forever.
func TestDeleteWorkspaceAfterDeletedRunCost(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, w.ID, m.ID, domain.RunRunning)

	if err := db.PutRunCost(ctx, &RunCost{
		RunID: r.ID, WorkspaceID: w.ID, MemberID: m.ID,
		InputTokens: 100, OutputTokens: 10, CostUSD: 1, Metered: true,
	}); err != nil {
		t.Fatalf("PutRunCost: %v", err)
	}
	if err := db.DeleteRun(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if err := db.DeleteWorkspace(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkspace after a deleted run folded its cost: %v", err)
	}
	var rows int
	if err := db.db.QueryRow(`SELECT count(*) FROM run_cost_deletions WHERE workspace_id = ?`, w.ID).Scan(&rows); err != nil {
		t.Fatalf("inspect run_cost_deletions after DeleteWorkspace: %v", err)
	}
	if rows != 0 {
		t.Fatalf("run_cost_deletions rows survived DeleteWorkspace: %d", rows)
	}
}

// deletedRunCostSchemaVersion is the migration slot that adds the
// run_cost_deletions accumulator DeleteRun folds a removed run's spend
// into.
const deletedRunCostSchemaVersion = 30

// TestDeletedRunCostMigrationAddsAccumulator builds a database at the
// version before the accumulator migration, seeds a run and its recorded
// cost, then opens it: the upgrade must add run_cost_deletions without
// disturbing existing cost history, and DeleteRun must fold into it from
// then on.
func TestDeletedRunCostMigrationAddsAccumulator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, deletedRunCostSchemaVersion-1)
	key := testKey(t, "legacy")
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, environment, base_branch, steer_others, origin, created_at)
			VALUES ('w1', 'legacy', '{}', 'main', '', '', 1);
		INSERT INTO runs (id, workspace_id, member_id, account_member_id, task, harness, mode, status,
		                  reason, branch, worktree, protected, created_at, started_at, finished_at,
		                  profile_snapshot_id, title, last_commit, last_commit_at, harness_session_id)
			VALUES ('r1', 'w1', 'm1', 'm1', 'legacy task', 'claude', 'tui', 'running',
			        '', 'legacy-branch', '', 0, 1, NULL, NULL, '', '', '', NULL, '');
		INSERT INTO run_costs (run_id, workspace_id, member_id, input_tokens, output_tokens, cost_usd, metered, recorded_at)
			VALUES ('r1', 'w1', 'm1', 100, 10, 1.5, 1, 1);
	`, key); err != nil {
		_ = raw.Close()
		t.Fatalf("seed v%d rows: %v", deletedRunCostSchemaVersion-1, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (deleted run cost migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	var version int
	if queryErr := db.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); queryErr != nil {
		t.Fatalf("read schema version: %v", queryErr)
	}
	if version != len(migrations) {
		t.Fatalf("schema version = %d, want %d", version, len(migrations))
	}

	before, err := db.SummarizeRunCosts(ctx, "w1")
	if err != nil {
		t.Fatalf("SummarizeRunCosts after migration: %v", err)
	}
	wantBefore := RunCostSummary{Runs: 1, Metered: 1, InputTokens: 100, OutputTokens: 10, CostUSD: 1.5}
	if before != wantBefore {
		t.Fatalf("summary after migration = %+v, want %+v", before, wantBefore)
	}

	if err = db.DeleteRun(ctx, "r1"); err != nil {
		t.Fatalf("DeleteRun after migration: %v", err)
	}
	after, err := db.SummarizeRunCosts(ctx, "w1")
	if err != nil {
		t.Fatalf("SummarizeRunCosts after delete: %v", err)
	}
	if after != wantBefore {
		t.Fatalf("summary after delete = %+v, want unchanged %+v", after, wantBefore)
	}
	_, deleted, err := db.ListWorkspaceCosts(ctx, "w1")
	if err != nil {
		t.Fatalf("ListWorkspaceCosts: %v", err)
	}
	if len(deleted) != 1 || deleted[0].MemberID != "m1" || deleted[0].RunCostSummary != wantBefore {
		t.Fatalf("deleted totals = %+v, want one m1 row matching %+v", deleted, wantBefore)
	}
}
