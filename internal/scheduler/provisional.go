package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// provisionalGrace is how long a swarm worker's turn end waits for the next
// turn, such as one its unread mail wakes, before its last message is
// forwarded.
var provisionalGrace = 15 * time.Second

// TurnEnded hands a swarm worker's last message to coordination, which
// forwards it to the integrator as a provisional report unless the turn
// reported. A turn that started within provisionalGrace or a waiting outcome
// keeps it back; an answered one does not, so the integrator hears from a
// worker whose attempt stays open.
func (s *Scheduler) TurnEnded(run domain.RunID, message string) {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil || !entry.missionAssigned {
		s.mu.Unlock()
		return
	}
	started := entry.turnStartedAt
	s.mu.Unlock()
	time.AfterFunc(provisionalGrace, func() {
		s.mu.Lock()
		current := s.runs[run] == entry && entry.turnStartedAt.Equal(started) && (!entry.waits() || entry.answered())
		s.mu.Unlock()
		c := s.coordinationSeam()
		if !current || c == nil || s.superCtx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.superCtx, 30*time.Second)
		defer cancel()
		if err := c.svc.ProvisionalReport(ctx, run, started, message); err != nil {
			slog.Warn("scheduler: forward the worker's last message to its integrator", "run", run, "error", err)
		}
	})
}
