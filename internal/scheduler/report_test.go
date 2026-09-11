package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// launchReporting launches a run on a harness whose profile carries a full
// status reporter (claude) and returns it with its fake container.
func (e *testEnv) launchReporting(t *testing.T, task string) (*domain.Run, *fakeContainer) {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	c := e.rt.byName(string(run.ID))
	if c == nil {
		t.Fatalf("no container created for run %s", run.ID)
	}
	return run, c
}

// startStalls runs the scheduler's poll loop for the duration of the test.
func (e *testEnv) startStalls(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

// TestAgentWaitingParksAndResumes is the whole point of the reporter: an
// agent that ends its turn parks its run immediately with the reason it
// gave - long before any stall threshold - a repaint while the member
// types does not un-park it, and the agent's own next turn does.
func TestAgentWaitingParksAndResumes(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		// Far longer than the test: nothing here is a stall.
		cfg.StallThreshold = time.Hour
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, c := e.launchReporting(t, "add OAuth login")

	waiting := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonInput}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report waiting: %v", err)
	}
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); p.From != domain.RunRunning || p.Reason != agentstatus.ReasonInput {
		t.Fatalf("park event = %+v, want running -> needs-attention because %q", p, agentstatus.ReasonInput)
	}
	if r := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); r.Reason != agentstatus.ReasonInput {
		t.Fatalf("stored reason = %q, want %q", r.Reason, agentstatus.ReasonInput)
	}

	// A TUI repainting while the member types is output on the same stream
	// the stall detector watches. It is not work, and it must not hide the
	// fact that the agent is waiting.
	for range 10 {
		c.output("redraw\r\n")
		time.Sleep(10 * time.Millisecond)
	}
	r, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunNeedsAttention {
		t.Fatalf("run = %s after a repaint, want it still parked: only the agent's own report resumes it", r.Status)
	}

	// The agent's own next turn does.
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatalf("report working: %v", err)
	}
	ev = waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if p := ev.Payload.(events.RunStatusPayload); p.From != domain.RunNeedsAttention || p.Reason != agentstatus.ReasonResumed {
		t.Fatalf("resume event = %+v, want needs-attention -> running because %q", p, agentstatus.ReasonResumed)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)

	// A second working report on a running run costs nothing: Claude Code
	// fires it on every tool call, so it must not become a status write and
	// an event per call.
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatalf("second report working: %v", err)
	}
	deadline := time.After(100 * time.Millisecond)
	for quiet := false; !quiet; {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				quiet = true
				break
			}
			if p, isStatus := ev.Payload.(events.RunStatusPayload); isStatus && ev.RunID == run.ID {
				t.Fatalf("a working report on a running run published %+v", p)
			}
		case <-deadline:
			quiet = true
		}
	}
}

// TestAgentWaitingReplacesAStallReason: a run the silence heuristic parked
// as a stall gets the real reason the moment the agent says what it is
// waiting for, and a harness without a full reporter still comes back on
// activity alone.
func TestAgentWaitingReplacesAStallReason(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 40 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, c := e.launchFake(t, "task")

	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); !strings.HasPrefix(p.Reason, "stalled: ") {
		t.Fatalf("stall reason = %q, want it to lead with \"stalled: \"", p.Reason)
	}

	waiting := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonPermission}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report waiting onto a stalled run: %v", err)
	}
	waitFor(t, "the stall reason to be replaced", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Reason == agentstatus.ReasonPermission
	})

	// This harness has no reporter, so it can never say "working" again:
	// activity is still what brings it back, exactly as before.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				c.output("still alive\r\n")
			}
		}
	}()
	resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if p := resumed.Payload.(events.RunStatusPayload); p.Reason != "activity resumed" {
		t.Fatalf("resume reason = %q, want \"activity resumed\"", p.Reason)
	}
}

// TestAgentWorkingStillStalls: the threshold is still the hang detector.
// A run whose agent said it was working and then went silent parks as a
// stall, with the reason that says so.
func TestAgentWorkingStillStalls(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 40 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, _ := e.launchReporting(t, "add OAuth login")

	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatalf("report working: %v", err)
	}
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); !strings.HasPrefix(p.Reason, "stalled: ") {
		t.Fatalf("reason = %q, want a stall: silence is still the hang detector", p.Reason)
	}
}

// TestReportAgentStateRefusesRunsItDoesNotSupervise: the caller is a hook
// inside a container, so a report for a run that is gone or finished is an
// error with enough context to read, not a status write.
func TestReportAgentStateRefusesRunsItDoesNotSupervise(t *testing.T) {
	e := newTestEnv(t, nil)
	working := agentstatus.Report{State: agentstatus.Working}

	if err := e.sched.ReportAgentState(t.Context(), domain.RunID("nosuchrun"), working); err == nil {
		t.Error("report for an unknown run succeeded")
	}
	run, c := e.launchReporting(t, "add OAuth login")
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if err := e.sched.ReportAgentState(t.Context(), run.ID, working); err == nil {
		t.Error("report for a finished run succeeded")
	}
}
