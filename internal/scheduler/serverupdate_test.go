package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// stubUpdates records how often the poll loop gave it a turn.
type stubUpdates struct{ ticks chan struct{} }

func (s *stubUpdates) Tick(context.Context) {
	select {
	case s.ticks <- struct{}{}:
	default:
	}
}

// A working run holds a scheduled update back: restarting is safe for the
// run itself, but the terminals attached to it are not worth dropping
// while somebody is working.
func TestBusyCountsAWorkingRun(t *testing.T) {
	e := newTestEnv(t, nil)
	if got := e.sched.Busy(t.Context()); !got.Idle() {
		t.Fatalf("busy on an empty scheduler = %+v, want idle", got)
	}
	run, c := e.launchFake(t, "keep the server busy")
	got := e.sched.Busy(t.Context())
	if got.Idle() || got.Runs != 1 {
		t.Fatalf("busy with a live run = %+v, want 1 working run", got)
	}

	// A run parked at needs-attention is waiting on a person, not working,
	// so it does not hold the update back.
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if got = e.sched.Busy(t.Context()); !got.Idle() {
		t.Fatalf("busy with a parked run = %+v, want idle", got)
	}
}

// A paused run is a frozen container: nothing is working inside it and it
// survives a restart like any other, so it is reported but does not hold
// an update back. Pause is a flag, not a status, so the run is still
// `running` in the store and this is the only thing that separates them.
func TestBusyDoesNotCountAPausedRun(t *testing.T) {
	e := newTestEnv(t, nil)
	run, c := e.launchFake(t, "pause me")
	defer c.exitNow(0)

	if err := e.sched.Pause(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	stored, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != domain.RunRunning {
		t.Fatalf("paused run status = %q, want it to still be running", stored.Status)
	}
	got := e.sched.Busy(t.Context())
	if !got.Idle() {
		t.Fatalf("busy with only a paused run = %+v, want idle", got)
	}
	if got.Paused != 1 || got.Runs != 0 {
		t.Fatalf("busy = %+v, want the run counted as paused, not working", got)
	}

	// Resuming puts it back to work and the update waits again.
	if err := e.sched.Resume(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got = e.sched.Busy(t.Context()); got.Idle() || got.Runs != 1 {
		t.Fatalf("busy after resume = %+v, want 1 working run", got)
	}
}

// A workspace shell has no container to reattach to after a restart, so it
// holds an update back the way a working run does.
func TestBusyCountsAnOpenWorkspaceShell(t *testing.T) {
	e := newTestEnv(t, nil)
	release := e.sched.holdShell()
	got := e.sched.Busy(t.Context())
	if got.Idle() || got.Shells != 1 {
		t.Fatalf("busy with an open shell = %+v, want 1 shell", got)
	}
	release()
	if got = e.sched.Busy(t.Context()); !got.Idle() {
		t.Fatalf("busy after the shell closed = %+v, want idle", got)
	}
}

// The poll loop is what drives a scheduled update, so it must give the
// service a turn every interval.
func TestPollLoopTicksTheUpdateService(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.PollInterval = 10 * time.Millisecond })
	svc := &stubUpdates{ticks: make(chan struct{}, 4)}
	e.sched.UseUpdates(svc)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = e.sched.Start(ctx) }()

	select {
	case <-svc.ticks:
	case <-time.After(waitTimeout):
		t.Fatal("the poll loop never gave the update service a turn")
	}
}

// Without an attached service the poll loop simply has nothing to tell.
func TestPollLoopWithoutTheUpdateService(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sched.tickUpdates(t.Context())
}
