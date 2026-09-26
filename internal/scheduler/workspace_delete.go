package scheduler

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
)

func (s *Scheduler) workspaceLock(id domain.WorkspaceID) *sync.RWMutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workspaceLocks == nil {
		s.workspaceLocks = make(map[domain.WorkspaceID]*sync.RWMutex)
	}
	lock := s.workspaceLocks[id]
	if lock == nil {
		lock = &sync.RWMutex{}
		s.workspaceLocks[id] = lock
	}
	return lock
}

// WithInactiveWorkspace fences launch/relaunch for the full cleanup, including
// launch preflight before a run row exists. It never stops unfinished work.
func (s *Scheduler) WithInactiveWorkspace(ctx context.Context, id domain.WorkspaceID, cleanup func([]*domain.Run) error) error {
	lock := s.workspaceLock(id)
	if !lock.TryLock() {
		return fmt.Errorf("%w: workspace has a launch or relaunch in progress", ErrInvalidTransition)
	}
	defer lock.Unlock()
	if _, err := s.cfg.Store.GetWorkspace(ctx, id); err != nil {
		return err
	}
	runs, err := s.cfg.Store.ListRunsByWorkspace(ctx, id)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if !run.Status.Terminal() {
			return fmt.Errorf("%w: run %s is %s; close or stop unfinished runs before deleting the workspace", ErrInvalidTransition, run.ID, run.Status)
		}
		s.mu.Lock()
		entry := s.runs[run.ID]
		busy := s.pending[run.ID] != nil || (entry != nil && (!entry.status.Terminal() || entry.finalizing || entry.destroyPending || !entry.retained))
		s.mu.Unlock()
		if busy {
			return fmt.Errorf("%w: run %s is still active or cleaning up", ErrInvalidTransition, run.ID)
		}
		sc, err := s.readSidecar(run.ID)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (sc.DestroyPending || sc.EvidencePending || (sc.ContainerID != "" && !sc.Retained && sc.RetainedUntil == nil)) {
			return fmt.Errorf("%w: run %s has pending runtime cleanup", ErrInvalidTransition, run.ID)
		}
	}
	return cleanup(runs)
}
