package scheduler

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
)

// UpdateTicker is the poll loop's view of the server self-update service
// (*serverupdate.Service). Tick is called once per poll interval; a
// pending update applies at the first idle moment and does not return -
// the process re-executes on the new binary. The service reads Busy for
// itself, so a poll with nothing pending costs one row read.
type UpdateTicker interface {
	Tick(ctx context.Context)
}

// UseUpdates attaches the self-update service to the poll loop. It is
// called once during assembly; leaving it unset means a scheduled update
// simply waits.
func (s *Scheduler) UseUpdates(t UpdateTicker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = t
}

// tickUpdates gives the self-update service its turn. It rides the
// stall-detection poll rather than adding a ticker of its own, so
// `--poll-interval` is the one knob for how promptly a scheduled update
// lands.
func (s *Scheduler) tickUpdates(ctx context.Context) {
	s.mu.Lock()
	t := s.updates
	s.mu.Unlock()
	if t == nil {
		return
	}
	t.Tick(ctx)
}

// Busy reports what this server is doing, which is what a scheduled
// self-update waits for. Restarting is safe for the runs and terminal
// containers either way - the scheduler reattaches to live containers on
// boot - but it drops the streams of anyone attached, so live interactive
// terminal attaches hold the update back too.
//
// Two kinds of run are not counted as working. A run parked at
// needs-attention is waiting on a person; a paused run is a frozen
// container. Neither has anything running inside it, both survive the
// restart like any other, and counting either would leave a busy
// deployment with no idle moment at all, since finished-but-unclosed and
// paused runs sit for days.
//
// A store read that fails reports Unknown, which is never idle: an unknown
// answer must not be the one that decides to restart.
func (s *Scheduler) Busy(ctx context.Context) domain.ServerBusy {
	active, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		slog.Warn("scheduler: read active runs for the idle check", "error", err)
		return domain.ServerBusy{Unknown: true}
	}
	out := domain.ServerBusy{}
	s.mu.Lock()
	out.Shells = s.shells
	for _, r := range active {
		switch {
		case r.Status == domain.RunNeedsAttention:
		case s.runs[r.ID] != nil && s.runs[r.ID].paused:
			out.Paused++
		default:
			out.Runs++
		}
	}
	s.mu.Unlock()
	return out
}

// HoldShell counts one live interactive terminal attach for the idle
// check and returns its release. A restart is safe for the terminal
// container itself (it is re-adopted on boot), but it drops the attached
// stream under the person typing into it, so a scheduled update waits.
func (s *Scheduler) HoldShell() func() {
	return s.holdShell()
}

// holdShell counts one open interactive shell for the idle check and
// returns its release.
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
