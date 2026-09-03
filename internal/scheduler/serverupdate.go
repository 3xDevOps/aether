package scheduler

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
)

// UpdateTicker is the poll loop's view of the server self-update service
// (*serverupdate.Service). Tick is called once per poll interval with
// whether the server is idle right now; a pending update applies on the
// first idle tick and does not return - the process re-executes on the new
// binary.
type UpdateTicker interface {
	Tick(ctx context.Context, idle bool)
}

// UseUpdates attaches the self-update service to the poll loop. It is
// called once during assembly; leaving it unset means a scheduled update
// simply waits.
func (s *Scheduler) UseUpdates(t UpdateTicker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = t
}

// tickUpdates reports the current idleness to the attached self-update
// service. It rides the stall-detection poll rather than adding a ticker
// of its own, so `--poll-interval` is the one knob for how promptly a
// scheduled update lands.
func (s *Scheduler) tickUpdates(ctx context.Context) {
	s.mu.Lock()
	t := s.updates
	s.mu.Unlock()
	if t == nil {
		return
	}
	t.Tick(ctx, s.Idle(ctx))
}

// Idle reports that nobody is working on this server: no run is queued,
// provisioning, or running, and no workspace shell is open. Restarting is
// safe for the runs either way - the scheduler reattaches to live
// containers on boot - but it drops attached terminals, and the
// interactive shells have no container to reattach to at all.
//
// A run parked at needs-attention does not hold it back. That run is
// waiting on a person, not working, and its container survives the restart
// like any other; counting it would leave a busy deployment with no idle
// moment at all, since finished-but-unclosed runs sit there for days.
//
// A store read that fails is not idle: an unknown answer must never be the
// one that decides to restart.
func (s *Scheduler) Idle(ctx context.Context) bool {
	s.mu.Lock()
	shells := s.shells
	s.mu.Unlock()
	if shells > 0 {
		return false
	}
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		slog.Warn("scheduler: read active runs for the idle check", "error", err)
		return false
	}
	for _, r := range active {
		if r.Status != domain.RunNeedsAttention {
			return false
		}
	}
	return true
}

// holdShell counts one open workspace shell for the idle check and returns
// its release.
func (s *Scheduler) holdShell() func() {
	s.mu.Lock()
	s.shells++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.shells--
		s.mu.Unlock()
	}
}
