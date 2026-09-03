package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// stubUpdates records what the poll loop reports.
type stubUpdates struct{ ticks chan bool }

func (s *stubUpdates) Tick(_ context.Context, idle bool) {
	select {
	case s.ticks <- idle:
	default:
	}
}

// An active run holds the idle check open: restarting is safe for the run
// itself, but the terminals attached to it are not worth dropping while
// somebody is working.
func TestIdleIsFalseWhileARunIsActive(t *testing.T) {
	e := newTestEnv(t, nil)
	if !e.sched.Idle(t.Context()) {
		t.Fatal("a scheduler with no runs must report idle")
	}
	run, c := e.launchFake(t, "keep the server busy")
	if e.sched.Idle(t.Context()) {
		t.Fatal("a live run must not report idle")
	}
	// A run parked at needs-attention is waiting on a person, not
	// working, so it does not hold the update back.
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if !e.sched.Idle(t.Context()) {
		t.Fatal("a run parked at needs-attention must not block the idle check")
	}
}

// A workspace shell has no container to reattach to after a restart, so it
// holds the idle check open the same way a run does.
func TestIdleIsFalseWhileAWorkspaceShellIsOpen(t *testing.T) {
	e := newTestEnv(t, nil)
	release := e.sched.holdShell()
	if e.sched.Idle(t.Context()) {
		t.Fatal("an open workspace shell must not report idle")
	}
	release()
	if !e.sched.Idle(t.Context()) {
		t.Fatal("the server must be idle once the shell closed")
	}
}

// The poll loop is what drives a scheduled update, so it must report
// idleness on every tick rather than only when something changed.
func TestPollLoopReportsIdlenessToTheUpdateService(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.PollInterval = 10 * time.Millisecond })
	svc := &stubUpdates{ticks: make(chan bool, 8)}
	e.sched.UseUpdates(svc)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = e.sched.Start(ctx) }()

	select {
	case idle := <-svc.ticks:
		if !idle {
			t.Fatal("an empty scheduler reported not idle")
		}
	case <-time.After(waitTimeout):
		t.Fatal("the poll loop never reported idleness")
	}

	_, c := e.launchFake(t, "busy")
	defer c.exitNow(0)
	deadline := time.After(waitTimeout)
	for {
		select {
		case idle := <-svc.ticks:
			if !idle {
				return
			}
		case <-deadline:
			t.Fatal("the poll loop kept reporting idle with a run active")
		}
	}
}

// Without an attached service the poll loop simply has nothing to tell.
func TestPollLoopWithoutTheUpdateService(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sched.tickUpdates(t.Context())
}
