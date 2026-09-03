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
		if payload.Title != "First title" {
			t.Fatalf("event title = %q, want First title", payload.Title)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first title event")
	}

	waitFor(t, "latest run title", func() bool {
		fresh, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && fresh.Title == "Latest title"
	})
}
