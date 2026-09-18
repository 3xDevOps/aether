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
	workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
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
	// column is the JSON-encoded environment definition; the version,
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
	// v18: the harness conversation ID pinned at launch, so a relaunch
	// resumes the interrupted run's own conversation by name. Empty on
	// every existing row: those relaunch with the old best-effort flag.
	`
ALTER TABLE runs ADD COLUMN harness_session_id TEXT NOT NULL DEFAULT '';
`,
	// v19: workspace environments retain only variables and setup policy.
	// Backfill rows that still rely on the legacy columns before dropping
	// those columns and the obsolete environment definition table.
	`
UPDATE workspaces
SET environment = json_object(
	'variables', CASE
		WHEN json_valid(env) AND json_type(env) = 'object' THEN json(env)
		ELSE json('{}')
	END,
	'setup_policy', json_object('script', setup_script)
)
WHERE environment IN ('', '{}');
DROP TABLE environment_definitions;
ALTER TABLE workspaces DROP COLUMN image;
ALTER TABLE workspaces DROP COLUMN env;
ALTER TABLE workspaces DROP COLUMN setup_script;
`,
	// v20: saved per-member environment image references.
	`
ALTER TABLE members ADD COLUMN image TEXT NOT NULL DEFAULT '';
`,
	// v21: opt-in member account sharing. Runs retain their authenticated
	// owner in member_id and separately pin the environment/vendor account
	// they used. Existing rows fall back to member_id in domain.Run.
	`
ALTER TABLE runs ADD COLUMN account_member_id TEXT REFERENCES members(id);
UPDATE runs SET account_member_id = member_id;
CREATE TABLE account_shares (
	owner_member_id   TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	grantee_member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	created_at        INTEGER NOT NULL,
	PRIMARY KEY (owner_member_id, grantee_member_id),
	CHECK (owner_member_id <> grantee_member_id)
);
CREATE INDEX idx_account_shares_grantee ON account_shares(grantee_member_id);
`,
	// v22: per-member git identity, and the set of members other than the
	// owner who steered a run - the co-authors its commits credit.
	`
ALTER TABLE members ADD COLUMN git_name TEXT NOT NULL DEFAULT '';
ALTER TABLE members ADD COLUMN git_email TEXT NOT NULL DEFAULT '';
CREATE TABLE run_steerers (
	run_id    TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	PRIMARY KEY (run_id, member_id)
);
`,
	// v23: the upstream git URL a workspace's run checkouts push to.
	`
ALTER TABLE workspaces ADD COLUMN origin TEXT NOT NULL DEFAULT '';
`,
	// v24: index per-workspace cost history for filtering and detailed
	// listing.
	`
CREATE INDEX idx_run_costs_workspace_recorded
	ON run_costs(workspace_id, recorded_at, run_id);
`,
	// v25: one optional upstream mirror configuration per workspace. Private
	// key material is operator-managed and intentionally never persisted.
	`
CREATE TABLE workspace_mirrors (
	workspace_id     TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
	source_url       TEXT NOT NULL,
	source_identity  TEXT NOT NULL,
	branch           TEXT NOT NULL,
	auth             TEXT NOT NULL,
	generation       INTEGER NOT NULL,
	status           TEXT NOT NULL,
	observed_commit  TEXT NOT NULL DEFAULT '',
	accepted_commit  TEXT NOT NULL DEFAULT '',
	key_fingerprint  TEXT NOT NULL DEFAULT '',
	last_error       TEXT NOT NULL DEFAULT '',
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL,
	last_attempt_at  INTEGER,
	last_success_at  INTEGER
);
`,
	// v26: durable provenance for the base used by each run. Text fields are
	// empty for rows created before provenance tracking; the nullable
	// timestamp preserves the domain zero time for those rows.
	`
ALTER TABLE runs ADD COLUMN base_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN base_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN base_source TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN base_checked_at INTEGER;
`,
	// v27: durable run-room messages. Bodies and attachments remain bounded
	// by the collaboration service; this table stores their authoritative
	// state, idempotency identity, and immutable actor display snapshot.
	`
CREATE TABLE room_messages (
	id                 TEXT PRIMARY KEY,
	workspace_id       TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	run_id             TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	actor_id           TEXT NOT NULL,
	actor_display_name TEXT NOT NULL,
	kind               TEXT NOT NULL CHECK (kind IN ('comment', 'steer_request', 'question', 'reply', 'system')),
	body               TEXT NOT NULL,
	attachments        TEXT NOT NULL DEFAULT '[]',
	anchor             TEXT,
	correlation_id     TEXT NOT NULL DEFAULT '',
	idempotency_key    TEXT NOT NULL,
	state              TEXT NOT NULL CHECK (state IN ('queued', 'sent', 'not_sent', 'uncertain', 'denied', 'cancelled')),
	deliver_after      INTEGER,
	decided_by         TEXT NOT NULL DEFAULT '',
	decided_at         INTEGER,
	delivered_at       INTEGER,
	failure            TEXT,
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL,
	UNIQUE (actor_id, run_id, idempotency_key)
);
CREATE INDEX idx_room_messages_scope
	ON room_messages(workspace_id, run_id, created_at DESC, id DESC);
CREATE INDEX idx_room_messages_state
	ON room_messages(workspace_id, run_id, state, created_at DESC, id DESC);
CREATE INDEX idx_room_messages_correlation
	ON room_messages(workspace_id, correlation_id, created_at DESC)
	WHERE correlation_id <> '';
`,
	// v28: durable evidence packets and handoff publication state. Evidence
	// metadata points at retained objects but never stores their contents or
	// host paths. Handoff rows are an outbox so publication can resume after
	// a restart without recapturing evidence.
	`
CREATE TABLE evidence_packets (
	id                       TEXT PRIMARY KEY,
	workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	run_id                   TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	origin_kind              TEXT NOT NULL DEFAULT 'human' CHECK (origin_kind IN ('human', 'run', 'server')),
	origin_id                TEXT NOT NULL DEFAULT '',
	owner_id                 TEXT NOT NULL DEFAULT '',
	creator_id               TEXT NOT NULL DEFAULT '',
	publication_owner        TEXT NOT NULL DEFAULT 'generic' CHECK (publication_owner IN ('generic', 'coord_report')),
	trigger                  TEXT NOT NULL CHECK (trigger IN ('finish', 'handoff', 'report')),
	objective                TEXT NOT NULL,
	captured_at              INTEGER NOT NULL,
	expires_at               INTEGER,
	availability             TEXT NOT NULL DEFAULT 'available' CHECK (availability IN ('available', 'expired')),
	expired_at               INTEGER,
	event_boundary           INTEGER NOT NULL DEFAULT 0,
	base_revision            TEXT NOT NULL DEFAULT '',
	retained_revision        TEXT NOT NULL DEFAULT '',
	changed_files            TEXT NOT NULL DEFAULT '[]',
	sources                  TEXT NOT NULL DEFAULT '[]',
	related_room_message_ids TEXT NOT NULL DEFAULT '[]',
	unresolved_facts         TEXT NOT NULL DEFAULT '[]',
	next_action              TEXT NOT NULL DEFAULT '',
	provenance               TEXT NOT NULL DEFAULT '',
	idempotency_key          TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	updated_at               INTEGER NOT NULL,
	UNIQUE (origin_kind, origin_id, run_id, idempotency_key)
);
CREATE INDEX idx_evidence_packets_scope
	ON evidence_packets(workspace_id, run_id, captured_at DESC, id DESC);
CREATE INDEX idx_evidence_packets_trigger
	ON evidence_packets(workspace_id, trigger, captured_at DESC, id DESC);
CREATE INDEX idx_evidence_packets_expiry
	ON evidence_packets(availability, expires_at, id);
CREATE TABLE evidence_publications (
	packet_id       TEXT PRIMARY KEY REFERENCES evidence_packets(id) ON DELETE CASCADE,
	event_id        TEXT NOT NULL UNIQUE,
	attempts        INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL,
	last_error      TEXT NOT NULL DEFAULT '',
	published_at    INTEGER,
	created_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL
);
CREATE INDEX idx_evidence_publications_pending
	ON evidence_publications(published_at, next_attempt_at, created_at, packet_id);
CREATE TABLE evidence_staging (
	id             TEXT PRIMARY KEY,
	workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	run_id         TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	origin_kind    TEXT NOT NULL DEFAULT 'human' CHECK (origin_kind IN ('human', 'run', 'server')),
	origin_id      TEXT NOT NULL DEFAULT '',
	creator_id     TEXT NOT NULL DEFAULT '',
	idempotency_key TEXT NOT NULL,
	expires_at     INTEGER NOT NULL,
	created_at     INTEGER NOT NULL
);
CREATE INDEX idx_evidence_staging_expiry
	ON evidence_staging(expires_at, created_at, id);

CREATE TABLE handoff_outbox (
	id                 TEXT PRIMARY KEY,
	workspace_id       TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	run_id             TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	actor_id           TEXT NOT NULL,
	from_member_id     TEXT NOT NULL,
	to_member_id       TEXT NOT NULL,
	evidence_packet_id TEXT NOT NULL DEFAULT '',
	evidence_state     TEXT NOT NULL CHECK (evidence_state IN ('pending', 'available', 'unavailable')),
	publication_state  TEXT NOT NULL CHECK (publication_state IN ('pending', 'published')),
	timeline_state     TEXT NOT NULL DEFAULT 'pending' CHECK (timeline_state IN ('pending', 'published')),
	coauthor_state     TEXT NOT NULL DEFAULT 'pending' CHECK (coauthor_state IN ('pending', 'published')),
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL,
	published_at       INTEGER
);
CREATE INDEX idx_handoff_outbox_pending ON handoff_outbox(publication_state, created_at);
CREATE INDEX idx_handoff_outbox_phases
	ON handoff_outbox(timeline_state, coauthor_state, evidence_state, publication_state, created_at);
`,
	// v29: coordination metadata, durable one-report reservations, and
	// persisted peer accounting. Existing mailbox rows retain ordinary
	// message defaults and no idempotency key.
	`
ALTER TABLE run_messages ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
CHECK (kind IN ('message', 'question', 'reply'));
ALTER TABLE run_messages ADD COLUMN correlation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE run_messages ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_run_messages_idempotency
	ON run_messages(from_run, idempotency_key)
	WHERE idempotency_key <> '';
CREATE INDEX idx_run_messages_correlation
	ON run_messages(correlation_id, created_at)
	WHERE correlation_id <> '';

CREATE TABLE coord_peers (
	from_run   TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	to_run     TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (from_run, to_run),
	CHECK (from_run <> to_run)
);
CREATE INDEX idx_coord_peers_from ON coord_peers(from_run, created_at, to_run);

CREATE TABLE coord_reports (
	id              TEXT PRIMARY KEY,
	workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	run_id          TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	outcome         TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'blocked')),
	summary         TEXT NOT NULL,
	next_action     TEXT NOT NULL DEFAULT '',
	evidence_refs   TEXT NOT NULL DEFAULT '[]',
	input_evidence_refs TEXT NOT NULL DEFAULT '[]',
	idempotency_key TEXT NOT NULL,
	state           TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'finalized')),
	created_at      INTEGER NOT NULL,
	finalized_at    INTEGER,
	published_at    INTEGER,
	UNIQUE (run_id),
	UNIQUE (run_id, idempotency_key)
);
CREATE INDEX idx_coord_reports_run
	ON coord_reports(workspace_id, run_id, created_at DESC, id DESC);
CREATE TABLE coord_report_publications (
	report_id         TEXT PRIMARY KEY REFERENCES coord_reports(id) ON DELETE CASCADE,
	event_id          TEXT NOT NULL UNIQUE,
	publication_state TEXT NOT NULL DEFAULT 'pending' CHECK (publication_state IN ('pending', 'published')),
	attempts          INTEGER NOT NULL DEFAULT 0,
	next_attempt_at   INTEGER NOT NULL DEFAULT 0,
	last_error        TEXT NOT NULL DEFAULT '',
	quarantined_at    INTEGER,
	quarantine_error  TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	published_at      INTEGER
);
CREATE INDEX idx_coord_report_publications_due
	ON coord_report_publications(publication_state, quarantined_at, next_attempt_at, created_at, report_id);

CREATE TABLE coord_audit_publications (
	message_id        TEXT PRIMARY KEY,
	event_id          TEXT NOT NULL UNIQUE,
	workspace_id      TEXT NOT NULL,
	from_run          TEXT NOT NULL,
	to_run            TEXT NOT NULL,
	body              TEXT NOT NULL,
	publication_state TEXT NOT NULL DEFAULT 'pending' CHECK (publication_state IN ('pending', 'published')),
	attempts          INTEGER NOT NULL DEFAULT 0,
	next_attempt_at   INTEGER NOT NULL DEFAULT 0,
	last_error        TEXT NOT NULL DEFAULT '',
	quarantined_at    INTEGER,
	quarantine_error  TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	published_at      INTEGER
);
CREATE INDEX idx_coord_audit_publications_due
	ON coord_audit_publications(publication_state, quarantined_at, next_attempt_at, created_at, message_id);

-- Published rows are only an event-log reconciliation cache. Pending and
-- quarantined rows retain their immutable projections until an operator can
-- inspect or retry them, even after mailbox/run retirement.
DELETE FROM coord_audit_publications
WHERE publication_state = 'published'
  AND NOT EXISTS (SELECT 1 FROM run_messages WHERE run_messages.id = coord_audit_publications.message_id);

UPDATE coord_reports SET published_at = NULL WHERE state = 'finalized';
INSERT OR IGNORE INTO coord_report_publications (report_id, event_id, publication_state, created_at, published_at)
	SELECT id, 'coord-report:' || id, 'pending', COALESCE(finalized_at, created_at), NULL
	FROM coord_reports
	WHERE state = 'finalized';
`,
	// v30: fold a deleted run's cost into its workspace and member instead
	// of losing it. run_costs.run_id is PRIMARY KEY REFERENCES runs(id), so
	// its row cannot outlive the run; one run_cost_deletions row per
	// (workspace, member) accumulates what DeleteRun folds in, and the cost
	// summaries add it back so counted spend and budget admission cannot
	// change just because a run was deleted. member_id carries no foreign
	// key: this is history keyed by id, and it must survive the member
	// being removed (see store.DeleteMember).
	`
CREATE TABLE run_cost_deletions (
	workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	member_id     TEXT NOT NULL,
	runs          INTEGER NOT NULL DEFAULT 0,
	metered       INTEGER NOT NULL DEFAULT 0,
	unmetered     INTEGER NOT NULL DEFAULT 0,
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	PRIMARY KEY (workspace_id, member_id)
);
`,
	// v31: archived_at hides a finished run from the board; its data is
	// kept. Nullable with no default, so existing rows read as not
	// archived.
	`
ALTER TABLE runs ADD COLUMN archived_at INTEGER;
`,
	// v32: durable missions, immutable task specifications, fenced attempts,
	// exact-version submissions, and acceptance records. Mission state is
	// deliberately separate from Store so compatibility stores can opt in.
	`
CREATE TABLE missions (
	id                        TEXT PRIMARY KEY,
	workspace_id              TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	objective                 TEXT NOT NULL,
	accountable_human_id      TEXT NOT NULL REFERENCES members(id),
	integrator_account_member_id TEXT NOT NULL DEFAULT '',
	integrator_harness        TEXT NOT NULL DEFAULT '',
	integrator_mode           TEXT NOT NULL DEFAULT 'headless',
	execution_choices         TEXT NOT NULL DEFAULT '[]',
	max_concurrent_attempts   INTEGER NOT NULL CHECK (max_concurrent_attempts > 0),
	max_total_attempts        INTEGER NOT NULL CHECK (max_total_attempts > 0),
	current_integrator_run_id TEXT,
	integrator_generation     INTEGER NOT NULL DEFAULT 1,
	accepted_set_version      INTEGER NOT NULL DEFAULT 0,
	idempotency_key           TEXT NOT NULL,
	created_at                INTEGER NOT NULL,
	updated_at                INTEGER NOT NULL,
	CHECK (json_valid(execution_choices)),
	UNIQUE (workspace_id, idempotency_key)
);
CREATE INDEX idx_missions_workspace ON missions(workspace_id, created_at, id);

CREATE TABLE mission_tasks (
	id               TEXT PRIMARY KEY,
	mission_id       TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	current_revision INTEGER NOT NULL DEFAULT 1 CHECK (current_revision > 0),
	abandoned_at     INTEGER,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL
);
CREATE INDEX idx_mission_tasks_mission ON mission_tasks(mission_id, created_at, id);

CREATE TABLE mission_task_revisions (
	task_id               TEXT NOT NULL REFERENCES mission_tasks(id) ON DELETE CASCADE,
	revision              INTEGER NOT NULL CHECK (revision > 0),
	title                 TEXT NOT NULL,
	objective             TEXT NOT NULL,
	scope                 TEXT NOT NULL DEFAULT '{}',
	evidence_requirements TEXT NOT NULL DEFAULT '[]',
	status                TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'superseded', 'abandoned')),
	proposed_by_run_id    TEXT NOT NULL DEFAULT '',
	supersedes_revision   INTEGER NOT NULL DEFAULT 0,
	created_at            INTEGER NOT NULL,
	accepted_at           INTEGER,
	PRIMARY KEY (task_id, revision),
	CHECK (json_valid(scope)),
	CHECK (json_valid(evidence_requirements))
);
CREATE INDEX idx_mission_task_revisions_status
	ON mission_task_revisions(task_id, status, revision DESC);

CREATE TABLE mission_task_dependencies (
	task_id             TEXT NOT NULL REFERENCES mission_tasks(id) ON DELETE CASCADE,
	task_revision       INTEGER NOT NULL,
	depends_on_task_id  TEXT NOT NULL REFERENCES mission_tasks(id) ON DELETE CASCADE,
	depends_on_revision INTEGER NOT NULL CHECK (depends_on_revision > 0),
	output_ref          TEXT NOT NULL DEFAULT '',
	created_at          INTEGER NOT NULL,
	PRIMARY KEY (task_id, task_revision, depends_on_task_id, depends_on_revision),
	CHECK (task_id <> depends_on_task_id),
	FOREIGN KEY (task_id, task_revision)
		REFERENCES mission_task_revisions(task_id, revision) ON DELETE CASCADE
);
CREATE INDEX idx_mission_task_dependencies_source
	ON mission_task_dependencies(depends_on_task_id, depends_on_revision);

CREATE TABLE mission_attempts (
	id                    TEXT PRIMARY KEY,
	mission_id            TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	task_id               TEXT NOT NULL REFERENCES mission_tasks(id) ON DELETE CASCADE,
	task_revision         INTEGER NOT NULL,
	number                INTEGER NOT NULL CHECK (number > 0),
	dispatch_key          TEXT NOT NULL,
	harness               TEXT NOT NULL DEFAULT '',
	mode                  TEXT NOT NULL DEFAULT 'headless',
	state                 TEXT NOT NULL CHECK (state IN ('reserved', 'launching', 'running', 'unknown', 'submitted', 'completed', 'failed', 'cancelled', 'superseded', 'abandoned')),
	run_id                TEXT,
	actor_run_id          TEXT NOT NULL DEFAULT '',
	authorizing_human_id  TEXT NOT NULL DEFAULT '',
	run_owner_id          TEXT NOT NULL DEFAULT '',
	account_owner_id      TEXT NOT NULL DEFAULT '',
	authority_generation  INTEGER NOT NULL DEFAULT 0,
	integrator_generation INTEGER NOT NULL DEFAULT 0,
	created_at            INTEGER NOT NULL,
	reserved_at           INTEGER NOT NULL,
	started_at            INTEGER,
	finished_at           INTEGER,
	UNIQUE (mission_id, dispatch_key),
	FOREIGN KEY (task_id, task_revision)
		REFERENCES mission_task_revisions(task_id, revision)
);
CREATE INDEX idx_mission_attempts_active
	ON mission_attempts(mission_id, state, created_at, id);
CREATE INDEX idx_mission_attempts_task
	ON mission_attempts(task_id, number DESC, created_at DESC);

CREATE TABLE mission_submissions (
	id                    TEXT PRIMARY KEY,
	mission_id            TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	task_id               TEXT NOT NULL REFERENCES mission_tasks(id) ON DELETE CASCADE,
	task_revision         INTEGER NOT NULL,
	attempt_id            TEXT NOT NULL REFERENCES mission_attempts(id) ON DELETE CASCADE,
	workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
	run_id                TEXT NOT NULL REFERENCES runs(id),
	evidence_ref          TEXT NOT NULL,
	retained_revision     TEXT NOT NULL,
	evidence               TEXT NOT NULL DEFAULT '[]',
	state                 TEXT NOT NULL CHECK (state IN ('proposed', 'accepted', 'rejected', 'superseded', 'abandoned')),
	proposed_by_run_id    TEXT NOT NULL,
	integrator_generation INTEGER NOT NULL,
	created_at            INTEGER NOT NULL,
	decided_at            INTEGER,
	decision_by_run_id    TEXT NOT NULL DEFAULT '',
	FOREIGN KEY (task_id, task_revision)
		REFERENCES mission_task_revisions(task_id, revision),
	UNIQUE (attempt_id)
);
CREATE INDEX idx_mission_submissions_task
	ON mission_submissions(task_id, task_revision, state, created_at DESC);

CREATE TABLE mission_acceptances (
	submission_id          TEXT PRIMARY KEY REFERENCES mission_submissions(id),
	mission_id             TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	task_id                TEXT NOT NULL REFERENCES mission_tasks(id) ON DELETE CASCADE,
	task_revision          INTEGER NOT NULL,
	accepted_set_version   INTEGER NOT NULL,
	integrator_generation  INTEGER NOT NULL,
	accepted_by_run_id     TEXT NOT NULL,
	accepted_at            INTEGER NOT NULL,
	FOREIGN KEY (task_id, task_revision)
		REFERENCES mission_task_revisions(task_id, revision),
	UNIQUE (mission_id, accepted_set_version),
	UNIQUE (task_id, task_revision)
);
CREATE INDEX idx_mission_acceptances_mission
	ON mission_acceptances(mission_id, accepted_set_version);

CREATE TABLE mission_worker_takeovers (
	worker_run_id TEXT PRIMARY KEY,
	mission_id    TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	task_id       TEXT NOT NULL REFERENCES mission_tasks(id) ON DELETE CASCADE,
	attempt_id    TEXT NOT NULL REFERENCES mission_attempts(id) ON DELETE CASCADE,
	member_id     TEXT NOT NULL,
	active        INTEGER NOT NULL CHECK (active IN (0, 1)),
	generation    INTEGER NOT NULL DEFAULT 1,
	updated_at    INTEGER NOT NULL
);
CREATE INDEX idx_mission_worker_takeovers_mission
	ON mission_worker_takeovers(mission_id, active, updated_at);
CREATE TABLE mission_control_changes (
	mission_id          TEXT PRIMARY KEY REFERENCES missions(id) ON DELETE CASCADE,
	generation          INTEGER NOT NULL DEFAULT 0,
	published_generation INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE mission_integrator_replacements (
	mission_id       TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	idempotency_key  TEXT NOT NULL,
	account_member_id TEXT NOT NULL,
	harness           TEXT NOT NULL,
	mode              TEXT NOT NULL,
	generation       INTEGER NOT NULL,
	run_id           TEXT NOT NULL DEFAULT '',
	created_at       INTEGER NOT NULL,
	PRIMARY KEY (mission_id, idempotency_key)
);
CREATE TABLE mission_create_receipts (
	workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	idempotency_key     TEXT NOT NULL,
	mission_id          TEXT NOT NULL UNIQUE REFERENCES missions(id) ON DELETE CASCADE,
	objective           TEXT NOT NULL,
	accountable_human_id TEXT NOT NULL,
	integrator_account_member_id TEXT NOT NULL,
	integrator_harness  TEXT NOT NULL,
	integrator_mode     TEXT NOT NULL,
	execution_choices   TEXT NOT NULL,
	max_concurrent_attempts INTEGER NOT NULL,
	max_total_attempts  INTEGER NOT NULL,
	created_at          INTEGER NOT NULL,
	PRIMARY KEY (workspace_id, idempotency_key)
);
CREATE INDEX idx_mission_integrator_replacements_mission
	ON mission_integrator_replacements(mission_id, generation);
`,
	`
CREATE TABLE mission_mutation_receipts (
	mission_id       TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
	operation        TEXT NOT NULL,
	idempotency_key  TEXT NOT NULL,
	payload          TEXT NOT NULL,
	result_id        TEXT NOT NULL DEFAULT '',
	result_revision  INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL,
	PRIMARY KEY (mission_id, operation, idempotency_key)
);
CREATE INDEX idx_mission_mutation_receipts_result
	ON mission_mutation_receipts(result_id);
`,
	`
ALTER TABLE mission_submissions ADD COLUMN scope_violations TEXT NOT NULL DEFAULT '[]';
ALTER TABLE mission_acceptances ADD COLUMN scope_disposition TEXT NOT NULL DEFAULT '';
`,
	`
ALTER TABLE missions ADD COLUMN integrator_authorizing_human_id TEXT NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN integrator_run_owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mission_integrator_replacements ADD COLUMN authorizing_human_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mission_integrator_replacements ADD COLUMN run_owner_id TEXT NOT NULL DEFAULT '';
`,
	`
ALTER TABLE mission_attempts ADD COLUMN cancel_requested_at INTEGER;
ALTER TABLE mission_attempts ADD COLUMN cancellation_actor_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mission_attempts ADD COLUMN cancellation_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mission_attempts ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
`,
	`
ALTER TABLE mission_create_receipts ADD COLUMN initial_run_id TEXT NOT NULL DEFAULT '';
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
