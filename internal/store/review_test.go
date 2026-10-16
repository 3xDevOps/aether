package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

// A reported success asks for review; a write that keeps the run's standing
// keeps the review, so a replay cannot re-arm one put off; any other status
// write clears it; SetRunReview moves it only from the states it names.
func TestRunReview(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
	review := func(want domain.RunReview, what string) *domain.Run {
		t.Helper()
		got, err := db.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("%s: GetRun: %v", what, err)
		}
		if got.Review != want || (want != "" && got.ReviewChangedAt == nil) {
			t.Fatalf("%s: review = %q at %v, want %q", what, got.Review, got.ReviewChangedAt, want)
		}
		return got
	}
	park := func() RunStatusStamp {
		t.Helper()
		stamp, err := db.FinishRunReported(ctx, run.ID, domain.RunNeedsAttention, "agent reported success", domain.ActivityFinished, nil, nil)
		if err != nil {
			t.Fatalf("park: %v", err)
		}
		return stamp
	}

	if _, err := db.UpdateRunStatus(ctx, run.ID, domain.RunNeedsAttention, "turn ended without a report", domain.ActivityWaiting, nil, nil); err != nil {
		t.Fatal(err)
	}
	review("", "turn end without a report")
	if _, err := db.FinishRunReported(ctx, run.ID, domain.RunNeedsAttention, "agent reported failure", domain.ActivityFailed, nil, nil); err != nil {
		t.Fatal(err)
	}
	review("", "reported failure")

	if stamp := park(); stamp.Review != domain.ReviewReady || stamp.ReviewChangedAt == nil {
		t.Fatalf("success park stamp = %+v, want ready", stamp)
	}
	review(domain.ReviewReady, "reported success")
	if _, changed, err := db.SetRunReview(ctx, run.ID, domain.ReviewLater, domain.ReviewReady); err != nil || !changed {
		t.Fatalf("later = %v, %v", changed, err)
	}
	review(domain.ReviewLater, "later")
	park()
	if _, err := db.UpdateRunStatus(ctx, run.ID, domain.RunNeedsAttention, "agent reported success", domain.ActivityDisconnected, nil, nil); err != nil {
		t.Fatal(err)
	}
	review(domain.ReviewLater, "a replayed report and an activity change")
	if _, changed, err := db.SetRunReview(ctx, run.ID, domain.ReviewLater, domain.ReviewReady); err != nil || changed {
		t.Fatalf("later from later = %v, %v; want unchanged", changed, err)
	}

	if _, err := db.UpdateRunStatus(ctx, run.ID, domain.RunRunning, "agent resumed", domain.ActivityWorking, nil, nil); err != nil {
		t.Fatal(err)
	}
	review("", "next turn")
	park()
	review(domain.ReviewReady, "a new success report")
	if _, err := db.FinishRunByMember(ctx, run.ID, domain.RunCompleted, "closed", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	review("", "closed by a member")
	if _, changed, err := db.SetRunReview(ctx, run.ID, domain.ReviewAccepted, "", domain.ReviewReady, domain.ReviewLater); err != nil || !changed {
		t.Fatalf("accept = %v, %v", changed, err)
	}
	review(domain.ReviewAccepted, "accepted")
}

// Every run from before v72 starts with no review, so none of them asks
// for one, and a legacy idle activity still reads.
func TestRunReviewMigrationFromV71(t *testing.T) {
	t.Parallel()
	const previous = 71
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
	for v := 1; v <= previous; v++ {
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
		INSERT INTO workspaces (id, name, created_at, environment, base_branch, steer_others, origin)
			VALUES ('w1', 'proj', 1, '{}', 'main', '', '');
		INSERT INTO runs (id, workspace_id, member_id, account_member_id, task, harness, mode, status, reason, activity, outcome_unseen, branch, worktree, created_at)
			VALUES ('parked', 'w1', 'm1', 'm1', 'a', 'claude', 'tui', 'needs-attention', 'agent reported success', 'finished', 1, 'b1', 'w1', 1),
			       ('done', 'w1', 'm1', 'm1', 'a', 'claude', 'headless', 'completed', 'agent reported success', '', 1, 'b2', 'w2', 1),
			       ('legacy', 'w1', 'm1', 'm1', 'a', 'claude', 'tui', 'needs-attention', 'agent idle', 'idle', 0, 'b3', 'w3', 1);
	`, testKey(t, "")); execErr != nil {
		t.Fatalf("seed rows: %v", execErr)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatalf("close raw: %v", closeErr)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (review migration): %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, id := range []domain.RunID{"parked", "done", "legacy"} {
		got, err := db.GetRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Review != "" || got.ReviewChangedAt != nil || got.NeedsAttention(false) {
			t.Fatalf("migrated %s = review %q at %v, needs attention %v; want no review and no need", id, got.Review, got.ReviewChangedAt, got.NeedsAttention(false))
		}
		if id == "legacy" && got.Activity != domain.ActivityIdle {
			t.Fatalf("legacy run activity = %q, want idle read as stored", got.Activity)
		}
	}
}
