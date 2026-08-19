package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// approvalsSchemaVersion is the migration slot the approvals table
// occupies; the upgrade test builds the schema one version behind it.
const approvalsSchemaVersion = 5

// TestApprovalInboxRoundTrip covers the inbox's whole persistence
// contract: create, list by decision, decide once, and the conflict a
// second decision raises.
func TestApprovalInboxRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	w := mustCreateWorkspace(t, db)
	s := mustCreateSession(t, db, w.ID)
	m := mustCreateMember(t, db)
	r := mustCreateRun(t, db, s.ID, m.ID, domain.RunRunning)

	a := &Approval{SessionID: s.ID, RunID: r.ID, Action: "ExitPlanMode", Detail: "1. do the thing"}
	if err := db.CreateApproval(ctx, a); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	if a.ID == "" || a.Decision != ApprovalRequested || a.CreatedAt.IsZero() {
		t.Fatalf("CreateApproval did not stamp the row: %+v", a)
	}

	pending, err := db.ListApprovals(ctx, s.ID, ApprovalRequested)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != a.ID || pending[0].Detail != a.Detail {
		t.Fatalf("pending = %+v, want the created request", pending)
	}

	if derr := db.DecideApproval(ctx, a.ID, "approved", m.ID, a.CreatedAt); derr != nil {
		t.Fatalf("DecideApproval: %v", derr)
	}
	got, err := db.GetApproval(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got.Decision != "approved" || got.DecidedBy != m.ID || got.DecidedAt == nil {
		t.Fatalf("decided approval = %+v, want approved and attributed", got)
	}

	if pending, err = db.ListApprovals(ctx, s.ID, ApprovalRequested); err != nil || len(pending) != 0 {
		t.Fatalf("pending after decision = %+v (err %v), want none", pending, err)
	}
	if all, aerr := db.ListApprovals(ctx, s.ID, ""); aerr != nil || len(all) != 1 {
		t.Fatalf("all approvals = %+v (err %v), want one", all, aerr)
	}

	if err := db.DecideApproval(ctx, a.ID, "denied", m.ID, a.CreatedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("second decision = %v, want ErrConflict", err)
	}
	if err := db.DecideApproval(ctx, "appr_missing", "approved", m.ID, a.CreatedAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("decision on missing request = %v, want ErrNotFound", err)
	}
	if err := db.CreateApproval(ctx, &Approval{SessionID: s.ID, RunID: "run_missing", Action: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approval for missing run = %v, want ErrNotFound", err)
	}
}

// TestApprovalsMigrationUpgradesPreviousVersion builds a database one
// schema version behind the approvals slot, seeds rows, then opens it:
// the upgrade must add the inbox without losing anything.
func TestApprovalsMigrationUpgradesPreviousVersion(t *testing.T) {
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
	for v := 1; v < approvalsSchemaVersion; v++ {
		if _, execErr := raw.Exec(migrations[v-1]); execErr != nil {
			t.Fatalf("apply v%d: %v", v, execErr)
		}
		if _, execErr := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, v); execErr != nil {
			t.Fatalf("record v%d: %v", v, execErr)
		}
	}
	if _, execErr := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, image, env, setup_script, created_at)
			VALUES ('w1', 'proj', 'img', '{}', '', 1);
		INSERT INTO sessions (id, workspace_id, name, base_branch, created_at)
			VALUES ('s1', 'w1', 'effort', 'main', 1);
		INSERT INTO runs (id, session_id, member_id, task, harness, mode, status, branch, worktree, created_at, profile_snapshot_id)
			VALUES ('r1', 's1', 'm1', 'task', 'claude', 'tui', 'running', '', '', 1, '');
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (approvals migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if _, err := db.GetRun(ctx, "r1"); err != nil {
		t.Fatalf("GetRun after migration: %v", err)
	}
	a := &Approval{SessionID: "s1", RunID: "r1", Action: "Bash", Detail: "rm -rf build"}
	if err := db.CreateApproval(ctx, a); err != nil {
		t.Fatalf("CreateApproval after migration: %v", err)
	}
	if err := db.DecideApproval(ctx, a.ID, "denied", "m1", a.CreatedAt); err != nil {
		t.Fatalf("DecideApproval after migration: %v", err)
	}
}
