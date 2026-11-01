package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// provisionalGrace is how long a child run's turn end waits for the next
// turn, such as one its unread mail wakes, before its last message is
// forwarded.
var provisionalGrace = 15 * time.Second

// TurnEnded hands a child run's last message to coordination, which
// forwards it to the parent as a provisional report unless the turn
// reported. A turn that started within provisionalGrace or a waiting outcome
// keeps it back; an answered one does not, so the parent hears from a child
// that stays open.
func (s *Scheduler) TurnEnded(run domain.RunID, message string) {
	s.mu.Lock()
	entry := s.runs[run]
	if entry == nil {
		s.mu.Unlock()
		return
	}
	started := entry.turnStartedAt
	s.mu.Unlock()
	if !s.hasParent(run) {
		return
	}
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
			slog.Warn("scheduler: forward the child's last message to its parent", "run", run, "error", err)
		}
	})
}

// hasParent reports whether run is a child run.
func (s *Scheduler) hasParent(run domain.RunID) bool {
	r, err := s.cfg.Store.GetRun(s.superCtx, run)
	return err == nil && r.ParentRunID != ""
}
