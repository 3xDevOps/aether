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
// A paused run is frozen. It survives restart like any other container but
// has nothing running inside it, so it does not hold a deployment back.
// A live needs-attention run is conservatively working: its agent can
// resume, and its container and PTY remain available. Completed runs are
// runtime-terminal and are absent from the active list.
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
