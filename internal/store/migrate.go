package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// migrations is the ordered, append-only schema history. Entry i applies
// schema version i+1. Never edit an entry that has shipped; append a new
// one instead.
var migrations = []string{
	`
CREATE TABLE workspaces (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	image        TEXT NOT NULL,
	env          TEXT NOT NULL,
	setup_script TEXT NOT NULL,
	created_at   INTEGER NOT NULL
);

CREATE TABLE sessions (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	base_branch  TEXT NOT NULL,
	created_at   INTEGER NOT NULL
);
CREATE INDEX idx_sessions_workspace ON sessions(workspace_id);

CREATE TABLE members (
	id           TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	public_key   TEXT NOT NULL UNIQUE,
	color        TEXT NOT NULL,
	role         TEXT NOT NULL,
	created_at   INTEGER NOT NULL
);

CREATE TABLE runs (
	id          TEXT PRIMARY KEY,
	session_id  TEXT NOT NULL REFERENCES sessions(id),
	member_id   TEXT NOT NULL REFERENCES members(id),
	task        TEXT NOT NULL,
	harness     TEXT NOT NULL,
	mode        TEXT NOT NULL,
	status      TEXT NOT NULL,
	branch      TEXT NOT NULL,
	worktree    TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	started_at  INTEGER,
	finished_at INTEGER
);
CREATE INDEX idx_runs_session ON runs(session_id);
CREATE INDEX idx_runs_member ON runs(member_id);
CREATE INDEX idx_runs_status ON runs(status);
`,
	// v2: tailnet identity. Members gain tailnet_login (empty = none) and
	// pending (awaiting admin approval); public_key becomes optional so
	// key-less tailnet members exist, but at least one identity is
	// required. The table is rebuilt because SQLite cannot drop the old
	// inline UNIQUE(public_key), which would reject a second key-less
	// member; deferring FK checks keeps runs.member_id references intact
	// across the rebuild.
	`
PRAGMA defer_foreign_keys = ON;
CREATE TABLE members_migrate AS
	SELECT id, display_name, public_key, color, role, created_at FROM members;
DROP TABLE members;
CREATE TABLE members (
	id            TEXT PRIMARY KEY,
	display_name  TEXT NOT NULL,
	public_key    TEXT NOT NULL DEFAULT '',
	tailnet_login TEXT NOT NULL DEFAULT '',
	pending       INTEGER NOT NULL DEFAULT 0,
	color         TEXT NOT NULL,
	role          TEXT NOT NULL,
	created_at    INTEGER NOT NULL,
	CHECK (public_key <> '' OR tailnet_login <> '')
);
INSERT INTO members (id, display_name, public_key, color, role, created_at)
	SELECT id, display_name, public_key, color, role, created_at FROM members_migrate;
DROP TABLE members_migrate;
CREATE UNIQUE INDEX idx_members_public_key ON members(public_key) WHERE public_key <> '';
CREATE UNIQUE INDEX idx_members_tailnet_login ON members(tailnet_login) WHERE tailnet_login <> '';
`,
	// v3: content-addressed agent profile snapshots, file blobs, latest
	// heads, and a per-run pin column. Empty profile_snapshot_id means
	// unpinned; it is not a foreign key so runs can exist without one.
	`
CREATE TABLE profile_snapshots (
	id         TEXT PRIMARY KEY,
	member_id  TEXT NOT NULL REFERENCES members(id),
	harness    TEXT NOT NULL,
	digest     TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE (member_id, harness, digest)
);
CREATE INDEX idx_profile_snapshots_member_harness ON profile_snapshots(member_id, harness, created_at);

CREATE TABLE profile_blobs (
	digest  TEXT PRIMARY KEY,
	content BLOB NOT NULL
);

CREATE TABLE profile_files (
	snapshot_id TEXT NOT NULL REFERENCES profile_snapshots(id) ON DELETE CASCADE,
	path        TEXT NOT NULL,
	mode        INTEGER NOT NULL,
	blob_digest TEXT NOT NULL REFERENCES profile_blobs(digest),
	PRIMARY KEY (snapshot_id, path)
);
CREATE INDEX idx_profile_files_blob ON profile_files(blob_digest);

CREATE TABLE profile_heads (
	member_id   TEXT NOT NULL,
	harness     TEXT NOT NULL,
	snapshot_id TEXT NOT NULL REFERENCES profile_snapshots(id),
	PRIMARY KEY (member_id, harness)
);

ALTER TABLE runs ADD COLUMN profile_snapshot_id TEXT NOT NULL DEFAULT '';
`,
	// v4: permission model. Sessions gain steer_others ('' = permissive
	// default, 'admins_only' = restrict steering/killing others' runs to
	// owner and admins); runs gain protected (0 = default, 1 = steer/kill
	// restricted to owner and admins). Defaults preserve the previously
	// unconditional permissive behavior.
	`
ALTER TABLE sessions ADD COLUMN steer_others TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN protected INTEGER NOT NULL DEFAULT 0;
`,
	// v5: the shared approval inbox. One row per permission request raised
	// by a run; decision is 'requested' until a steer-holder decides it,
	// after which decided_by and decided_at carry the attribution.
	// source_id is the raising request's own identity within its run (the
	// agent's tool-use id), unique per run so a replayed event cannot raise
	// the same request twice; '' means no identity and no deduplication.
	`
CREATE TABLE approvals (
	id         TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	run_id     TEXT NOT NULL REFERENCES runs(id),
	source_id  TEXT NOT NULL DEFAULT '',
	action     TEXT NOT NULL,
	detail     TEXT NOT NULL DEFAULT '',
	decision   TEXT NOT NULL,
	decided_by TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	decided_at INTEGER
);
CREATE INDEX idx_approvals_session ON approvals(session_id, decision);
CREATE UNIQUE INDEX idx_approvals_source ON approvals(run_id, source_id) WHERE source_id <> '';
`,
	// v6: cost attribution and session budgets. One run_costs row per run,
	// metered=0 meaning the run's usage was never measured (no harness
	// adapter) rather than measured as zero. session_budgets holds the cap,
	// the soft warning threshold (0 = none), and the admin override that
	// lets new runs start past the cap.
	`
CREATE TABLE run_costs (
	run_id        TEXT PRIMARY KEY REFERENCES runs(id),
	session_id    TEXT NOT NULL REFERENCES sessions(id),
	member_id     TEXT NOT NULL REFERENCES members(id),
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	metered       INTEGER NOT NULL DEFAULT 0,
	recorded_at   INTEGER NOT NULL
);
CREATE INDEX idx_run_costs_session ON run_costs(session_id);
CREATE INDEX idx_run_costs_member ON run_costs(member_id);

CREATE TABLE session_budgets (
	session_id TEXT PRIMARY KEY REFERENCES sessions(id),
	limit_usd  REAL NOT NULL,
	warn_usd   REAL NOT NULL DEFAULT 0,
	override   INTEGER NOT NULL DEFAULT 0,
	updated_by TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);
`,
	// v7: task templates and their cron schedules. A template is a named,
	// parameterized run definition on a session; params is a JSON object
	// of placeholder defaults. At most one schedule per template, and it
	// dies with the template. member_id is the schedule's creator: every
	// fire is attributed to them and re-checked against their role.
	`
CREATE TABLE templates (
	id         TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	name       TEXT NOT NULL,
	task       TEXT NOT NULL,
	harness    TEXT NOT NULL,
	mode       TEXT NOT NULL,
	params     TEXT NOT NULL DEFAULT '{}',
	budget_usd REAL NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	UNIQUE (session_id, name)
);

CREATE TABLE schedules (
	id            TEXT PRIMARY KEY,
	template_id   TEXT NOT NULL UNIQUE REFERENCES templates(id) ON DELETE CASCADE,
	cron          TEXT NOT NULL,
	member_id     TEXT NOT NULL REFERENCES members(id),
	created_at    INTEGER NOT NULL,
	last_fired_at INTEGER
);
`,
	// v8: the run-to-run coordination mailbox. One row per message between
	// two runs the conflict radar put in file conflict. delivery_token is
	// the opaque run-scoped token that binds one delivered batch: it is
	// written together with delivered_at, handed to the reader, and
	// acknowledged as a unit by the next read, so a response lost between
	// the server and the agent redelivers the same batch instead of losing
	// it. An empty token means undelivered; a NULL acked_at means unread.
	`
CREATE TABLE run_messages (
	id             TEXT PRIMARY KEY,
	session_id     TEXT NOT NULL REFERENCES sessions(id),
	from_run       TEXT NOT NULL REFERENCES runs(id),
	to_run         TEXT NOT NULL REFERENCES runs(id),
	body           TEXT NOT NULL,
	delivery_token TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL,
	delivered_at   INTEGER,
	acked_at       INTEGER
);
CREATE INDEX idx_run_messages_inbox ON run_messages(to_run, acked_at, id);
`,
	// v9: first-class workspace environments and immutable per-member tool
	// snapshots. Legacy workspace columns remain for on-disk compatibility,
	// while environment is the sole runtime representation.
	`
ALTER TABLE workspaces ADD COLUMN environment TEXT NOT NULL DEFAULT '{}';
UPDATE workspaces
SET environment = json_object(
	'custom_image', image,
	'neutral_image', CASE WHEN image = '' THEN json('true') ELSE json('false') END,
	'variables', CASE WHEN json_valid(env) THEN json(env) ELSE json('{}') END,
	'setup_policy', json_object('script', setup_script)
);
ALTER TABLE runs ADD COLUMN tool_snapshot_id TEXT NOT NULL DEFAULT '';

CREATE TABLE tool_snapshots (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	member_id    TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	digest       TEXT NOT NULL,
	manifest     TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	UNIQUE (workspace_id, member_id, digest)
);
CREATE INDEX idx_tool_snapshots_scope ON tool_snapshots(member_id, workspace_id, created_at);

CREATE TABLE tool_heads (
	member_id    TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	snapshot_id  TEXT NOT NULL REFERENCES tool_snapshots(id),
	PRIMARY KEY (member_id, workspace_id)
);

CREATE TABLE pending_workspace_shells (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	member_id    TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	snapshot_id  TEXT NOT NULL DEFAULT '',
	staging_id   TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL
);
CREATE INDEX idx_pending_workspace_shells_scope
	ON pending_workspace_shells(member_id, workspace_id);
`,
	// v10: member-owned custom harness definitions, keyed by (member, name).
	// The definition column is an opaque JSON blob validated by callers.
	`
CREATE TABLE harness_definitions (
	member_id  TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	definition TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (member_id, name)
);
`,
}

// migrate brings the schema to the current version. It is idempotent:
// already-applied versions (tracked in schema_migrations) are skipped, so
// it is safe on fresh and existing databases alike, and safe under
// concurrent Open calls on the same file (each version's DDL runs at most
// once; see applyMigration). SQLITE_BUSY is retried with a bounded backoff
// because initial database creation (the journal-mode switch to WAL) takes
// exclusive locks that the busy handler does not always cover.
func migrate(db *sql.DB) error {
	const deadline = 5 * time.Second
	start := time.Now()
	for {
		err := migrateOnce(db)
		if err == nil || !isBusy(err) || time.Since(start) > deadline {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// isBusy reports whether err is SQLITE_BUSY or one of its extended codes.
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

func migrateOnce(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	var current int
	if err := db.QueryRow(
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`,
	).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("store: database schema version %d is newer than this binary supports (%d)",
			current, len(migrations))
	}

	for v := current + 1; v <= len(migrations); v++ {
		if err := applyMigration(db, v); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration in a transaction whose first statement
// claims the version row (acquiring the write lock before any DDL). When a
// concurrent opener already applied this version, the claim inserts zero
// rows and the DDL is skipped, so racing Opens on one file all succeed.
func applyMigration(db *sql.DB, version int) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin migration %d: %w", version, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	res, err := tx.Exec(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, unixepoch())
		 ON CONFLICT (version) DO NOTHING`,
		version,
	)
	if err != nil {
		return fmt.Errorf("store: record migration %d: %w", version, err)
	}
	claimed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: record migration %d: %w", version, err)
	}
	if claimed == 0 {
		return nil
	}
	if _, err := tx.Exec(migrations[version-1]); err != nil {
		return fmt.Errorf("store: apply migration %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %d: %w", version, err)
	}
	return nil
}
