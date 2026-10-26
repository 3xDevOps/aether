package scheduler

import (
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/coordhooks"
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
	if got := userMessages(items); len(got) != 2 || got[1] != coordhooks.ReportReminder {
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
	if got := userMessages(items); slices.Index(got, coordhooks.ReportReminder) != 1 || len(got) != 4 {
		t.Fatalf("prompts %q, want only the first turn's reminder", got)
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
