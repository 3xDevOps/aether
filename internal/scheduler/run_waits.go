package scheduler

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
)

// RunWaits cancels the conditions a run waits on. Satisfied by
// *watch.Service.
type RunWaits interface {
	EndRunWaits(ctx context.Context, run domain.RunID, cause string) error
}

// UseRunWaits has a person's prompt and a relaunch cancel the run's wait
// conditions: the run is no longer waiting on them.
func (s *Scheduler) UseRunWaits(w RunWaits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waits = w
}

func (s *Scheduler) endRunWaits(ctx context.Context, run domain.RunID, cause string) {
	s.mu.Lock()
	w := s.waits
	s.mu.Unlock()
	if w == nil {
		return
	}
	if err := w.EndRunWaits(ctx, run, cause); err != nil {
		slog.Warn("scheduler: cancel the run's wait conditions", "run", run, "cause", cause, "error", err)
	}
}
