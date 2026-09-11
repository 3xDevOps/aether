package scheduler

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// newReportingEnv is a test scheduler whose runs really do get a status
// reporter. The reporter rides the run's coordination directory, so a run
// launched without coordination has none whatever its harness profile says
// - which is exactly why the reporter is recorded by the launch that
// attached it rather than looked up from the harness name.
func newReportingEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	binary := fakeServerBinary(t, "#!/bin/sh\necho aether\n")
	e := newTestEnv(t, func(cfg *Config) {
		cfg.ServerBinary = binary
		if mutate != nil {
			mutate(cfg)
		}
	})
	withCoordination(t, e)
	return e
}

// launchReporting launches a run on a harness whose profile carries a full
// status reporter (claude) and returns it with its fake container. The env
// must come from newReportingEnv, or the run gets no reporter at all.
func (e *testEnv) launchReporting(t *testing.T) (*domain.Run, *fakeContainer) {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "add OAuth login", "claude", domain.LaunchTUI)
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
	e := newReportingEnv(t, func(cfg *Config) {
		// Far longer than the test: nothing here is a stall.
		cfg.StallThreshold = time.Hour
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, c := e.launchReporting(t)

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
	expectNoStatusEvent(t, sub, run.ID, "a working report on a running run")
}

// expectNoStatusEvent fails if a run.status event for run arrives in the
// next tenth of a second. what names the thing that must not have published.
func expectNoStatusEvent(t *testing.T, sub events.Subscription, run domain.RunID, what string) {
	t.Helper()
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			if p, isStatus := ev.Payload.(events.RunStatusPayload); isStatus && ev.RunID == run {
				t.Fatalf("%s published %+v", what, p)
			}
		case <-deadline:
			return
		}
	}
}

// TestAgentWaitingReplacesAStallReason: a run the silence heuristic parked
// as a stall gets the real reason the moment the agent says what it is
// waiting for, and a harness without a full reporter still comes back on
// activity alone.
func TestAgentWaitingReplacesAStallReason(t *testing.T) {
	e := newReportingEnv(t, func(cfg *Config) {
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
	stop := pump(t, c)
	defer stop()
	resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if p := resumed.Payload.(events.RunStatusPayload); p.Reason != "activity resumed" {
		t.Fatalf("resume reason = %q, want \"activity resumed\"", p.Reason)
	}

	// Un-parking on activity forgets what the agent last said, so the same
	// wait reported after the next stall is news again. Remembering it
	// would leave the run reading "stalled:" for a member it is really
	// waiting on.
	stop()
	waitFor(t, "the run to stall a second time", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Status == domain.RunNeedsAttention && strings.HasPrefix(r.Reason, "stalled: ")
	})
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report the same wait after a second stall: %v", err)
	}
	waitFor(t, "the second stall reason to be replaced", func() bool {
		r, err := e.db.GetRun(t.Context(), run.ID)
		return err == nil && r.Reason == agentstatus.ReasonPermission
	})
}

// TestAgentWorkingStillStalls: the threshold is still the hang detector.
// A run whose agent said it was working and then went silent parks as a
// stall, with the reason that says so.
func TestAgentWorkingStillStalls(t *testing.T) {
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 40 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, c := e.launchReporting(t)

	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatalf("report working: %v", err)
	}
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); !strings.HasPrefix(p.Reason, "stalled: ") {
		t.Fatalf("reason = %q, want a stall: silence is still the hang detector", p.Reason)
	}

	// Nothing holds this run for the member - the agent said it was
	// working, not waiting - so the agent talking again releases it, the
	// same way it releases a run on a harness that cannot report at all.
	stop := pump(t, c)
	defer stop()
	resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if p := resumed.Payload.(events.RunStatusPayload); p.Reason != "activity resumed" {
		t.Fatalf("resume reason = %q, want \"activity resumed\"", p.Reason)
	}
}

// TestWorkingReportOutlastsTheNextPoll: a hook writes nothing to the
// terminal and touches no files, so the report itself has to count as
// activity. Otherwise the poll that follows a resumed run re-parks it as
// stalled, and every tool call flips the run card twice.
func TestWorkingReportOutlastsTheNextPoll(t *testing.T) {
	e := newReportingEnv(t, func(cfg *Config) {
		// Long enough that several polls run before the threshold is a
		// stall again, short enough to park the launched run quickly.
		cfg.StallThreshold = 200 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, _ := e.launchReporting(t)

	waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working}); err != nil {
		t.Fatalf("report working: %v", err)
	}
	waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	expectNoStatusEvent(t, sub, run.ID, "the polls after a working report")
}

// pump keeps a container's terminal producing agent output until the
// returned function is called, the way an agent that is talking again does.
func pump(t *testing.T, c *fakeContainer) func() {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				c.output("still alive\r\n")
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop); <-done }) }
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
	run, c := e.launchReporting(t)
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if err := e.sched.ReportAgentState(t.Context(), run.ID, working); err == nil {
		t.Error("report for a finished run succeeded")
	}
}

// TestRepeatedWaitingReportIsNotNews: Claude Code reports one wait twice -
// as the turn ends, and again once it has been idle at its prompt for a
// minute - and the pair for a permission six seconds apart. The second
// report says what the run already says, so it costs no store write and no
// event; a report that changes the reason still lands.
func TestRepeatedWaitingReportIsNotNews(t *testing.T) {
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.StallThreshold = time.Hour
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	e.startStalls(t)
	run, _ := e.launchReporting(t)

	waiting := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonInput}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report waiting: %v", err)
	}
	waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report the same wait again: %v", err)
	}
	expectNoStatusEvent(t, sub, run.ID, "the same wait reported twice")

	// A different reason is news: the member is now being asked for
	// something else.
	permission := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonPermission}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, permission); err != nil {
		t.Fatalf("report a permission wait: %v", err)
	}
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != agentstatus.ReasonPermission {
		t.Fatalf("event reason = %q, want %q", p.Reason, agentstatus.ReasonPermission)
	}
}

// TestAgentWaitingWhilePausedStillParks: a hook whose report is in flight
// when the member pauses the run arrives against a frozen container. The
// agent cannot repeat it - it is stopped at the prompt it sent it from - so
// holding the report back would leave the run reading Working with nothing
// left to correct it: silence from a container the member froze is not a
// stall either.
func TestAgentWaitingWhilePausedStillParks(t *testing.T) {
	e := newReportingEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 40 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	e.startStalls(t)
	run, _ := e.launchReporting(t)
	if err := e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	waiting := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonInput}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report waiting on a paused run: %v", err)
	}
	if r := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention); r.Reason != agentstatus.ReasonInput {
		t.Fatalf("stored reason = %q, want %q", r.Reason, agentstatus.ReasonInput)
	}
	if err := e.sched.Resume(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Well past the stall threshold: the run stays parked for the reason
	// the agent gave, and the silence heuristic does not relabel it.
	time.Sleep(100 * time.Millisecond)
	r, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunNeedsAttention || r.Reason != agentstatus.ReasonInput {
		t.Fatalf("resumed run = %s because %q, want it still parked because %q", r.Status, r.Reason, agentstatus.ReasonInput)
	}
}
