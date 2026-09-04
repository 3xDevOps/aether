package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// recordCommit persists the latest commit published for a run without loading
// and rewriting the rest of its row.
func (s *Scheduler) recordCommit(ctx context.Context, run domain.RunID, commit string, at time.Time) error {
	if err := s.cfg.Store.UpdateRunCommit(ctx, run, commit, at); err != nil {
		return fmt.Errorf("scheduler: record commit for run %s: %w", run, err)
	}
	return nil
}

// RecordCommit exposes commit metadata recording to the server assembly that
// wires the GitEngine publication callback.
func (s *Scheduler) RecordCommit(ctx context.Context, run domain.RunID, commit string, at time.Time) error {
	return s.recordCommit(ctx, run, commit, at)
}
