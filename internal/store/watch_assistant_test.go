package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// The assistant's follows become assistant watches that remember what each
// follow last announced; follows of ended runs are dropped with the table.
func TestAssistantFollowsMigrateToWatches(t *testing.T) {
	t.Parallel()
	const previous = 89
	path := filepath.Join(t.TempDir(), "aether.db")
	raw, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=foreign_keys(1)&_pragma=synchronous(OFF)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err = raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for v := 1; v <= previous; v++ {
		if _, err = raw.Exec(migrations[v-1]); err != nil {
			t.Fatalf("apply v%d: %v", v, err)
		}
		if _, err = raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at) VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at, environment, base_branch, steer_others, origin) VALUES ('w1', 'proj', 1, '{}', 'main', '', '');
		INSERT INTO runs (id, workspace_id, member_id, account_member_id, task, harness, mode, status, reason, activity, branch, worktree, created_at, review, review_changed_at, status_changed_at)
			VALUES ('asking', 'w1', 'm1', 'm1', 'a', 'claude', 'acp', 'needs-attention', 'blocked: x', 'needs-you', 'b1', 'w1', 1, '', NULL, 5),
			       ('parked', 'w1', 'm1', 'm1', 'a', 'claude', 'acp', 'needs-attention', 'agent reported success', 'finished', 'b2', 'w2', 1, 'ready', 7, 6),
			       ('quiet', 'w1', 'm1', 'm1', 'a', 'claude', 'acp', 'running', '', 'working', 'b3', 'w3', 1, '', NULL, NULL),
			       ('reported', 'w1', 'm1', 'm1', 'a', 'claude', 'acp', 'completed', 'agent reported success; retained container', '', 'b5', 'w5', 1, 'ready', 8, 9),
			       ('closed', 'w1', 'm1', 'm1', 'a', 'claude', 'acp', 'completed', 'closed', '', 'b6', 'w6', 1, '', NULL, 10),
			       ('ended', 'w1', 'm1', 'm1', 'a', 'claude', 'acp', 'merged', '', '', 'b4', 'w4', 1, '', NULL, NULL);
		INSERT INTO assistant_follows (member_id, run_id, created_at, notified_key) VALUES
			('m1', 'asking', 100, 'attention:needs-attention|needs-you|blocked: x|5'),
			('m1', 'parked', 100, 'finished:needs-attention|finished|agent reported success|6|7'),
			('m1', 'reported', 100, 'finished:completed'),
			('m1', 'closed', 100, 'finished:completed'),
			('m1', 'quiet', 100, ''),
			('m1', 'ended', 100, 'finished:merged');
	`, testKey(t, "")); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	watches, err := db.ListWatches(context.Background(), WatchFilter{MemberID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	fired := map[domain.RunID]map[string]string{}
	for _, w := range watches {
		if w.Source != "assistant" || w.State != "active" || w.TargetKind != "run" || w.WorkspaceID != "w1" ||
			len(w.Conditions) != 2 || w.Conditions[0] != "needs_you" || w.Conditions[1] != "finished" ||
			w.IdempotencyKey != "assistant:"+string(w.RunID) || w.CreatedAt.Unix() != 100 || !w.ExpiresAt.After(w.UpdatedAt) || w.NextCheckAt == nil {
			t.Fatalf("migrated watch = %+v", w)
		}
		fired[w.RunID] = w.Fired
	}
	want := map[domain.RunID]map[string]string{
		"asking":   {"needs_you": "status:needs-attention|needs-you|blocked: x|5"},
		"parked":   {"finished": "reported|live|6"},
		"reported": {"finished": "reported|9"},
		"closed":   {"finished": "completed"},
		"quiet":    {},
	}
	if len(fired) != len(want) {
		t.Fatalf("migrated watches fired %v, want %v", fired, want)
	}
	for run, keys := range want {
		if len(fired[run]) != len(keys) || fired[run]["needs_you"] != keys["needs_you"] || fired[run]["finished"] != keys["finished"] {
			t.Fatalf("watch on %s fired %v, want %v", run, fired[run], keys)
		}
	}
	var tables int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'assistant_follows'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("assistant_follows tables = %d, %v; want it dropped", tables, err)
	}
}
