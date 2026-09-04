package scheduler

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func TestSetRunTitleDebouncesAndKeepsLatest(t *testing.T) {
	oldInterval := runTitleDebounceInterval
	runTitleDebounceInterval = 25 * time.Millisecond
	t.Cleanup(func() { runTitleDebounceInterval = oldInterval })

	e := newTestEnv(t, nil)
	run := &domain.Run{
		WorkspaceID: e.ws.ID,
		MemberID:    e.member.ID,
		Task:        "fix login",
		Harness:     "fake",
		Mode:        domain.LaunchTUI,
		Status:      domain.RunQueued,
	}
	if err := e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	sub := e.subscribe(t)

	e.sched.setRunTitle(run.ID, "First title")
	e.sched.setRunTitle(run.ID, "Latest title")
	select {
	case ev := <-sub.Events():
		t.Fatalf("title event published before debounce: %#v", ev)
	default:
	}
	fresh, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("read run before debounce: %v", err)
	}
	if fresh.Title != "" {
		t.Fatalf("title persisted before debounce: %q", fresh.Title)
	}

	select {
	case ev := <-sub.Events():
		payload, ok := ev.Payload.(events.RunTitlePayload)
		if !ok {
			t.Fatalf("event payload = %T, want RunTitlePayload", ev.Payload)
		}
		if payload.Title != "Latest title" {
			t.Fatalf("event title = %q, want Latest title", payload.Title)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for debounced title event")
	}

	waitFor(t, "latest run title", func() bool {
		fresh, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && fresh.Title == "Latest title"
	})
	select {
	case ev := <-sub.Events():
		t.Fatalf("unexpected second title event in debounce window: %#v", ev)
	case <-time.After(2 * runTitleDebounceInterval):
	}
}

func TestSetRunTitleDeletedRunStopsRetrying(t *testing.T) {
	oldInterval := runTitleDebounceInterval
	runTitleDebounceInterval = 10 * time.Millisecond
	t.Cleanup(func() { runTitleDebounceInterval = oldInterval })

	e := newTestEnv(t, nil)
	run := &domain.Run{
		WorkspaceID: e.ws.ID,
		MemberID:    e.member.ID,
		Task:        "fix login",
		Harness:     "fake",
		Mode:        domain.LaunchTUI,
		Status:      domain.RunQueued,
	}
	if err := e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	e.sched.setRunTitle(run.ID, "Deleted title")
	if err := e.db.DeleteRun(t.Context(), run.ID); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	waitFor(t, "deleted title pending entry", func() bool {
		e.sched.titleMu.Lock()
		defer e.sched.titleMu.Unlock()
		_, ok := e.sched.titleUpdates[run.ID]
		return !ok
	})
	time.Sleep(3 * runTitleDebounceInterval)
	e.sched.titleMu.Lock()
	defer e.sched.titleMu.Unlock()
	if _, ok := e.sched.titleUpdates[run.ID]; ok {
		t.Fatal("deleted run title was retried")
	}
}

func TestFlushPendingRunTitlesDoesNotRetryAfterFailure(t *testing.T) {
	oldInterval := runTitleDebounceInterval
	runTitleDebounceInterval = 10 * time.Millisecond
	t.Cleanup(func() { runTitleDebounceInterval = oldInterval })

	e := newTestEnv(t, nil)
	run := &domain.Run{
		WorkspaceID: e.ws.ID,
		MemberID:    e.member.ID,
		Task:        "fix login",
		Harness:     "fake",
		Mode:        domain.LaunchTUI,
		Status:      domain.RunQueued,
	}
	if err := e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	e.sched.setRunTitle(run.ID, "Shutdown title")
	if err := e.db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	e.sched.flushPendingRunTitles()
	time.Sleep(3 * runTitleDebounceInterval)
	e.sched.titleMu.Lock()
	defer e.sched.titleMu.Unlock()
	if _, ok := e.sched.titleUpdates[run.ID]; ok {
		t.Fatal("failed shutdown title flush was retried")
	}
}
