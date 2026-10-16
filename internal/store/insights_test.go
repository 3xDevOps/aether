package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestInsightsMigrationMarksSwarmRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 66)
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`PRAGMA foreign_keys = OFF;
		INSERT INTO members (id, display_name, public_key, color, role, created_at) VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at) VALUES ('w1', 'proj', 1);
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at)
		VALUES ('top', 'w1', 'm1', 't', 'claude', 'tui', 'completed', '', '', 1),
		       ('worker', 'w1', 'm1', 't', 'claude', 'tui', 'completed', '', '', 1),
		       ('first', 'w1', 'm1', 't', 'claude', 'tui', 'failed', '', '', 1),
		       ('second', 'w1', 'm1', 't', 'claude', 'tui', 'running', '', '', 1);
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, idempotency_key, created_at, updated_at, phase, current_integrator_run_id)
		VALUES ('mis', 'w1', 'o', 'm1', 'k', 1, 1, 'active', 'second');
		INSERT INTO mission_create_receipts (workspace_id, idempotency_key, mission_id, objective, accountable_human_id,
			integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, created_at, initial_run_id)
		VALUES ('w1', 'k', 'mis', 'o', 'm1', 'm1', 'claude', 'tui', '[]', 1, 'first');
		INSERT INTO mission_attempts (id, mission_id, task_id, task_revision, number, dispatch_key, state, run_id, created_at, reserved_at)
		VALUES ('a1', 'mis', 't', 1, 1, 'd1', 'completed', 'worker', 1, 1), ('a2', 'mis', 't', 1, 2, 'd2', 'reserved', NULL, 1, 1);
		PRAGMA foreign_keys = ON;`, testKey(t, "")); err != nil {
		t.Fatalf("seed v66 rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	want := map[string]bool{"top": false, "worker": true, "first": true, "second": true}
	for id, swarm := range want {
		var got bool
		if err := db.db.QueryRowContext(context.Background(), `SELECT swarm_run FROM runs WHERE id = ?`, id).Scan(&got); err != nil || got != swarm {
			t.Errorf("run %s swarm_run = %v, %v; want %v", id, got, err, swarm)
		}
	}
}

func TestRunActivityChangeRecordsTransition(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	r := mustCreateRun(t, db, mustCreateWorkspace(t, db).ID, mustCreateMember(t, db).ID, domain.RunRunning)
	for _, activity := range []domain.RunActivity{domain.ActivityWorking, domain.ActivityWaitingTools, domain.ActivityWaitingTools} {
		if _, err := db.UpdateRunStatus(ctx, r.ID, domain.RunRunning, "", activity, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	r, err := db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Activity = domain.ActivityNeedsYou
	if err = db.UpdateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.QueryContext(ctx, `SELECT from_status || '>' || to_status || ' ' || activity FROM run_state_transitions WHERE run_id = ? ORDER BY rowid`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var got []string
	for rows.Next() {
		var row string
		if err = rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"running>running working", "running>running waiting-tools", "running>running needs-you"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("transitions = %q, want %q", got, want)
	}
}
