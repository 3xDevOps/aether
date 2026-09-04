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
	// v11: runs gain reason, the last run.status reason (already sanitized
	// by the scheduler before persistence). Empty when the last transition
	// carried no reason.
	`
ALTER TABLE runs ADD COLUMN reason TEXT NOT NULL DEFAULT '';
`,
	// v12: the session layer is removed. Runs and everything that hung off
	// a session now hang off the workspace directly, and the workspace
	// absorbs the session's base branch and steer-others policy.
	//
	// Tables are rebuilt rather than altered because SQLite cannot drop a
	// column that carries a foreign key. The rebuild follows the v2
	// pattern - stage into a scratch table, drop, recreate under the
	// original name - because ALTER TABLE ... RENAME re-parses every other
	// table's schema, which fails while a referenced table is missing.
	// Deferred foreign keys hold the referencing tables together until the
	// migration commits.
	//
	// Collapse rules, chosen so nothing silently widens: the base branch
	// comes from the workspace's oldest session, the steer-others policy is
	// restrictive if any session was restrictive, merged budgets take the
	// tightest cap and drop any single session's admin override, and a
	// template name claimed by two sessions keeps the older definition
	// with its schedule remapped onto it.
	//
	// The events table is rebuilt here too. It is created by
	// internal/events.OpenSQLiteLog outside this ladder, but it lives in
	// the same file, and only this migration can still read the sessions
	// table it needs to rescope its rows. Keep eventsSchema in
	// internal/events/sqlitelog.go in step with the shape below.
	`
PRAGMA defer_foreign_keys = ON;

ALTER TABLE workspaces ADD COLUMN base_branch TEXT NOT NULL DEFAULT 'main';
ALTER TABLE workspaces ADD COLUMN steer_others TEXT NOT NULL DEFAULT '';
UPDATE workspaces SET base_branch = COALESCE((
	SELECT s.base_branch FROM sessions s
	WHERE s.workspace_id = workspaces.id AND s.base_branch <> ''
	ORDER BY s.created_at, s.id LIMIT 1
), 'main');
UPDATE workspaces SET steer_others = 'admins_only' WHERE EXISTS (
	SELECT 1 FROM sessions s
	WHERE s.workspace_id = workspaces.id AND s.steer_others = 'admins_only'
);

CREATE TABLE runs_migrate AS
	SELECT r.id, s.workspace_id, r.member_id, r.task, r.harness, r.mode, r.status,
	       r.reason, r.branch, r.worktree, r.protected, r.profile_snapshot_id,
	       r.tool_snapshot_id, r.created_at, r.started_at, r.finished_at
	FROM runs r JOIN sessions s ON s.id = r.session_id;
CREATE TABLE approvals_migrate AS
	SELECT a.id, s.workspace_id, a.run_id, a.source_id, a.action, a.detail,
	       a.decision, a.decided_by, a.created_at, a.decided_at
	FROM approvals a JOIN sessions s ON s.id = a.session_id;
CREATE TABLE run_costs_migrate AS
	SELECT c.run_id, s.workspace_id, c.member_id, c.input_tokens, c.output_tokens,
	       c.cost_usd, c.metered, c.recorded_at
	FROM run_costs c JOIN sessions s ON s.id = c.session_id;
-- Merging budgets must never widen one. The tightest cap wins, an admin
-- override on any single session is dropped rather than extended over the
-- whole workspace, and a warning threshold that ends up at or above the
-- surviving cap is cleared, because a warning that can only fire once
-- spending is already refused is noise. An admin re-grants the override
-- deliberately after the upgrade.
CREATE TABLE workspace_budgets_migrate AS
	SELECT s.workspace_id AS workspace_id,
	       COALESCE(MIN(NULLIF(b.limit_usd, 0)), 0) AS limit_usd,
	       CASE
	            WHEN COALESCE(MIN(NULLIF(b.warn_usd, 0)), 0)
	                 < COALESCE(MIN(NULLIF(b.limit_usd, 0)), 0)
	            THEN COALESCE(MIN(NULLIF(b.warn_usd, 0)), 0)
	            ELSE 0
	       END AS warn_usd,
	       0 AS override,
	       (SELECT b2.updated_by FROM session_budgets b2
	        JOIN sessions s2 ON s2.id = b2.session_id
	        WHERE s2.workspace_id = s.workspace_id
	        ORDER BY b2.updated_at DESC, b2.session_id DESC LIMIT 1) AS updated_by,
	       MAX(b.updated_at) AS updated_at
	FROM session_budgets b JOIN sessions s ON s.id = b.session_id
	GROUP BY s.workspace_id;
CREATE TABLE templates_migrate AS
	SELECT MIN(t.id) AS id, s.workspace_id AS workspace_id, t.name AS name,
	       t.task AS task, t.harness AS harness, t.mode AS mode, t.params AS params,
	       t.budget_usd AS budget_usd, t.created_at AS created_at
	FROM templates t JOIN sessions s ON s.id = t.session_id
	GROUP BY s.workspace_id, t.name;
-- Templates de-duplicate by (workspace, name), so a schedule may point at
-- a template ID that no longer exists. Remap it onto the survivor rather
-- than dropping it: a workspace must not silently stop running scheduled
-- work because of an upgrade. At most one schedule exists per template
-- and templates keep one row per name, so when two collapsed templates
-- both carried a schedule the oldest wins, matching the template rule.
CREATE TABLE schedules_migrate AS
	SELECT MIN(sc.id) AS id, keep.id AS template_id, sc.cron AS cron,
	       sc.member_id AS member_id, sc.created_at AS created_at,
	       sc.last_fired_at AS last_fired_at
	FROM schedules sc
	JOIN templates t ON t.id = sc.template_id
	JOIN sessions s ON s.id = t.session_id
	JOIN templates_migrate keep
	  ON keep.workspace_id = s.workspace_id AND keep.name = t.name
	GROUP BY keep.id;
CREATE TABLE run_messages_migrate AS
	SELECT m.id, s.workspace_id, m.from_run, m.to_run, m.body, m.delivery_token,
	       m.created_at, m.delivered_at, m.acked_at
	FROM run_messages m JOIN sessions s ON s.id = m.session_id;

-- The event log is created by internal/events.OpenSQLiteLog, not by this
-- ladder, so on a fresh database it does not exist yet. Creating it in the
-- old shape first makes the rebuild below uniform either way, and staging
-- it here is what lets it read the sessions table before that table goes.
CREATE TABLE IF NOT EXISTS events (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	id         TEXT NOT NULL UNIQUE,
	ts         INTEGER NOT NULL,
	session_id TEXT NOT NULL,
	run_id     TEXT NOT NULL,
	actor_id   TEXT NOT NULL,
	type       TEXT NOT NULL,
	payload    TEXT NOT NULL
);
-- The four session-scoped event type strings were renamed with the scope
-- they name. The decoder resolves a payload codec by exact type string, so
-- a row left under its old name is not merely mislabelled: reading it back
-- fails the whole page with "unknown event type".
CREATE TABLE events_migrate AS
	SELECT e.seq, e.id, e.ts, s.workspace_id, e.run_id, e.actor_id,
	       CASE e.type
	            WHEN 'session.presence' THEN 'workspace.presence'
	            WHEN 'session.approval' THEN 'workspace.approval'
	            WHEN 'session.timeline' THEN 'workspace.timeline'
	            WHEN 'session.budget'   THEN 'workspace.budget'
	            ELSE e.type
	       END AS type,
	       e.payload
	FROM events e JOIN sessions s ON s.id = e.session_id;

DROP TABLE schedules;
DROP TABLE run_messages;
DROP TABLE run_costs;
DROP TABLE session_budgets;
DROP TABLE approvals;
DROP TABLE templates;
DROP TABLE runs;
DROP TABLE sessions;

CREATE TABLE runs (
	id                  TEXT PRIMARY KEY,
	workspace_id        TEXT NOT NULL REFERENCES workspaces(id),
	member_id           TEXT NOT NULL REFERENCES members(id),
	task                TEXT NOT NULL,
	harness             TEXT NOT NULL,
	mode                TEXT NOT NULL,
	status              TEXT NOT NULL,
	reason              TEXT NOT NULL DEFAULT '',
	branch              TEXT NOT NULL,
	worktree            TEXT NOT NULL,
	protected           INTEGER NOT NULL DEFAULT 0,
	profile_snapshot_id TEXT NOT NULL DEFAULT '',
	tool_snapshot_id    TEXT NOT NULL DEFAULT '',
	created_at          INTEGER NOT NULL,
	started_at          INTEGER,
	finished_at         INTEGER
);
INSERT INTO runs SELECT * FROM runs_migrate;
CREATE INDEX idx_runs_workspace ON runs(workspace_id);
CREATE INDEX idx_runs_member ON runs(member_id);
CREATE INDEX idx_runs_status ON runs(status);

CREATE TABLE approvals (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	run_id       TEXT NOT NULL REFERENCES runs(id),
	source_id    TEXT NOT NULL DEFAULT '',
	action       TEXT NOT NULL,
	detail       TEXT NOT NULL DEFAULT '',
	decision     TEXT NOT NULL,
	decided_by   TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	decided_at   INTEGER
);
INSERT INTO approvals SELECT * FROM approvals_migrate;
CREATE INDEX idx_approvals_workspace ON approvals(workspace_id, decision);
CREATE UNIQUE INDEX idx_approvals_source ON approvals(run_id, source_id) WHERE source_id <> '';

CREATE TABLE run_costs (
	run_id        TEXT PRIMARY KEY REFERENCES runs(id),
	workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
	member_id     TEXT NOT NULL REFERENCES members(id),
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	metered       INTEGER NOT NULL DEFAULT 0,
	recorded_at   INTEGER NOT NULL
);
INSERT INTO run_costs SELECT * FROM run_costs_migrate;
CREATE INDEX idx_run_costs_workspace ON run_costs(workspace_id);
CREATE INDEX idx_run_costs_member ON run_costs(member_id);

CREATE TABLE workspace_budgets (
	workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
	limit_usd    REAL NOT NULL,
	warn_usd     REAL NOT NULL DEFAULT 0,
	override     INTEGER NOT NULL DEFAULT 0,
	updated_by   TEXT NOT NULL DEFAULT '',
	updated_at   INTEGER NOT NULL
);
INSERT INTO workspace_budgets SELECT * FROM workspace_budgets_migrate;

CREATE TABLE templates (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	task         TEXT NOT NULL,
	harness      TEXT NOT NULL,
	mode         TEXT NOT NULL,
	params       TEXT NOT NULL DEFAULT '{}',
	budget_usd   REAL NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL,
	UNIQUE (workspace_id, name)
);
INSERT INTO templates SELECT * FROM templates_migrate;

CREATE TABLE schedules (
	id            TEXT PRIMARY KEY,
	template_id   TEXT NOT NULL UNIQUE REFERENCES templates(id) ON DELETE CASCADE,
	cron          TEXT NOT NULL,
	member_id     TEXT NOT NULL REFERENCES members(id),
	created_at    INTEGER NOT NULL,
	last_fired_at INTEGER
);
INSERT INTO schedules SELECT * FROM schedules_migrate;

CREATE TABLE run_messages (
	id             TEXT PRIMARY KEY,
	workspace_id   TEXT NOT NULL REFERENCES workspaces(id),
	from_run       TEXT NOT NULL REFERENCES runs(id),
	to_run         TEXT NOT NULL REFERENCES runs(id),
	body           TEXT NOT NULL,
	delivery_token TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL,
	delivered_at   INTEGER,
	acked_at       INTEGER
);
INSERT INTO run_messages SELECT * FROM run_messages_migrate;
CREATE INDEX idx_run_messages_inbox ON run_messages(to_run, acked_at, id);

DROP TABLE events;
CREATE TABLE events (
	seq          INTEGER PRIMARY KEY AUTOINCREMENT,
	id           TEXT NOT NULL UNIQUE,
	ts           INTEGER NOT NULL,
	workspace_id TEXT NOT NULL,
	run_id       TEXT NOT NULL,
	actor_id     TEXT NOT NULL,
	type         TEXT NOT NULL,
	payload      TEXT NOT NULL
);
INSERT INTO events SELECT * FROM events_migrate;
CREATE INDEX idx_events_workspace_seq ON events (workspace_id, seq);

DROP TABLE runs_migrate;
DROP TABLE approvals_migrate;
DROP TABLE run_costs_migrate;
DROP TABLE workspace_budgets_migrate;
DROP TABLE templates_migrate;
DROP TABLE schedules_migrate;
DROP TABLE run_messages_migrate;
DROP TABLE events_migrate;
`,
	// v13: versioned workspace environment definitions. The definition
	// column is the JSON-encoded domain.EnvironmentDefinition; the version,
	// status, failure_detail, and timestamp columns are authoritative for
	// the fields they mirror. The partial unique index enforces the
	// one-active-version-per-workspace invariant in the schema itself.
	`
CREATE TABLE environment_definitions (
	workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	version        INTEGER NOT NULL,
	definition     TEXT NOT NULL,
	status         TEXT NOT NULL,
	failure_detail TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL,
	PRIMARY KEY (workspace_id, version)
);
CREATE UNIQUE INDEX idx_environment_definitions_active
	ON environment_definitions(workspace_id) WHERE status = 'active';
`,
	// v14: the server's own update state. One row, id 1: at most one
	// pending self-update (a second request replaces it) plus the outcome
	// of the last attempt, so both survive the restart the update causes.
	`
CREATE TABLE server_update_state (
	id                   INTEGER PRIMARY KEY CHECK (id = 1),
	pending_version      TEXT NOT NULL DEFAULT '',
	pending_requested_by TEXT NOT NULL DEFAULT '',
	pending_requested_at INTEGER NOT NULL DEFAULT 0,
	last_version         TEXT NOT NULL DEFAULT '',
	last_outcome         TEXT NOT NULL DEFAULT '',
	last_detail          TEXT NOT NULL DEFAULT '',
	last_at              INTEGER NOT NULL DEFAULT 0
);
`,
	// v15: one persistent home per member supersedes workspace tool
	// snapshots and pending workspace shells. tool_heads references
	// tool_snapshots, so the child table drops first: DROP TABLE runs an
	// implicit DELETE and the FK would otherwise reject the parent drop.
	`
DROP TABLE tool_heads;
DROP TABLE pending_workspace_shells;
DROP TABLE tool_snapshots;
ALTER TABLE runs DROP COLUMN tool_snapshot_id;
CREATE TABLE member_terminals (
	member_id    TEXT PRIMARY KEY,
	container_id TEXT NOT NULL,
	image        TEXT NOT NULL,
	started_at   INTEGER NOT NULL
);
`,
	// v16: runs gain the latest title reported by the agent's terminal.
	`
ALTER TABLE runs ADD COLUMN title TEXT NOT NULL DEFAULT '';
`,
	// v17: published run commit metadata. The SHA is empty until the first
	// branch publication; the timestamp remains nullable for that state.
	`
ALTER TABLE runs ADD COLUMN last_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN last_commit_at INTEGER;
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
