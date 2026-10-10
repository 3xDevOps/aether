package scheduler

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func TestEnhancedRunTitleFollowsAgentAcrossReopen(t *testing.T) {
	oldInterval := runTitleDebounceInterval
	runTitleDebounceInterval = 25 * time.Millisecond
	t.Cleanup(func() { runTitleDebounceInterval = oldInterval })

	e, rt := newACPEnv(t)
	fixture, err := acpmock.Load("codex")
	if err != nil {
		t.Fatal(err)
	}
	fixture.Initialize = bytes.Replace(fixture.Initialize, []byte(`"resume": {},`), nil, 1)
	rt.fixture = fixture
	sub := e.subscribe(t)
	run := e.launchACP(t, "say pong")
	awaitTitle := func(want string) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case ev := <-sub.Events():
				title, ok := ev.Payload.(events.RunTitlePayload)
				if !ok || ev.RunID != run.ID || title.Title != want {
					continue
				}
				fresh, readErr := e.db.GetRun(t.Context(), run.ID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if fresh.Title != want || fresh.Task != "say pong" {
					t.Fatalf("title = %q, task = %q", fresh.Title, fresh.Task)
				}
				return
			case <-timer.C:
				t.Fatalf("no run.title event for %q", want)
			}
		}
	}
	awaitTitle("Reply with exactly the word pong and nothing else; do not use tools.")
	waitItems(t, e.sched, run.ID, "first turn", turnEnded("end_turn", 1))
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	awaitTitle("Pong response request")
}

func TestEnhancedRunIsTitledFromItsFirstPrompt(t *testing.T) {
	oldDebounce, oldLookup := runTitleDebounceInterval, acpTitleLookupDelay
	runTitleDebounceInterval, acpTitleLookupDelay = 25*time.Millisecond, 25*time.Millisecond
	t.Cleanup(func() { runTitleDebounceInterval, acpTitleLookupDelay = oldDebounce, oldLookup })

	e, rt := newACPEnv(t, withMockClaude)
	storedTitle := func(run domain.RunID) string {
		t.Helper()
		fresh, err := e.db.GetRun(t.Context(), run)
		if err != nil {
			t.Fatal(err)
		}
		return fresh.Title
	}
	inject := func(s *Scheduler, run domain.RunID, text string) {
		t.Helper()
		if _, err := s.Inject(t.Context(), run, e.member.ID, domain.AgentPrompt{Text: text}, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	listedSince := func(first int) func() bool {
		return func() bool {
			return slices.ContainsFunc(rt.all()[first:], func(exec *acpExec) bool {
				return slices.Contains(exec.agent.Methods(), acp.AgentMethodSessionList)
			})
		}
	}

	withTask := e.launchACP(t, "say pong")
	waitItems(t, e.sched, withTask.ID, "the task's turn", turnEnded("end_turn", 1))
	inject(e.sched, withTask.ID, "then say ping")
	if title := storedTitle(withTask.ID); title != "" {
		t.Fatalf("a follow-up titled a run launched with a task %q", title)
	}

	sub := e.subscribe(t)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatal(err)
	}
	nextTitle := func() string {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case ev := <-sub.Events():
				if title, ok := ev.Payload.(events.RunTitlePayload); ok && ev.RunID == run.ID {
					return title.Title
				}
			case <-timer.C:
				t.Fatal("no run.title event")
			}
		}
	}

	// The recorded adapter lists an untitled session under this prompt.
	const prompt = "\nReply with exactly the word pong\nand nothing else; do not use tools."
	inject(e.sched, run.ID, prompt)
	if title := storedTitle(run.ID); title != "Reply with exactly the word pong" {
		t.Fatalf("title after the first prompt = %q, want its first line", title)
	}
	if title := nextTitle(); title != "Reply with exactly the word pong" {
		t.Fatalf("first run.title = %q, want the prompt's first line", title)
	}
	waitFor(t, "the title lookup", listedSince(0))
	waitItems(t, e.sched, run.ID, "first turn", turnEnded("end_turn", 1))

	inject(e.sched, run.ID, "again")
	if title := nextTitle(); title != "Pong response" {
		t.Fatalf("second run.title = %q, want the agent's listed title", title)
	}
	waitItems(t, e.sched, run.ID, "second turn", turnEnded("end_turn", 2))

	execs := len(rt.all())
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	startScheduler(t, restarted)
	waitFor(t, "resumed session", func() bool { return restarted.acp.session(run.ID) != nil })
	inject(restarted, run.ID, prompt)
	waitFor(t, "the restored session's title lookup", listedSince(execs))
	time.Sleep(4 * runTitleDebounceInterval)
	if title := storedTitle(run.ID); title != "Pong response" {
		t.Fatalf("title after a restart and another prompt = %q, want the agent's", title)
	}
}

func TestProvisionalRunTitleNeverReplacesATitle(t *testing.T) {
	oldInterval := runTitleDebounceInterval
	runTitleDebounceInterval = 25 * time.Millisecond
	t.Cleanup(func() { runTitleDebounceInterval = oldInterval })

	e := newTestEnv(t, nil)
	run := &domain.Run{
		WorkspaceID: e.ws.ID,
		MemberID:    e.member.ID,
		Harness:     "fake",
		Mode:        domain.LaunchACP,
		Status:      domain.RunQueued,
	}
	if err := e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	title := func() string {
		t.Helper()
		fresh, err := e.db.GetRun(t.Context(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return fresh.Title
	}

	e.sched.setRunTitle(run.ID, "Agent title")
	e.sched.setProvisionalRunTitle(run.ID, "a prompt sent before the agent's title was stored")
	flushed := func() bool {
		e.sched.titleMu.Lock()
		defer e.sched.titleMu.Unlock()
		return e.sched.titleUpdates[run.ID] == nil
	}
	waitFor(t, "the agent's title", flushed)
	e.sched.setProvisionalRunTitle(run.ID, "a prompt sent after it")
	if got := title(); got != "Agent title" {
		t.Fatalf("a provisional title replaced the agent's: %q", got)
	}

	if err := e.db.SetRunTitle(t.Context(), run.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.sched.setProvisionalRunTitle(run.ID, "\n  "+strings.Repeat("é", 130)+"\nsecond line")
	if got, want := title(), strings.Repeat("é", 120)+"…"; got != want {
		t.Fatalf("provisional title = %q, want %q", got, want)
	}
	e.sched.setRunTitle(run.ID, "Agent title")
	waitFor(t, "the agent's title over the provisional one", func() bool { return title() == "Agent title" })
	waitFor(t, "the last title flush", flushed)
}

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
