package scheduler

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

const heldQuestion = "needs input: which region?"

func mailTurnEnded(items []acphost.Item) bool {
	return slices.ContainsFunc(items, func(it acphost.Item) bool {
		return it.Kind == acphost.KindMessage && it.Message.Role == "user" && it.Message.Text == "unread mail"
	}) && turnEnded("end_turn", 2)(items)
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
	waitItems(t, e.sched, run.ID, "the mail prompt", mailTurnEnded)
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
	waitItems(t, e.sched, run.ID, "the mail prompt turn", mailTurnEnded)
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

func TestEnhancedClaudeRunKeepsAQuestionAskedAfterTheMessageWasSent(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	withCoordination(t, e)
	run := e.launchClaudeACP(t, acpmock.PromptHold)
	e.askWhileHeld(t, run.ID)
	e.sched.mu.Lock()
	asked := e.sched.runs[run.ID].idleReportAt
	e.sched.mu.Unlock()
	if err := e.sched.answerReached(run.ID, asked.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if reason := e.idleReason(run.ID); reason != heldQuestion {
		t.Fatalf("idle reason %q after a message sent before the question reached the agent", reason)
	}
	if err := e.sched.answerReached(run.ID, asked.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if reason := e.idleReason(run.ID); reason != "" {
		t.Fatalf("idle reason %q after the answer reached the agent, want none", reason)
	}
}

func (e *testEnv) waitRunReason(t *testing.T, run domain.RunID, reason string) {
	t.Helper()
	waitFor(t, "the run to need attention because "+reason, func() bool {
		r, err := e.db.GetRun(t.Context(), run)
		return err == nil && r.Status == domain.RunNeedsAttention && r.Reason == reason
	})
}

func TestEnhancedClaudeRunShowsAQuestionAgainAfterAStopAndAWake(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	withCoordination(t, e)
	run := e.launchClaudeACP(t, acpmock.PromptHold)
	e.askWhileHeld(t, run.ID)
	if err := e.sched.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	e.waitRunReason(t, run.ID, acpCancelledReason)
	if reason := e.idleReason(run.ID); reason != heldQuestion {
		t.Fatalf("idle reason %q after the stop, want the pending question kept", reason)
	}
	if err := e.sched.WakeEnhanced(t.Context(), run.ID, "unread mail"); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the mail turn", mailTurnEnded)
	e.waitRunReason(t, run.ID, heldQuestion)
	if reason := e.idleReason(run.ID); reason != heldQuestion {
		t.Fatalf("idle reason %q after the mail turn, want the question kept", reason)
	}
}

func TestEnhancedClaudeRunShowsAQuestionAgainAfterTheSessionEnds(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	withCoordination(t, e)
	run := e.launchClaudeACP(t, acpmock.PromptHold)
	e.askWhileHeld(t, run.ID)
	if _, err := rt.all()[0].Stop(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the ended session's reason", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Status == domain.RunNeedsAttention && strings.HasPrefix(r.Reason, acpEndedReason)
	})
	if reason := e.idleReason(run.ID); reason != heldQuestion {
		t.Fatalf("idle reason %q after the session ended, want the pending question kept", reason)
	}
	if err := e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Resume(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reopened session", func() bool { return e.sched.IdleEnhanced(run.ID) })
	if err := e.sched.WakeEnhanced(t.Context(), run.ID, "unread mail"); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the mail turn", mailTurnEnded)
	e.waitRunReason(t, run.ID, heldQuestion)
}

func TestEnhancedClaudeRunQuestionAskedMidTurnClearsWhenAMessageFollows(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	withCoordination(t, e)
	run := e.launchClaudeACP(t, acpmock.PromptWait)
	e.waitAgentWorking(t, run.ID)
	if err := e.sched.ReportNeedsInput(t.Context(), run.ID, "needs-input-1", "which region?", time.Now()); err != nil {
		t.Fatal(err)
	}
	if reason := e.idleReason(run.ID); reason != heldQuestion {
		t.Fatalf("idle reason %q after the question, want it pending", reason)
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "us-east"}, true, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the answer to clear the question", func() bool { return e.idleReason(run.ID) == "" })
	if err := e.sched.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the cancelled turn", turnEnded("cancelled", 1))
	e.waitRunReason(t, run.ID, acpCancelledReason)
}
