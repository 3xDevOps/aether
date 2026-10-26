package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// Widening the receipt table for answered keeps every v84 receipt, applied
// or not, so a reused key still conflicts after the upgrade.
func TestCoordStateReportsMigrationFromV84(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 84)
	if _, execErr := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at, environment, base_branch, steer_others, origin)
			VALUES ('w1', 'proj', 1, '{}', 'main', '', '');
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r1', 'w1', 'm1', 'a', 'claude', 'acp', 'running', 'b', 'w', 1);
		INSERT INTO coord_state_reports (run_id, idempotency_key, outcome, waiting_for, summary, created_at, applied_at)
			VALUES ('r1', 'wait-1', 'waiting', 'ci', 'CI on PR 12', 5, 6),
			       ('r1', 'ask-1', 'needs-input', '', 'which region?', 7, NULL);
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (coord state reports migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	var applied sql.NullInt64
	if err := db.db.QueryRowContext(ctx, `SELECT applied_at FROM coord_state_reports WHERE run_id = 'r1' AND idempotency_key = 'wait-1'`).Scan(&applied); err != nil || applied.Int64 != 6 {
		t.Fatalf("migrated applied_at = %+v, %v; want 6", applied, err)
	}
	retry := &CoordStateReport{RunID: "r1", IdempotencyKey: "wait-1", Outcome: "waiting", WaitingFor: "ci", Summary: "CI on PR 12"}
	if created, err := db.RecordCoordStateReport(ctx, retry); err != nil || created {
		t.Fatalf("retry of a migrated receipt = %v, %v; want the existing receipt", created, err)
	}
	reused := &CoordStateReport{RunID: "r1", IdempotencyKey: "ask-1", Outcome: "answered", Summary: "replied"}
	if _, err := db.RecordCoordStateReport(ctx, reused); !errors.Is(err, ErrCoordReportIdempotencyConflict) {
		t.Fatalf("reused migrated key = %v, want ErrCoordReportIdempotencyConflict", err)
	}
	answered := &CoordStateReport{RunID: "r1", IdempotencyKey: "answered-1", Outcome: "answered", Summary: "replied"}
	if created, err := db.RecordCoordStateReport(ctx, answered); err != nil || !created {
		t.Fatalf("answered receipt = %v, %v; want a new receipt", created, err)
	}
}
