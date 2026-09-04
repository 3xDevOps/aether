package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// openLegacy builds a database at the given schema version by applying the
// first n migrations by hand, so a test can seed rows in the old shape and
// then let Open run the migration under test.
func openLegacy(t *testing.T, path string, n int) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for i := range n {
		if _, err := raw.Exec(migrations[i]); err != nil {
			t.Fatalf("apply v%d: %v", i+1, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, i+1); err != nil {
			t.Fatalf("record v%d: %v", i+1, err)
		}
	}
	return raw
}

// TestSessionCollapseMigrationRehomesEveryRow pins the session-to-workspace
// collapse: a v11 database whose rows all hang off sessions must come out of
// Open with the same rows hanging off workspaces, and with no sessions table
// left behind.
func TestSessionCollapseMigrationRehomesEveryRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 11)
	key := testKey(t, "")
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, image, env, setup_script, environment, created_at)
			VALUES ('w1', 'proj', 'img', '{}', '', '{"custom_image":"img","variables":{},"setup_policy":{"script":""}}', 1);
		INSERT INTO sessions (id, workspace_id, name, base_branch, steer_others, created_at)
			VALUES ('s1', 'w1', 'first', 'trunk', '', 1),
			       ('s2', 'w1', 'second', 'other', 'admins_only', 2);
		INSERT INTO runs (id, session_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r1', 's1', 'm1', 'one', 'claude', 'tui', 'running', 'b1', 'wt1', 1),
			       ('r2', 's2', 'm1', 'two', 'claude', 'tui', 'running', 'b2', 'wt2', 2);
		INSERT INTO approvals (id, session_id, run_id, source_id, action, detail, decision, created_at)
			VALUES ('a1', 's2', 'r2', 'tool_1', 'bash', 'rm -rf', 'requested', 3);
		INSERT INTO run_costs (run_id, session_id, member_id, input_tokens, output_tokens, cost_usd, metered, recorded_at)
			VALUES ('r1', 's1', 'm1', 10, 20, 1.5, 1, 4);
		INSERT INTO session_budgets (session_id, limit_usd, warn_usd, override, updated_by, updated_at)
			VALUES ('s1', 50, 10, 0, 'm1', 5),
			       ('s2', 20, 0, 1, 'm1', 6);
		INSERT INTO templates (id, session_id, name, task, harness, mode, params, budget_usd, created_at)
			VALUES ('t1', 's1', 'nightly', 'triage', 'claude', 'headless', '{}', 0, 7),
			       ('t2', 's2', 'nightly', 'other triage', 'codex', 'tui', '{}', 0, 8);
		INSERT INTO schedules (id, template_id, cron, member_id, created_at)
			VALUES ('sc2', 't2', '0 4 * * *', 'm1', 9);
		INSERT INTO run_messages (id, session_id, from_run, to_run, body, created_at)
			VALUES ('rm1', 's1', 'r1', 'r2', 'heads up', 10);
	`, key); err != nil {
		t.Fatalf("seed v11 rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (v12 migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	ws, err := db.GetWorkspace(ctx, "w1")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	// The oldest session's base branch is the workspace's, and the
	// restrictive steer policy of any session wins: the migration fails
	// closed on permissions.
	if ws.BaseBranch != "trunk" {
		t.Errorf("workspace base branch = %q, want trunk", ws.BaseBranch)
	}
	if ws.SteerOthers != domain.SteerOthersAdminsOnly {
		t.Errorf("workspace steer others = %q, want %q", ws.SteerOthers, domain.SteerOthersAdminsOnly)
	}

	runs, err := db.ListRunsByWorkspace(ctx, "w1")
	if err != nil {
		t.Fatalf("ListRunsByWorkspace: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs after migration = %d, want 2", len(runs))
	}
	for _, r := range runs {
		if r.WorkspaceID != "w1" {
			t.Errorf("run %s workspace = %q, want w1", r.ID, r.WorkspaceID)
		}
	}

	approvals, err := db.ListApprovals(ctx, "w1", "")
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 1 || approvals[0].WorkspaceID != "w1" || approvals[0].RunID != "r2" {
		t.Fatalf("approvals after migration = %+v", approvals)
	}

	costs, err := db.ListRunCosts(ctx, "w1")
	if err != nil {
		t.Fatalf("ListRunCosts: %v", err)
	}
	if len(costs) != 1 || costs[0].WorkspaceID != "w1" || costs[0].InputTokens != 10 {
		t.Fatalf("run costs after migration = %+v", costs)
	}

	// Two session budgets merge into one workspace budget, and the merge
	// only ever tightens: the lowest cap survives and the admin override
	// one session carried is dropped rather than extended over everything
	// the workspace now covers.
	budget, err := db.GetWorkspaceBudget(ctx, "w1")
	if err != nil {
		t.Fatalf("GetWorkspaceBudget: %v", err)
	}
	if budget.LimitUSD != 20 || budget.WarnUSD != 10 || budget.Override {
		t.Fatalf("merged budget = %+v, want limit 20, warn 10, override false", budget)
	}

	// Two sessions held a template of the same name; the workspace keeps
	// exactly one, and the older definition wins. The surviving row must be
	// one coherent row, not a mix: the migration selects bare columns
	// alongside MIN(id), which SQLite only resolves to the min-row when
	// exactly one such aggregate is present.
	templates, err := db.ListTemplates(ctx, "w1")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("templates after migration = %d, want 1", len(templates))
	}
	if got := templates[0]; got.ID != "t1" || got.Name != "nightly" ||
		got.Task != "triage" || got.Harness != "claude" || got.Mode != "headless" {
		t.Fatalf("surviving template is not one coherent row: %+v", got)
	}
	// The schedule hung off the losing duplicate template. It must follow
	// the survivor, not vanish: an upgrade that silently stops scheduled
	// work is worse than one that fails loudly.
	schedules, err := db.ListSchedules(ctx, "w1")
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(schedules) != 1 || schedules[0].TemplateID != "t1" ||
		schedules[0].WorkspaceID != "w1" || schedules[0].Cron != "0 4 * * *" {
		t.Fatalf("schedules after migration = %+v", schedules)
	}

	var sessions int
	if err := db.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`,
	).Scan(&sessions); err != nil {
		t.Fatalf("count sessions table: %v", err)
	}
	if sessions != 0 {
		t.Error("sessions table survived the migration")
	}
}

// TestSessionCollapseMigrationRewritesEventScope pins the event log half of
// the collapse: persisted events are scoped by workspace afterwards, and a
// database that never opened an event log still migrates.
func TestSessionCollapseMigrationRewritesEventScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 11)
	if _, err := raw.Exec(`
		INSERT INTO workspaces (id, name, image, env, setup_script, environment, created_at)
			VALUES ('w1', 'proj', 'img', '{}', '', '{"custom_image":"img","variables":{},"setup_policy":{"script":""}}', 1);
		INSERT INTO sessions (id, workspace_id, name, base_branch, steer_others, created_at)
			VALUES ('s1', 'w1', 'first', 'main', '', 1);
		CREATE TABLE events (
			seq        INTEGER PRIMARY KEY AUTOINCREMENT,
			id         TEXT NOT NULL UNIQUE,
			ts         INTEGER NOT NULL,
			session_id TEXT NOT NULL,
			run_id     TEXT NOT NULL,
			actor_id   TEXT NOT NULL,
			type       TEXT NOT NULL,
			payload    TEXT NOT NULL
		);
		CREATE INDEX idx_events_session_seq ON events (session_id, seq);
		INSERT INTO events (seq, id, ts, session_id, run_id, actor_id, type, payload)
			VALUES (41, 'e1', 1, 's1', 'r1', 'm1', 'run.status', '{"status":"running"}'),
			       (42, 'e2', 2, 's1', 'r1', 'm1', 'session.timeline', '{"kind":"inject","message":"go on"}'),
			       (43, 'e3', 3, 's1', 'r1', 'm1', 'session.approval', '{"decision":"requested"}'),
			       (44, 'e4', 4, 's1', '', 'm1', 'session.presence', '{"state":"online"}'),
			       (45, 'e5', 5, 's1', '', 'm1', 'session.budget', '{"state":"ok"}');
	`); err != nil {
		t.Fatalf("seed v11 rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (v12 migration): %v", err)
	}
	defer func() { _ = db.Close() }()

	var scope string
	var seq uint64
	if scanErr := db.db.QueryRowContext(context.Background(),
		`SELECT workspace_id, seq FROM events WHERE id = 'e1'`,
	).Scan(&scope, &seq); scanErr != nil {
		t.Fatalf("read migrated event scope: %v", scanErr)
	}
	if scope != "w1" {
		t.Errorf("event workspace scope = %q, want w1", scope)
	}
	// The bus resumes its sequence from MAX(seq), so a rebuild that
	// renumbered rows would hand out sequence numbers the log already
	// holds and every later publish would collide on the primary key.
	if seq != 41 {
		t.Errorf("event seq = %d, want 41 preserved across the rebuild", seq)
	}

	// The four session-scoped type strings were renamed with the scope
	// they name. Reading history back resolves a payload codec by exact
	// type string, so a row left under its old name does not just read
	// oddly, it fails the whole page. Read through the real log rather
	// than raw SQL, which is what catches that.
	log, err := events.OpenSQLiteLog(path)
	if err != nil {
		t.Fatalf("OpenSQLiteLog on the migrated database: %v", err)
	}
	defer func() { _ = log.Close() }()
	page, err := log.Read(context.Background(), events.Filter{Workspace: "w1"}, 0, 0, 100)
	if err != nil {
		t.Fatalf("read migrated history: %v", err)
	}
	if len(page) != 5 {
		t.Fatalf("migrated history = %d events, want 5", len(page))
	}
	want := []events.Type{
		events.TypeRunStatus,
		events.TypeTimeline,
		events.TypeApproval,
		events.TypePresence,
		events.TypeBudget,
	}
	for i, ev := range page {
		if ev.Type != want[i] {
			t.Errorf("event %d type = %q, want %q", i, ev.Type, want[i])
		}
		if ev.WorkspaceID != "w1" {
			t.Errorf("event %d workspace = %q, want w1", i, ev.WorkspaceID)
		}
	}
}

// TestSessionCollapseMigrationWithoutEventLog covers the fresh-install
// ordering: store.Open runs before the event log is ever created, so the
// migration must tolerate a database with no events table.
func TestSessionCollapseMigrationWithoutEventLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 11)
	if _, err := raw.Exec(`
		INSERT INTO workspaces (id, name, image, env, setup_script, environment, created_at)
			VALUES ('w1', 'proj', '', '{}', '', '{"neutral_image":true,"variables":{},"setup_policy":{"script":""}}', 1);
	`); err != nil {
		t.Fatalf("seed v11 rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (v12 migration, no event log): %v", err)
	}
	defer func() { _ = db.Close() }()

	ws, err := db.GetWorkspace(context.Background(), "w1")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	// A workspace that never had a session still gets the default base
	// branch rather than an empty one.
	if ws.BaseBranch != "main" {
		t.Errorf("workspace base branch = %q, want main", ws.BaseBranch)
	}
}
func TestMemberHomeMigrationDropsLegacyTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 13)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, tailnet_login, pending, color, role, created_at)
			VALUES ('m1', 'Ada', '', 'ada@example.com', 0, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, image, env, setup_script, environment, base_branch, steer_others, created_at)
			VALUES ('w1', 'proj', '', '{}', '', '{"neutral_image":true,"variables":{},"setup_policy":{"script":""}}', 'main', '', 1);
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, reason,
		                  branch, worktree, protected, profile_snapshot_id, tool_snapshot_id,
		                  created_at, started_at, finished_at)
			VALUES ('r1', 'w1', 'm1', 'task', 'claude', 'tui', 'running', '',
			        'branch', 'worktree', 0, '', 'tools-1', 1, NULL, NULL);
		INSERT INTO tool_snapshots (id, workspace_id, member_id, digest, manifest, created_at)
			VALUES ('tools-1', 'w1', 'm1', 'sha256:test', '{}', 1);
		INSERT INTO tool_heads (member_id, workspace_id, snapshot_id)
			VALUES ('m1', 'w1', 'tools-1');
		INSERT INTO pending_workspace_shells (id, workspace_id, member_id, snapshot_id, staging_id, created_at, updated_at)
			VALUES ('p1', 'w1', 'm1', 'tools-1', '', 1, 1);
	`); err != nil {
		t.Fatalf("seed v13 rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (v14 migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	var version string
	if err := db.db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		t.Fatalf("read sqlite version: %v", err)
	}
	var major, minor int
	if _, err := fmt.Sscanf(version, "%d.%d", &major, &minor); err != nil ||
		major < 3 || (major == 3 && minor < 35) {
		t.Fatalf("sqlite version = %s, want at least 3.35", version)
	}

	var runID, snapshotID string
	if err := db.db.QueryRowContext(ctx,
		`SELECT id, profile_snapshot_id FROM runs WHERE id = 'r1'`,
	).Scan(&runID, &snapshotID); err != nil {
		t.Fatalf("read migrated run: %v", err)
	}
	if runID != "r1" || snapshotID != "" {
		t.Fatalf("migrated run = id %q, profile snapshot %q", runID, snapshotID)
	}
	var toolColumn int
	if err := db.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('runs') WHERE name = 'tool_snapshot_id'`,
	).Scan(&toolColumn); err != nil {
		t.Fatalf("inspect runs columns: %v", err)
	}
	if toolColumn != 0 {
		t.Fatal("runs.tool_snapshot_id survived migration")
	}
	for _, table := range []string{"tool_snapshots", "tool_heads", "pending_workspace_shells"} {
		var count int
		if err := db.db.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&count); err != nil {
			t.Fatalf("inspect %s table: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s table survived migration", table)
		}
	}
	var terminals int
	if err := db.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'member_terminals'`,
	).Scan(&terminals); err != nil {
		t.Fatalf("inspect member_terminals table: %v", err)
	}
	if terminals != 1 {
		t.Fatal("member_terminals table missing after migration")
	}
}
