package overlap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// TestRestartDoesNotRepublishHistory covers the restart path: two runs
// overlap and both finish, then the server restarts and the index rebuilds
// itself from the log. The rebuild must announce nothing - the overlaps it
// walks through are history, and republishing them would append a fresh set
// of run.overlap events to the durable log on every boot.
func TestRestartDoesNotRepublishHistory(t *testing.T) {
	ctx := context.Background()
	log, openErr := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if openErr != nil {
		t.Fatalf("event log: %v", openErr)
	}
	t.Cleanup(func() { _ = log.Close() })

	const workspace = domain.WorkspaceID("w1")
	diff := func(bus events.Bus, run domain.RunID, path string) {
		t.Helper()
		if _, err := bus.Publish(ctx, events.Event{
			WorkspaceID: workspace, RunID: run,
			Payload: events.RunDiffPayload{Files: []events.FileDiffStat{{Path: path}}},
		}); err != nil {
			t.Fatalf("publish diff: %v", err)
		}
	}
	countOverlaps := func() int {
		t.Helper()
		got, err := log.Read(ctx, events.Filter{Types: []events.Type{events.TypeRunOverlap}}, 0, 0, 1000)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		return len(got)
	}
	waitOverlaps := func(want int) int {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if n := countOverlaps(); n >= want || time.Now().After(deadline) {
				return n
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	idx := NewIndex(bus, oneWorkspace, log)
	if err = idx.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	diff(bus, "r1", "main.go")
	diff(bus, "r2", "main.go")
	if n := waitOverlaps(2); n != 2 {
		t.Fatalf("live index: overlap events = %d, want 2", n)
	}
	for _, run := range []domain.RunID{"r1", "r2"} {
		if _, err = bus.Publish(ctx, events.Event{
			WorkspaceID: workspace, RunID: run,
			Payload: events.RunStatusPayload{From: domain.RunRunning, To: domain.RunMerged},
		}); err != nil {
			t.Fatalf("publish status: %v", err)
		}
	}
	// The first terminal status clears the overlap for both runs.
	live := waitOverlaps(4)
	if live != 4 {
		t.Fatalf("live index: overlap events = %d, want 4", live)
	}
	_ = idx.Close()
	_ = bus.Close()

	restarted, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatalf("bus after restart: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	rebuilt := NewIndex(restarted, oneWorkspace, log)
	if err = rebuilt.Start(ctx); err != nil {
		t.Fatalf("start after restart: %v", err)
	}
	t.Cleanup(func() { _ = rebuilt.Close() })

	// Two fresh runs overlap after the rebuild. Their two events are the
	// only ones the restart may add: the index consumes in order, so
	// seeing them means the whole replay has already been consumed.
	diff(restarted, "r3", "other.go")
	diff(restarted, "r4", "other.go")
	if n := waitOverlaps(live + 2); n != live+2 {
		t.Fatalf("after restart: overlap events = %d, want %d", n, live+2)
	}
}
