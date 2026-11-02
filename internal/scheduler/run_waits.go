package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// RunWaits cancels the conditions a run waits on. Satisfied by
// *watch.Service.
type RunWaits interface {
	EndRunWaits(ctx context.Context, run domain.RunID, cause string, before time.Time) error
}

// UseRunWaits has a person's prompt and a relaunch cancel the run's wait
// conditions: the run is no longer waiting on them.
func (s *Scheduler) UseRunWaits(w RunWaits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waits = w
}

// endRunWaits cancels the conditions run registered before now, so ones
// it registers in answer to what follows stay.
func (s *Scheduler) endRunWaits(ctx context.Context, run domain.RunID, cause string) error {
	s.mu.Lock()
	w := s.waits
	s.mu.Unlock()
	if w == nil {
		return nil
	}
	if err := w.EndRunWaits(ctx, run, cause, time.Now()); err != nil {
		return fmt.Errorf("scheduler: cancel run %s's wait conditions: %w", run, err)
	}
	return nil
}
