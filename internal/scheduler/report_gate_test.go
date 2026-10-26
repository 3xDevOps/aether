package scheduler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// waitNote waits for sub to carry the timeline note want.
func waitNote(t *testing.T, sub events.Subscription, want string) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	var notes []string
	for {
		select {
		case ev := <-sub.Events():
			if p, isTL := ev.Payload.(events.TimelinePayload); isTL && p.Kind == events.TimelineNote {
				if p.Message == want {
					return
				}
				notes = append(notes, p.Message)
			}
		case <-timeout:
			t.Fatalf("timeline notes %q, want %q", notes, want)
		}
	}
}

const reminderPrefix = "Report your state with aether-internal report before ending the turn. "

func userMessages(items []acphost.Item) []string {
	var texts []string
	for _, it := range items {
		if it.Kind == acphost.KindMessage && it.Message.Role == "user" {
			texts = append(texts, it.Message.Text)
		}
	}
	return texts
}

// An Enhanced turn that ends without a report is asked for one once; the
// turn that answers is not asked again. A turn that reported, and a turn the
// member cancelled, are never asked.
func TestEnhancedTurnWithoutAReportIsAskedOnce(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	withCoordination(t, e)
	sub := e.subscribe(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the reminder's turn", turnEnded("end_turn", 2))
	waitNote(t, sub, "run instructions: first message")
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	items := waitItems(t, e.sched, run.ID, "the settled log", func([]acphost.Item) bool { return true })
	if got := userMessages(items); len(got) != 2 || !strings.HasPrefix(got[1], reminderPrefix) || !strings.Contains(got[1], "answered = ") {
		t.Fatalf("prompts %q, want the task and one reminder", got)
	}

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptAskPermission}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the permission", func() bool { return len(e.sched.PendingInputs(run.ID)) == 1 })
	receipt := &store.CoordStateReport{RunID: run.ID, IdempotencyKey: "answered-1", Outcome: "answered", Summary: "replied", CreatedAt: time.Now().UTC()}
	if _, err := e.db.RecordCoordStateReport(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ACPAnswer(run.ID, e.sched.PendingInputs(run.ID)[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the reported turn", turnEnded("end_turn", 3))
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptWait}, false, nil); err != nil {
		t.Fatal(err)
	}
	e.waitAgentWorking(t, run.ID)
	if err := e.sched.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the cancelled turn", turnEnded("cancelled", 1))
	if got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); got.Reason != acpCancelledReason {
		t.Fatalf("reason %q, want %q", got.Reason, acpCancelledReason)
	}
	items = waitItems(t, e.sched, run.ID, "the settled log", func([]acphost.Item) bool { return true })
	if got := userMessages(items); len(got) != 4 || !strings.HasPrefix(got[1], reminderPrefix) || slices.ContainsFunc(got[2:], func(text string) bool { return strings.HasPrefix(text, reminderPrefix) }) {
		t.Fatalf("prompts %q, want only the first turn's reminder", got)
	}
}

// A reminder that wake admission refuses, as for a protected run or a worker
// a human holds, is not sent: the turn ends as it would have.
func TestEnhancedReminderRefusedByAdmissionEndsTheTurn(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	coord, _ := withCoordination(t, e)
	coord.remind = func(context.Context, domain.RunID, string) error { return errors.New("run is protected") }
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); got.Reason != turnEndedReason {
		t.Fatalf("reason %q, want %q", got.Reason, turnEndedReason)
	}
	items := waitItems(t, e.sched, run.ID, "the settled log", func([]acphost.Item) bool { return true })
	if got := userMessages(items); len(got) != 1 {
		t.Fatalf("prompts %q, want the task only", got)
	}
}

// A run that takes no reports is never asked for one.
func TestEnhancedRunWithoutCoordinationIsNeverAsked(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	items := waitItems(t, e.sched, run.ID, "the settled log", func([]acphost.Item) bool { return true })
	if got := userMessages(items); len(got) != 1 {
		t.Fatalf("prompts %q, want the task only", got)
	}
}

// A Standard launch records where its instructions went.
func TestStandardLaunchRecordsItsInstructionChannel(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	withCoordination(t, e)
	sub := e.subscribe(t)
	e.launchFake(t, "add OAuth login")
	waitNote(t, sub, "run instructions: SessionStart hook and MCP server instructions")
}

// A Standard turn is held once, a report since its start lets it end, and a
// held stop whose answer never reached Claude Code ends the turn anyway once
// no hook follows.
func TestStandardTurnIsHeldOnceAndEndsWhenTheHookGoesQuiet(t *testing.T) {
	heldStopGrace = 50 * time.Millisecond
	t.Cleanup(func() { heldStopGrace = time.Minute })
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	withCoordination(t, e)
	run, _ := e.launchFake(t, "add OAuth login")
	start := func() {
		t.Helper()
		if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working, TurnStart: true}); err != nil {
			t.Fatal(err)
		}
	}
	start()
	reminder, err := e.sched.HoldTurnEnd(t.Context(), run.ID)
	if err != nil || !strings.HasPrefix(reminder, reminderPrefix) {
		t.Fatalf("HoldTurnEnd = %q, %v; want the reminder", reminder, err)
	}
	if again, err := e.sched.HoldTurnEnd(t.Context(), run.ID); err != nil || again != "" {
		t.Fatalf("second HoldTurnEnd of the turn = %q, %v; want none", again, err)
	}
	if got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); got.Reason != turnEndedReason {
		t.Fatalf("reason %q, want %q", got.Reason, turnEndedReason)
	}

	start()
	receipt := &store.CoordStateReport{RunID: run.ID, IdempotencyKey: "answered-1", Outcome: "answered", Summary: "replied", CreatedAt: time.Now().UTC()}
	if _, err := e.db.RecordCoordStateReport(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	if reminder, err := e.sched.HoldTurnEnd(t.Context(), run.ID); err != nil || reminder != "" {
		t.Fatalf("HoldTurnEnd of a reported turn = %q, %v; want none", reminder, err)
	}
}

// A message queued behind the reminder's turn starts a turn of its own,
// which is reminded like any other.
func TestEnhancedTurnQueuedBehindTheReminderIsReminded(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	coord, _ := withCoordination(t, e)
	var once sync.Once
	var run *domain.Run
	coord.remind = func(ctx context.Context, id domain.RunID, prompt string) error {
		first := false
		once.Do(func() { first = true })
		if !first {
			return e.sched.Wake(ctx, id, prompt)
		}
		// The first reminder's turn runs until cancelled, with a message
		// queued behind it.
		if err := e.sched.Wake(ctx, id, acpmock.PromptWait); err != nil {
			return err
		}
		go func() {
			if _, err := e.sched.Inject(context.Background(), id, e.member.ID, domain.AgentPrompt{Text: "after"}, false, nil); err != nil {
				t.Error(err)
				return
			}
			if err := e.sched.ACPCancel(context.Background(), id); err != nil {
				t.Error(err)
			}
		}()
		return nil
	}
	run = e.launchACP(t, "say pong")
	items := waitItems(t, e.sched, run.ID, "the queued turn's reminder", func(items []acphost.Item) bool {
		got := userMessages(items)
		return len(got) == 4 && strings.HasPrefix(got[3], reminderPrefix)
	})
	if got := userMessages(items); got[1] != acpmock.PromptWait || got[2] != "after" {
		t.Fatalf("prompts %q, want the task, the first reminder's turn, the queued message and its reminder", got)
	}
}
