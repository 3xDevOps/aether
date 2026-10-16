package scheduler

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// integratorStore stands in for a swarm's live workers.
type integratorStore struct {
	store.Store
	workers atomic.Bool
}

func (s *integratorStore) IntegratorWaiting(context.Context, domain.RunID, time.Time) (bool, error) {
	return s.workers.Load(), nil
}

// quietFor makes checkStalls see run as quiet for longer than limit: the
// stall threshold becomes a fraction of the time since its last activity.
func quietFor(t *testing.T, e *testEnv, limit time.Duration) {
	t.Helper()
	time.Sleep(6 * limit)
	e.sched.mu.Lock()
	saved := e.sched.cfg.StallThreshold
	e.sched.cfg.StallThreshold = limit
	e.sched.mu.Unlock()
	e.sched.checkStalls(t.Context())
	e.sched.mu.Lock()
	e.sched.cfg.StallThreshold = saved
	e.sched.mu.Unlock()
}

// nextActivity is the next run.status event for run, which must carry a
// status_seq above the last one seen.
func nextActivity(t *testing.T, sub events.Subscription, run domain.RunID, seq *int64) events.RunStatusPayload {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev := <-sub.Events():
			p, ok := ev.Payload.(events.RunStatusPayload)
			if !ok || ev.RunID != run {
				continue
			}
			if p.StatusSeq <= *seq {
				t.Fatalf("status event %+v has status_seq %d, not above %d", p, p.StatusSeq, *seq)
			}
			*seq = p.StatusSeq
			return p
		case <-deadline:
			t.Fatalf("timed out waiting for run.status on %s", run)
		}
	}
}

// TestRunActivityFollowsWhatTheRunWaitsOn walks one run through every cause
// of a parked or working run and checks the activity each transition
// publishes and stores: only a human request, or a turn only a member can
// continue, is needs-you.
func TestRunActivityFollowsWhatTheRunWaitsOn(t *testing.T) {
	t.Parallel()
	var integrator *integratorStore
	e := newReportingEnv(t, func(cfg *Config) {
		integrator = &integratorStore{Store: cfg.Store}
		cfg.Store = integrator
		cfg.StallThreshold = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Activity != domain.ActivityWorking {
		t.Fatalf("launched run activity = %q, want working", row.Activity)
	}
	seq := row.StatusSeq
	sub := e.subscribe(t)
	report := func(r agentstatus.Report) {
		t.Helper()
		if err := e.sched.ReportAgentState(ctx, run.ID, r); err != nil {
			t.Fatalf("report %+v: %v", r, err)
		}
	}
	idle := agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}
	working := agentstatus.Report{State: agentstatus.Working}
	question := "which region should the staging bucket live in?" + strings.Repeat(" context", 60)

	for _, step := range []struct {
		name     string
		act      func()
		status   domain.RunStatus
		activity domain.RunActivity
		reason   string
	}{
		{"turn end", func() { report(idle) }, domain.RunNeedsAttention, domain.ActivityWaiting, turnEndedReason},
		{"next turn", func() { report(working) }, domain.RunRunning, domain.ActivityWorking, agentstatus.ReasonResumed},
		{"permission mid-turn", func() { report(permissionInput("open")) }, domain.RunRunning, domain.ActivityNeedsYou, agentstatus.ReasonResumed},
		{"permission answered", func() { report(permissionInput("close")) }, domain.RunRunning, domain.ActivityWorking, agentstatus.ReasonResumed},
		{"integrator turn end with a live worker", func() {
			integrator.workers.Store(true)
			report(idle)
		}, domain.RunNeedsAttention, domain.ActivityWaitingAgents, turnEndedReason},
		{"the last worker finished", func() {
			integrator.workers.Store(false)
			e.sched.refreshActivities(ctx)
		}, domain.RunNeedsAttention, domain.ActivityWaiting, turnEndedReason},
		{"needs-input asked at the turn end", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			if err := e.sched.ReportNeedsInput(ctx, run.ID, "ask-1", question, time.Now()); err != nil {
				t.Fatal(err)
			}
			report(idle)
		}, domain.RunNeedsAttention, domain.ActivityNeedsYou, needsInputReasonPrefix + question},
		{"waiting reported", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			// The summary is the agent's text, never a reason Aether wrote.
			if err := e.sched.ReportWaiting(ctx, run.ID, "wait-1", "", "blocked: on 3 workers", time.Now()); err != nil {
				t.Fatal(err)
			}
			report(idle)
		}, domain.RunNeedsAttention, domain.ActivityWaitingAgents, "blocked: on 3 workers"},
		{"the wait outlived three stall thresholds", func() {
			// A live worker would hold the run in waiting-agents again.
			integrator.workers.Store(true)
			quietFor(t, e, 20*time.Millisecond)
		}, domain.RunNeedsAttention, domain.ActivityWaiting, waitedReasonPrefix},
		{"waiting for CI reported", func() {
			integrator.workers.Store(false)
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			if err := e.sched.ReportWaiting(ctx, run.ID, "wait-ci", protocol.CoordWaitingForCI, "CI on PR 12", time.Now()); err != nil {
				t.Fatal(err)
			}
			report(idle)
		}, domain.RunNeedsAttention, domain.ActivityWaitingCI, "CI on PR 12"},
		{"the CI wait went stale", func() {
			quietFor(t, e, 20*time.Millisecond)
		}, domain.RunNeedsAttention, domain.ActivityWaiting, waitedReasonPrefix},
		{"waiting for a process reported", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			if err := e.sched.ReportWaiting(ctx, run.ID, "wait-process", protocol.CoordWaitingForProcess, "release build", time.Now()); err != nil {
				t.Fatal(err)
			}
			report(idle)
		}, domain.RunNeedsAttention, domain.ActivityWaitingTools, "release build"},
		{"a waiting outcome shown without a turn end", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			if err := e.sched.ReportWaiting(ctx, run.ID, "wait-2", "", "no update for 2m on 2 workers", time.Now()); err != nil {
				t.Fatal(err)
			}
			e.sched.mu.Lock()
			err := e.sched.parkIdleReportLocked(ctx, e.sched.runs[run.ID])
			e.sched.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		}, domain.RunNeedsAttention, domain.ActivityWaitingAgents, "no update for 2m on 2 workers"},
		{"background subagents held after the turn", func() {
			report(agentstatus.Report{State: agentstatus.Working, Reason: agentstatus.BackgroundReason(2)})
		}, domain.RunRunning, domain.ActivityWaitingAgents, agentstatus.BackgroundReason(2)},
		{"only a monitor held after the turn", func() {
			report(agentstatus.Report{State: agentstatus.Working, Reason: agentstatus.BackgroundReason(1), Tools: true})
		}, domain.RunRunning, domain.ActivityWaitingTools, agentstatus.BackgroundReason(1)},
		{"the held run stalled", func() {
			quietFor(t, e, 20*time.Millisecond)
		}, domain.RunNeedsAttention, domain.ActivityWaiting, stalledReasonPrefix},
		{"an approval requested mid-turn", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			if err := e.db.CreateApproval(ctx, &store.Approval{WorkspaceID: e.ws.ID, RunID: run.ID, SourceID: "tool-1", Action: "ExitPlanMode"}); err != nil {
				t.Fatal(err)
			}
			if err := e.sched.refreshRunActivity(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
		}, domain.RunRunning, domain.ActivityNeedsYou, agentstatus.ReasonResumed},
		{"the approval decided", func() {
			requested, err := e.db.ListApprovals(ctx, e.ws.ID, store.ApprovalRequested)
			if err != nil || len(requested) != 1 {
				t.Fatalf("approvals = %v, %v", requested, err)
			}
			if err := e.db.DecideApproval(ctx, requested[0].ID, "approved", e.member.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := e.sched.refreshRunActivity(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
		}, domain.RunRunning, domain.ActivityWorking, agentstatus.ReasonResumed},
		{"an Enhanced turn interrupted", func() {
			report(agentstatus.Report{State: agentstatus.Idle, Reason: acpInterruptedReason})
		}, domain.RunNeedsAttention, domain.ActivityNeedsYou, acpInterruptedReason},
		{"an Enhanced turn stopped at its token limit", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			report(agentstatus.Report{State: agentstatus.Idle, Reason: acpIdleReason("max_tokens")})
		}, domain.RunNeedsAttention, domain.ActivityNeedsYou, acpStoppedReason + "max_tokens"},
		{"an Enhanced turn cancelled", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			report(agentstatus.Report{State: agentstatus.Idle, Reason: acpCancelledReason})
		}, domain.RunNeedsAttention, domain.ActivityWaiting, acpCancelledReason},
		{"the agent's turn failed", func() {
			report(agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonFailed})
		}, domain.RunNeedsAttention, domain.ActivityFailed, agentstatus.ReasonFailed},
		{"reported success parks for review", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			if err := e.sched.FinishReported(ctx, run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
				t.Fatal(err)
			}
			report(idle)
		}, domain.RunNeedsAttention, domain.ActivityFinished, reportedSuccessReason},
		{"session disconnected", func() {
			report(working)
			nextActivity(t, sub, run.ID, &seq)
			report(agentstatus.Report{State: agentstatus.Idle, Reason: acpEndedReason + "exit code 1"})
		}, domain.RunNeedsAttention, domain.ActivityDisconnected, acpEndedReason + "exit code 1"},
	} {
		step.act()
		p := nextActivity(t, sub, run.ID, &seq)
		if p.To != step.status || p.Activity != step.activity || !strings.HasPrefix(p.Reason, step.reason) {
			t.Fatalf("%s: published %s/%s %q, want %s/%s %q", step.name, p.To, p.Activity, p.Reason, step.status, step.activity, step.reason)
		}
		row, err := e.db.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != p.To || row.Activity != p.Activity || row.StatusSeq != p.StatusSeq || row.OutcomeUnseen != p.OutcomeUnseen {
			t.Fatalf("%s: stored %s/%s seq %d unseen %v, published %s/%s seq %d unseen %v", step.name,
				row.Status, row.Activity, row.StatusSeq, row.OutcomeUnseen, p.To, p.Activity, p.StatusSeq, p.OutcomeUnseen)
		}
		if (p.Activity == domain.ActivityFailed || p.Activity == domain.ActivityFinished) != p.OutcomeUnseen {
			t.Fatalf("%s: outcome_unseen = %v on a %s run", step.name, p.OutcomeUnseen, p.Activity)
		}
		// Only a human request, a failure, a disconnect or a success to
		// review needs anyone; no wait ever does.
		needs := p.Activity == domain.ActivityNeedsYou || p.Activity == domain.ActivityFailed ||
			p.Activity == domain.ActivityDisconnected || p.Activity == domain.ActivityFinished
		if row.NeedsAttention(false) != needs || (p.Review == domain.ReviewReady) != (p.Activity == domain.ActivityFinished) || row.Review != p.Review {
			t.Fatalf("%s: %s run needs attention %v with review %q (stored %q)", step.name, p.Activity, row.NeedsAttention(false), p.Review, row.Review)
		}
	}
	// Nothing changed: no event, and no new sequence for a client to apply.
	// A subagent's activity only keeps the run from stalling.
	report(agentstatus.Report{State: agentstatus.Idle, Reason: acpEndedReason + "exit code 1"})
	e.sched.mu.Lock()
	before := e.sched.runs[run.ID].lastWorking
	e.sched.mu.Unlock()
	report(agentstatus.Report{Subagent: true})
	e.sched.refreshActivities(ctx)
	expectNoStatusEvent(t, sub, run.ID, "a repeated disconnect and a subagent's activity")
	e.sched.mu.Lock()
	refreshed := e.sched.runs[run.ID].lastWorking.After(before)
	e.sched.mu.Unlock()
	if !refreshed {
		t.Fatal("a subagent's activity did not count as the run's")
	}
}

// TestEveryTurnStartIsRecorded: worker mail is new to an integrator when it
// arrived after its last turn started, so a prompt that starts a turn while
// the agent still works counts as a start too.
func TestEveryTurnStartIsRecorded(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchReporting(t)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	started := func() time.Time {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID].turnStartedAt
	}
	prompt := agentstatus.Report{State: agentstatus.Working, TurnStart: true}
	if err := e.sched.ReportAgentState(ctx, run.ID, prompt); err != nil {
		t.Fatal(err)
	}
	first := started()
	if err := e.sched.ReportAgentState(ctx, run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatal(err)
	}
	if !started().Equal(first) {
		t.Fatal("a tool call inside the turn moved its start")
	}
	time.Sleep(time.Millisecond)
	if err := e.sched.ReportAgentState(ctx, run.ID, prompt); err != nil {
		t.Fatal(err)
	}
	if first.IsZero() || !started().After(first) {
		t.Fatalf("turn starts %v then %v, want the second prompt to move it", first, started())
	}
}

// TestAStallIsWaitingNotNeedsYou: a stall parks the run, but nobody is asked
// for anything.
func TestAStallIsWaitingNotNeedsYou(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 200 * time.Millisecond
		cfg.PollInterval = 20 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, _ := e.launchFake(t, "task")
	p := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention).Payload.(events.RunStatusPayload)
	if !strings.HasPrefix(p.Reason, "stalled: ") || p.Activity != domain.ActivityWaiting {
		t.Fatalf("stall published %q with activity %q, want a stalled reason and waiting", p.Reason, p.Activity)
	}
}
