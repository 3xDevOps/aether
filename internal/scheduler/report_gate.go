package scheduler

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// HoldTurnEnd reports whether the turn run's agent is ending owes a report:
// the run takes reports, and none arrived since the turn started. It holds
// each turn at most once, so the caller asks the agent for the report once.
// A Background run is never held: its turn end finishes it.
func (s *Scheduler) HoldTurnEnd(ctx context.Context, run domain.RunID) (bool, error) {
	c := s.coordinationSeam()
	s.mu.Lock()
	entry := s.runs[run]
	if c == nil || !c.enabled || entry == nil || !entry.reportOwed() {
		s.mu.Unlock()
		return false, nil
	}
	started := entry.turnStartedAt
	s.mu.Unlock()
	last, err := s.cfg.Store.LastCoordReportAt(ctx, run)
	if err != nil {
		return false, fmt.Errorf("scheduler: hold the turn end of %s: %w", run, err)
	}
	if !last.Before(started) {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[run] != entry || !entry.turnStartedAt.Equal(started) || !entry.reportOwed() {
		return false, nil
	}
	entry.reportAsked = started
	return true, nil
}

// reportOwed reports whether the run's current turn may be held for its
// report. A pending request already waits on a member. The caller must hold
// s.mu.
func (e *supervised) reportOwed() bool {
	return e.coordDir != "" && e.launchMode.Interactive() && !e.turnStartedAt.IsZero() && !e.reportAsked.Equal(e.turnStartedAt) &&
		(e.status == domain.RunRunning || e.status == domain.RunNeedsAttention) && e.reported == "" && !e.atPrompt() &&
		!e.killRequested && !e.finalizing && !e.destroyPending && !e.exitObserved
}
