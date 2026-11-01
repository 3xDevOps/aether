package store

import (
	"path/filepath"
	"testing"
)

func TestSwarmRemovalMigrationFromV98TurnsSwarmsIntoParentsWithChildren(t *testing.T) {
	t.Parallel()
	const previous = 98
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, previous)
	if _, execErr := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1), ('m2', 'Bo', ?, '#3cb44b', 'collaborator', 1);
		INSERT INTO workspaces (id, name, environment, created_at)
			VALUES ('w1', 'proj', '{}', 1);
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at, swarm_run, model)
			VALUES ('done-int', 'w1', 'm1', 'integrate', 'claude', 'acp', 'completed', 'b0', 'w0', 10, 1, ''),
			       ('done-a', 'w1', 'm1', 'build the parser', 'codex', 'acp', 'completed', 'b1', 'w1', 11, 1, 'gpt'),
			       ('done-b', 'w1', 'm1', 'write docs' || char(10) || 'for it', 'claude', 'tui', 'failed', 'b2', 'w2', 12, 1, ''),
			       ('old-int', 'w1', 'm2', 'first integrator', 'claude', 'acp', 'abandoned', 'b3', 'w3', 20, 1, ''),
			       ('live-int', 'w1', 'm1', 'integrate again', 'claude', 'acp', 'running', 'b4', 'w4', 21, 1, ''),
			       ('live-a', 'w1', 'm1', 'fix the bug', 'claude', 'acp', 'running', 'b5', 'w5', 22, 1, ''),
			       ('nested', 'w1', 'm1', 'review', 'claude', 'acp', 'running', 'b6', 'w6', 23, 0, ''),
			       ('lone', 'w1', 'm1', 'unattached worker', 'claude', 'acp', 'completed', 'b7', 'w7', 24, 1, '');
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, idempotency_key, created_at, updated_at,
		                      phase, current_integrator_run_id)
			VALUES ('mis-done', 'w1', 'ship', 'm1', 'k1', 10, 10, 'completed', 'done-int'),
			       ('mis-live', 'w1', 'ship more', 'm1', 'k2', 20, 20, 'active', 'live-int'),
			       ('mis-bare', 'w1', 'no integrator', 'm1', 'k3', 30, 30, 'cancelled', NULL);
		INSERT INTO mission_create_receipts (workspace_id, idempotency_key, mission_id, objective, accountable_human_id,
		                                     integrator_account_member_id, integrator_harness, integrator_mode,
		                                     execution_choices, created_at, initial_run_id)
			VALUES ('w1', 'k2', 'mis-live', 'ship more', 'm1', 'm1', 'claude', 'acp', '[]', 20, 'old-int');
		INSERT INTO mission_integrator_replacements (mission_id, idempotency_key, account_member_id, harness, mode,
		                                             generation, run_id, created_at)
			VALUES ('mis-live', 'r1', 'm1', 'claude', 'acp', 2, 'live-int', 21);
		INSERT INTO mission_tasks (id, mission_id, created_at, updated_at)
			VALUES ('t1', 'mis-done', 10, 10), ('t2', 'mis-done', 10, 10), ('t3', 'mis-live', 22, 22), ('t4', 'mis-bare', 30, 30);
		INSERT INTO mission_task_revisions (task_id, revision, title, objective, status, created_at)
			VALUES ('t1', 1, 'Parser', 'o', 'accepted', 10), ('t2', 1, '', 'o', 'accepted', 10),
			       ('t3', 1, 'Fix the bug', 'o', 'accepted', 22), ('t4', 1, 'Lone', 'o', 'accepted', 30);
		INSERT INTO mission_attempts (id, mission_id, task_id, task_revision, number, dispatch_key, state, run_id,
		                              created_at, reserved_at)
			VALUES ('a1', 'mis-done', 't1', 1, 1, 'd1', 'completed', 'done-a', 11, 11),
			       ('a2', 'mis-done', 't2', 1, 1, 'd2', 'failed', 'done-b', 12, 12),
			       ('a3', 'mis-live', 't3', 1, 1, 'd3', 'running', 'live-a', 22, 22),
			       ('a4', 'mis-bare', 't4', 1, 1, 'd4', 'completed', 'lone', 30, 30),
			       ('a5', 'mis-live', 't3', 1, 2, 'd5', 'reserved', 'never-created', 31, 31);
		INSERT INTO run_children (run_id, parent_run_id, root_run_id, tree_key, mission_id, workspace_id, owner_member_id,
		                          account_member_id, harness, mode, purpose, task, base_commit, idempotency_key,
		                          request_hash, state, created_at)
			VALUES ('nested', 'live-a', 'live-int', 'mission:mis-live', 'mis-live', 'w1', 'm1', 'm1', 'claude', 'acp',
			        'review', 'review', 'abc', 'n1', 'h', 'started', 23);
		INSERT INTO watches (id, member_id, target_kind, run_id, mission_id, conditions, source, state, created_at, updated_at, expires_at)
			VALUES ('watch-run', 'm1', 'run', 'live-int', NULL, '["finished"]', 'user', 'active', 1, 1, 9),
			       ('watch-swarm', 'm1', 'mission', NULL, 'mis-live', '["finished"]', 'user', 'active', 1, 1, 9);
		INSERT INTO watch_notifications (id, watch_id, condition, dedup_key, title, body, link, created_at, push_state)
			VALUES ('n-run', 'watch-run', 'finished', 'k', 't', 'b', 'l', 1, 'none'),
			       ('n-swarm', 'watch-swarm', 'finished', 'k', 't', 'b', 'l', 1, 'none');
		INSERT INTO watch_deliveries (notification_id, channel, status, at)
			VALUES ('n-run', 'push', 'delivered', 1), ('n-swarm', 'push', 'delivered', 1);
	`, testKey(t, ""), testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migration from v%d): %v", previous, err)
	}
	defer func() { _ = db.Close() }()

	type child struct{ parent, tree, purpose, harness, model, state string }
	want := map[string]child{
		"done-a":  {"done-int", "done-int", "Parser", "codex", "gpt", "started"},
		"done-b":  {"done-int", "done-int", "write docs for it", "claude", "", "started"},
		"old-int": {"live-int", "live-int", "earlier swarm integrator", "claude", "", "started"},
		"live-a":  {"live-int", "live-int", "Fix the bug", "claude", "", "started"},
		"nested":  {"live-a", "live-int", "review", "claude", "", "started"},
	}
	rows, err := db.db.Query(`SELECT run_id, parent_run_id, tree_key, purpose, harness, model, state FROM run_children`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	got := map[string]child{}
	for rows.Next() {
		var id string
		var c child
		if scanErr := rows.Scan(&id, &c.parent, &c.tree, &c.purpose, &c.harness, &c.model, &c.state); scanErr != nil {
			t.Fatal(scanErr)
		}
		got[id] = c
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		t.Fatal(rowsErr)
	}
	if len(got) != len(want) {
		t.Errorf("children = %v, want %v", got, want)
	}
	for id, c := range want {
		if got[id] != c {
			t.Errorf("child %s = %+v, want %+v", id, got[id], c)
		}
	}

	notified := map[string]bool{}
	detached := map[string]bool{}
	nrows, err := db.db.Query(`SELECT run_id, end_notified, detached FROM run_children WHERE idempotency_key LIKE 'swarm:%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer nrows.Close() //nolint:errcheck // read-only iteration
	for nrows.Next() {
		var id string
		var n, d bool
		if scanErr := nrows.Scan(&id, &n, &d); scanErr != nil {
			t.Fatal(scanErr)
		}
		notified[id], detached[id] = n, d
	}
	for id, wantNotified := range map[string]bool{"done-a": true, "done-b": true, "old-int": true, "live-a": false} {
		if notified[id] != wantNotified {
			t.Errorf("%s end_notified = %v, want %v", id, notified[id], wantNotified)
		}
	}

	for id, want := range map[string]bool{"done-a": true, "done-b": true, "old-int": true, "live-a": false} {
		if detached[id] != want {
			t.Errorf("%s detached = %v, want %v", id, detached[id], want)
		}
	}
	for _, id := range []string{"done-int", "live-int"} {
		var children string
		if scanErr := db.db.QueryRow(`SELECT children FROM runs WHERE id = ?`, id).Scan(&children); scanErr != nil || children != "on" {
			t.Errorf("%s children = %q, %v; want on", id, children, scanErr)
		}
	}

	var leftover []string
	trows, err := db.db.Query(`SELECT name FROM sqlite_master WHERE name LIKE '%mission%' OR sql LIKE '%mission\_%' ESCAPE '\' OR sql LIKE '%missions%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer trows.Close() //nolint:errcheck // read-only iteration
	for trows.Next() {
		var name string
		if err := trows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		leftover = append(leftover, name)
	}
	if len(leftover) > 0 {
		t.Errorf("schema still mentions missions: %v", leftover)
	}

	var watches, notices int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM watches`).Scan(&watches); err != nil || watches != 1 {
		t.Errorf("watches = %d, %v; want only the run watch", watches, err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM watch_notifications`).Scan(&notices); err != nil || notices != 1 {
		t.Errorf("watch notifications = %d, %v; want the run watch's", notices, err)
	}
	var deliveries int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM watch_deliveries`).Scan(&deliveries); err != nil || deliveries != 1 {
		t.Errorf("watch deliveries = %d, %v; want the run watch's", deliveries, err)
	}
}
