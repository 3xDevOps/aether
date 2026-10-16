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
	"github.com/3xDevOps/Aether/internal/store"
)

// integratorStore stands in for a swarm's live workers.
type integratorStore struct {
	store.Store
	workers atomic.Bool
}

func (s *integratorStore) IntegratorWaiting(context.Context, domain.RunID) (bool, error) {
	return s.workers.Load(), nil
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
// publishes and stores: only a human request is needs-you.
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
		{"turn end", func() { report(idle) }, domain.RunNeedsAttention, domain.ActivityIdle, agentstatus.ReasonIdle},
		{"next turn", func() { report(working) }, domain.RunRunning, domain.ActivityWorking, agentstatus.ReasonResumed},
		{"permission mid-turn", func() { report(permissionInput("open")) }, domain.RunRunning, domain.ActivityNeedsYou, agentstatus.ReasonResumed},
		{"permission answered", func() { report(permissionInput("close")) }, domain.RunRunning, domain.ActivityWorking, agentstatus.ReasonResumed},
		{"integrator turn end with a live worker", func() {
			// An integrator is launched for its mission like a worker.
			e.sched.mu.Lock()
			e.sched.runs[run.ID].missionAssigned = true
			e.sched.mu.Unlock()
			integrator.workers.Store(true)
			report(idle)
		}, domain.RunNeedsAttention, domain.ActivityWaitingAgents, agentstatus.ReasonIdle},
		{"the last worker finished", func() {
			integrator.workers.Store(false)
			e.sched.refreshActivities(ctx)
			e.sched.mu.Lock()
			e.sched.runs[run.ID].missionAssigned = false
			e.sched.mu.Unlock()
		}, domain.RunNeedsAttention, domain.ActivityIdle, agentstatus.ReasonIdle},
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
			if err := e.sched.ReportWaiting(ctx, run.ID, "wait-1", "waiting on 3 workers", time.Now()); err != nil {
				t.Fatal(err)
			}
			report(idle)
		}, domain.RunNeedsAttention, domain.ActivityWaitingAgents, "waiting on 3 workers"},
		{"the wait outlived three stall thresholds", func() {
			e.sched.mu.Lock()
			err := e.sched.expireWaitLocked(ctx, e.sched.runs[run.ID], 3*time.Hour+time.Second)
			e.sched.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		}, domain.RunNeedsAttention, domain.ActivityIdle, "waited 3h0m1s with no activity"},
		{"background subagents held after the turn", func() {
			report(agentstatus.Report{State: agentstatus.Working, Reason: agentstatus.BackgroundReason(2)})
		}, domain.RunRunning, domain.ActivityWaitingAgents, agentstatus.BackgroundReason(2)},
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
		if p.To != step.status || p.Activity != step.activity || p.Reason != step.reason {
			t.Fatalf("%s: published %s/%s %q, want %s/%s %q", step.name, p.To, p.Activity, p.Reason, step.status, step.activity, step.reason)
		}
		row, err := e.db.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != p.To || row.Activity != p.Activity || row.StatusSeq != p.StatusSeq {
			t.Fatalf("%s: stored %s/%s seq %d, published %s/%s seq %d", step.name, row.Status, row.Activity, row.StatusSeq, p.To, p.Activity, p.StatusSeq)
		}
	}
	// Nothing changed: no event, and no new sequence for a client to apply.
	report(agentstatus.Report{State: agentstatus.Idle, Reason: acpEndedReason + "exit code 1"})
	e.sched.refreshActivities(ctx)
	expectNoStatusEvent(t, sub, run.ID, "a repeated disconnect")
}

// TestAStallIsIdleNotNeedsYou: a stall parks the run, but nobody is asked
// for anything.
func TestAStallIsIdleNotNeedsYou(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 200 * time.Millisecond
		cfg.PollInterval = 20 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, _ := e.launchFake(t, "task")
	p := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention).Payload.(events.RunStatusPayload)
	if !strings.HasPrefix(p.Reason, "stalled: ") || p.Activity != domain.ActivityIdle {
		t.Fatalf("stall published %q with activity %q, want a stalled reason and idle", p.Reason, p.Activity)
	}
}
