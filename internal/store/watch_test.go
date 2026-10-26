package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestWatchWritesCompareVersionsAndNoticesRecordOnce(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ws := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := &domain.Run{WorkspaceID: ws.ID, MemberID: member.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	w := &Watch{MemberID: member.ID, WorkspaceID: ws.ID, TargetKind: "run", RunID: run.ID, Conditions: []string{"needs_you"},
		Source: "dashboard", State: "active", IdempotencyKey: "k1", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.CreateWatch(ctx, w); err != nil {
		t.Fatal(err)
	}
	again := *w
	if err := db.CreateWatch(ctx, &again); !errors.Is(err, ErrConflict) {
		t.Fatalf("a repeated idempotency key = %v, want ErrConflict", err)
	}

	stale := *w
	w.Label = "first"
	if err := db.UpdateWatch(ctx, w); err != nil {
		t.Fatal(err)
	}
	stale.Label = "second"
	if err := db.UpdateWatch(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("a write at a stale version = %v, want ErrConflict", err)
	}

	// Concurrent evaluations of the same need: one records it.
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		recorded int
	)
	for range 6 {
		wg.Go(func() {
			got, err := db.GetWatch(ctx, w.ID)
			if err != nil {
				t.Error(err)
				return
			}
			got.Fired["needs_you"] = "k"
			n := &WatchNotification{Condition: "needs_you", Key: "k", Title: "t", Body: "b", Link: "/", CreatedAt: now,
				PushState: WatchPushNone, Deliveries: []WatchDelivery{{Channel: "in_app", Status: "delivered", At: now}}}
			out, err := db.CommitWatchEvaluation(ctx, got, []*WatchNotification{n})
			if err != nil && !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
			mu.Lock()
			recorded += len(out)
			mu.Unlock()
		})
	}
	wg.Wait()
	notes, err := db.ListWatchNotifications(ctx, w.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if recorded != 1 || len(notes) != 1 || len(notes[0].Deliveries) != 1 {
		t.Fatalf("recorded %d, stored %+v; want one notice with its in-app delivery", recorded, notes)
	}

	// Even a commit at the current version cannot record the same notice again.
	got, err := db.GetWatch(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := db.CommitWatchEvaluation(ctx, got, []*WatchNotification{{Condition: "needs_you", Key: "k", CreatedAt: now, PushState: WatchPushNone}})
	if err != nil || len(out) != 0 {
		t.Fatalf("recommit = %d notices, %v; want none", len(out), err)
	}
}
