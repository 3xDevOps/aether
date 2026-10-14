package scheduler

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

const heldQuestion = "needs input: which region?"

func userSaid(text string) func([]acphost.Item) bool {
	return func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindMessage && it.Message.Role == "user" && it.Message.Text == text
		})
	}
}

func (e *testEnv) askWhileHeld(t *testing.T, run domain.RunID) {
	t.Helper()
	waitFor(t, "the run to report its task", func() bool {
		r, err := e.db.GetRun(t.Context(), run)
		return err == nil && r.Reason == "working in background: 1 task"
	})
	if err := e.sched.ReportNeedsInput(t.Context(), run, "needs-input-1", "which region?", time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the question", func() bool {
		r, err := e.db.GetRun(t.Context(), run)
		return err == nil && r.Status == domain.RunNeedsAttention && r.Reason == heldQuestion
	})
}

func TestEnhancedClaudeRunWakesForMailWhileATaskIsHeldAfterAPromptTurn(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	coord, _ := withCoordination(t, e)
	var mail sync.Once
	coord.mu.Lock()
	coord.onWake = func(r domain.RunID) {
		if e.sched.IdleEnhanced(r) {
			mail.Do(func() { go func() { _ = e.sched.WakeEnhanced(context.Background(), r, "unread mail") }() })
		}
	}
	coord.mu.Unlock()
	run := e.launchClaudeACP(t, acpmock.PromptHold)
	waitItems(t, e.sched, run.ID, "the mail prompt", func(items []acphost.Item) bool {
		return userSaid("unread mail")(items) && turnEnded("end_turn", 2)(items)
	})
	if !e.sched.acp.session(run.ID).State().Background {
		t.Fatal("the held task ended before the mail prompt")
	}
}

func TestEnhancedClaudeRunKeepsAQuestionThroughAMailPromptTurn(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	withCoordination(t, e)
	sub := e.subscribe(t)
	run := e.launchClaudeACP(t, acpmock.PromptHold)
	e.askWhileHeld(t, run.ID)
	trail := e.statusTrail(t, sub, run.ID, func(trail []events.RunStatusPayload) bool {
		return statusIndex(trail, domain.RunNeedsAttention, heldQuestion) >= 0
	})

	if err := e.sched.WakeEnhanced(t.Context(), run.ID, "unread mail"); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the mail prompt turn", func(items []acphost.Item) bool {
		return userSaid("unread mail")(items) && turnEnded("end_turn", 2)(items)
	})
	waitFor(t, "the mail turn to settle", func() bool { return !e.sched.acp.session(run.ID).State().TurnInFlight })
	trail = append(trail, e.runStatusEvents(sub, run.ID)...)
	for _, p := range trail[statusIndex(trail, domain.RunNeedsAttention, heldQuestion):] {
		if p.To != domain.RunNeedsAttention || p.Reason != heldQuestion {
			t.Fatalf("status events %+v, want nothing to move the run off its question", trail)
		}
	}
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunNeedsAttention || r.Reason != heldQuestion {
		t.Fatalf("run = %+v, %v; want the question kept through the mail turn", r, err)
	}
}

func TestEnhancedClaudeRunQuestionClearsWhenAMemberMessageReachesTheAgent(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	withCoordination(t, e)
	run := e.launchClaudeACP(t, acpmock.PromptHold)
	e.askWhileHeld(t, run.ID)
	if err := e.sched.WakeEnhanced(t.Context(), run.ID, acpmock.PromptDemo); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the paced mail turn", func() bool { return e.sched.acp.session(run.ID).State().TurnInFlight })
	if r, err := e.db.GetRun(t.Context(), run.ID); err != nil || r.Status != domain.RunNeedsAttention || r.Reason != heldQuestion {
		t.Fatalf("run = %+v, %v; want the question kept while the mail turn runs", r, err)
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "us-east"}, true, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the answer to resume the run", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Status == domain.RunRunning
	})
	if reason := e.idleReason(run.ID); reason != "" {
		t.Fatalf("idle reason %q after the answer reached the agent, want none", reason)
	}
}

func (e *testEnv) idleReason(run domain.RunID) string {
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	return e.sched.runs[run].idleReason
}
