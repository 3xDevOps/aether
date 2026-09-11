package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestKill(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	// Kill publishes its timeline event on one goroutine and the abandoned
	// transition on the finalize goroutine, in either order. The wait
	// helpers discard what they do not match, so each wait reads its own
	// stream rather than racing to swallow the other's event.
	status := e.subscribe(t)
	ctx := t.Context()

	run, _ := e.launchFake(t, "long haul task")
	if err := e.sched.Kill(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineKill)

	ev := waitStatusEvent(t, status, run.ID, domain.RunAbandoned)
	p := ev.Payload.(events.RunStatusPayload)
	if p.Reason != "killed" {
		t.Fatalf("abandoned reason = %q", p.Reason)
	}
	if ev.ActorID != e.member.ID {
		t.Fatalf("abandoned actor = %s, want %s", ev.ActorID, e.member.ID)
	}
	fresh := e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "wip: long haul task" {
		t.Fatalf("commits = %v", got)
	}
	if fresh.Worktree == "" {
		t.Fatal("kill must preserve the worktree")
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
}

func TestPauseResumeAndStallExemption(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 60 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, c := e.launchFake(t, "task")
	if err := e.sched.Pause(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelinePause)
	if got := c.currentState(); got != "paused" {
		t.Fatalf("container state = %q, want paused", got)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || !sc.Paused {
		t.Fatalf("sidecar paused flag: %+v, %v", sc, err)
	}
	if !e.sched.Paused(run.ID) {
		t.Fatal("Paused(run) = false after Pause")
	}
	if e.sched.Paused("run_unknown") {
		t.Fatal("Paused(unknown run) = true")
	}
	if perr := e.sched.Pause(ctx, run.ID, e.member.ID); perr == nil {
		t.Fatal("double pause accepted")
	}

	// Paused runs are exempt from stall detection: well past the stall
	// threshold the run must still be running.
	time.Sleep(200 * time.Millisecond)
	r, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunRunning {
		t.Fatalf("paused run status = %s, want running", r.Status)
	}

	if rerr := e.sched.Resume(ctx, run.ID, e.member.ID); rerr != nil {
		t.Fatalf("Resume: %v", rerr)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineResume)
	if got := c.currentState(); got != "running" {
		t.Fatalf("container state = %q, want running", got)
	}
	sc, err = e.sched.readSidecar(run.ID)
	if err != nil || sc.Paused {
		t.Fatalf("sidecar paused flag after resume: %+v, %v", sc, err)
	}
	if e.sched.Paused(run.ID) {
		t.Fatal("Paused(run) = true after Resume")
	}

	// Unpaused and idle: now the stall fires.
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); !strings.HasPrefix(p.Reason, "stalled: no output or file changes for ") {
		t.Fatalf("stall reason = %q", p.Reason)
	}
}

func TestStallAndActivityResume(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 60 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, c := e.launchFake(t, "task")
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); !strings.HasPrefix(p.Reason, "stalled: ") {
		t.Fatalf("stall reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	// PTY output refreshes activity on the stalled-but-alive run.
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
		t.Fatalf("resume reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
}

func TestNativeActivityOverridesGenericLiveness(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	run, c := e.launchFake(t, "native state")

	e.pty.setActivity(run.ID, ptyhost.ActivityIdle, false)
	e.sched.checkNativeActivity(t.Context())
	idle := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if got := idle.Payload.(events.RunStatusPayload).Reason; got != nativeIdleReason {
		t.Fatalf("idle reason = %q, want %q", got, nativeIdleReason)
	}

	// Generic output and file activity cannot clear an explicit native idle.
	c.output("repaint and answer\r\n")
	e.git.touch(run.ID)
	e.sched.checkStalls(t.Context())
	stored := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if stored.Status != domain.RunNeedsAttention {
		t.Fatalf("native idle was cleared by generic activity: %s", stored.Status)
	}

	e.pty.setActivity(run.ID, ptyhost.ActivityBlocked, false)
	e.sched.checkNativeActivity(t.Context())
	blocked := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if got := blocked.Payload.(events.RunStatusPayload).Reason; got != nativeBlockedReason {
		t.Fatalf("blocked reason = %q, want %q", got, nativeBlockedReason)
	}

	e.pty.setActivity(run.ID, ptyhost.ActivityWorking, false)
	e.sched.checkNativeActivity(t.Context())
	resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if got := resumed.Payload.(events.RunStatusPayload).Reason; got != "activity resumed" {
		t.Fatalf("working resume reason = %q", got)
	}
}

func TestStaleNativeActivityIsUncertain(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	run, _ := e.launchFake(t, "stale native state")

	e.pty.setActivity(run.ID, ptyhost.ActivityIdle, true)
	e.sched.checkNativeActivity(t.Context())
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if got := ev.Payload.(events.RunStatusPayload).Reason; got != nativeStaleReason {
		t.Fatalf("stale reason = %q, want %q", got, nativeStaleReason)
	}
	if got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention).Status; got != domain.RunNeedsAttention {
		t.Fatalf("stale activity status = %s, want needs-attention", got)
	}
}

func TestFileChangeCountsAsActivity(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 80 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, _ := e.launchFake(t, "task")
	// Keep touching files (no PTY output): the run must stay running.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		e.git.touch(run.ID)
		time.Sleep(10 * time.Millisecond)
	}
	r, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunRunning {
		t.Fatalf("run with file activity = %s, want running", r.Status)
	}
}

func TestInject(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	if err := e.sched.Inject(ctx, run.ID, e.member.ID, "focus on the tests"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	inj := e.pty.injected()
	if len(inj) != 1 || inj[0].name != "Ada" || inj[0].color != "#e6194b" || inj[0].message != "focus on the tests" {
		t.Fatalf("injects = %+v", inj)
	}
	waitFor(t, "stdin delivery", func() bool {
		return strings.Contains(c.stdinString(), "focus on the tests\r")
	})
	ev := waitTimelineEvent(t, sub, run.ID, events.TimelineSteer)
	p := ev.Payload.(events.TimelinePayload)
	if p.Message != "focus on the tests" {
		t.Fatalf("steer message = %q", p.Message)
	}
	if ev.ActorID != e.member.ID {
		t.Fatalf("steer actor = %s", ev.ActorID)
	}
}

// The submit sequence is the harness's: a harness that steers with a
// second Enter gets it, so steered text reaches the conversation instead
// of sitting in the agent's input box.
func TestInjectUsesHarnessSubmitSequence(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	if err := e.sched.Inject(ctx, run.ID, e.member.ID, "one enter"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	waitFor(t, "stdin delivery", func() bool {
		return strings.HasSuffix(c.stdinString(), "one enter\r")
	})

	opencode, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "task", "opencode", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch opencode: %v", err)
	}
	if err := e.sched.Inject(ctx, opencode.ID, e.member.ID, "two enters"); err != nil {
		t.Fatalf("Inject opencode: %v", err)
	}
	inj := e.pty.injected()
	var last *fakeInject
	for i := range inj {
		if inj[i].run == opencode.ID {
			last = &inj[i]
		}
	}
	if last == nil || last.submit != "\r\r" {
		t.Fatalf("opencode injects = %+v, want submit %q", inj, "\r\r")
	}
}

// CloseRun resolves a stalled run's disposition directly: the status
// moves to the outcome before the agent's exit, the container is torn
// down, and finalization still cleans up behind the expected race.
func TestCloseRunResolvesStalledRun(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 40 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, _ := e.launchFake(t, "task")
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun on stalled run: %v", err)
	}
	r := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if r.FinishedAt == nil {
		t.Fatal("closed run must have FinishedAt")
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	waitFor(t, "entry removed", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID] == nil
	})
	if err := e.sched.DeleteRun(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
}

// A finished run is re-labeled in place by the close disposition, and a
// second close at the same outcome is a no-op.
func TestCloseRunRelabelsFinishedRun(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatalf("CloseRun completed to abandoned: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun abandoned to merged: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun at the same outcome: %v", err)
	}
}

func TestInjectLiveStalledNeedsAttention(t *testing.T) {
	const stallThreshold = 40 * time.Millisecond
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = stallThreshold
		cfg.PollInterval = 10 * time.Millisecond
	})
	sub := e.subscribe(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, c := e.launchFake(t, "task")
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	if err := e.sched.Inject(ctx, run.ID, e.member.ID, "keep going"); err != nil {
		t.Fatalf("Inject stalled: %v", err)
	}
	inj := e.pty.injected()
	if len(inj) != 1 || inj[0].message != "keep going" {
		t.Fatalf("injects = %+v", inj)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineSteer)

	// The steer by itself does not clear the stall. Its banner is the
	// server's own output, and this agent never answers, so unparking here
	// would hide the hang for another whole threshold.
	quiet := time.After(5 * stallThreshold)
	for watching := true; watching; {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatal("event stream closed while watching the stalled run")
			}
			p, isStatus := ev.Payload.(events.RunStatusPayload)
			if isStatus && ev.RunID == run.ID && p.To == domain.RunRunning {
				t.Fatalf("steer alone returned the run to running (%q); only the agent's own output should",
					p.Reason)
			}
		case <-quiet:
			watching = false
		}
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	// The agent answering is what returns it to running.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				c.output("got:keep going\r\n")
			}
		}
	}()
	resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if p := resumed.Payload.(events.RunStatusPayload); p.Reason != "activity resumed" {
		t.Fatalf("resume reason = %q", p.Reason)
	}
}

func TestInjectCleanExitedCompleted(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	waitFor(t, "completed run removed from supervision", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		_, ok := e.sched.runs[run.ID]
		return !ok
	})

	err := e.sched.Inject(ctx, run.ID, e.member.ID, "too late")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Inject completed = %v, want ErrInvalidTransition", err)
	}
}

// DeleteRun's redundant Kill must survive the kill that is already in
// flight: the first kill's finalize can transition the status and destroy
// the container while the delete's own Stop call is in the air, and the
// delete must still remove the checkout, transcripts, and run record.
func TestDeleteRunSurvivesKillFinalizingDuringStop(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "delete during kill")

	entered := make(chan struct{})
	release := make(chan struct{})
	e.rt.stopHook = func() {
		close(entered)
		<-release
	}

	errCh := make(chan error, 1)
	go func() { errCh <- e.sched.DeleteRun(t.Context(), run.ID, e.member.ID) }()
	<-entered // DeleteRun's Kill sits inside Stop; its status check already passed.

	e.sched.mu.Lock()
	cid := e.sched.runs[run.ID].containerID
	e.sched.mu.Unlock()
	// The first kill's effect: the process ends and finalization destroys
	// the container and releases the entry while the redundant stop is parked.
	if err := e.rt.Destroy(t.Context(), cid); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	waitFor(t, "finalization released the entry", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID] == nil
	})

	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if _, err := e.sched.cfg.Store.GetRun(t.Context(), run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after delete = %v, want not found", err)
	}
}
