package store

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// A run's last change is its creation until something changes, and an
// activity-only change moves it, since that is when an agent went quiet.
func TestRunLastChange(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)

	created, err := db.RunLastChange(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if created.Sub(run.CreatedAt).Abs() > time.Second {
		t.Fatalf("last change of an unchanged run = %s, want its creation %s", created, run.CreatedAt)
	}

	if _, err = db.UpdateRunStatus(ctx, run.ID, domain.RunRunning, "", domain.ActivityWorking, nil, nil); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	time.Sleep(10 * time.Millisecond)
	if _, err = db.UpdateRunStatus(ctx, run.ID, domain.RunRunning, "", domain.ActivityAnswered, nil, nil); err != nil {
		t.Fatal(err)
	}
	answered, err := db.RunLastChange(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if answered.Before(before) {
		t.Fatalf("last change after working -> answered = %s, want after %s", answered, before)
	}

	if _, err := db.RunLastChange(ctx, "run-missing"); err != ErrNotFound {
		t.Fatalf("last change of a missing run = %v, want ErrNotFound", err)
	}
}
