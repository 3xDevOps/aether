package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func (s *Scheduler) StorageRetention(ctx context.Context, run domain.RunID) (*time.Time, string, error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, "", err
	}
	if !r.Status.Terminal() {
		return nil, "Active run", nil
	}
	var sc sidecar
	s.mu.Lock()
	entry := s.runs[run]
	live := entry != nil
	finalizing := live && entry.finalizing
	if live {
		sc.RunID, sc.ContainerID = string(run), string(entry.containerID)
		sc.DestroyPending, sc.EvidencePending = entry.destroyPending, entry.evidencePending
		if entry.retainedUntil != nil {
			until := *entry.retainedUntil
			sc.RetainedUntil = &until
		}
	}
	s.mu.Unlock()
	if finalizing {
		return sc.RetainedUntil, "Run finalization in progress", nil
	}
	if !live {
		sc, err = s.readSidecar(run)
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		if err != nil {
			return nil, "", fmt.Errorf("scheduler: read storage ownership for %s: %w", run, err)
		}
	}
	if sc.RunID != string(run) {
		return nil, "", fmt.Errorf("scheduler: storage ownership does not match run %s", run)
	}
	switch {
	case sc.EvidencePending:
		return sc.RetainedUntil, "Evidence preservation pending; files protected", nil
	case sc.DestroyPending:
		return sc.RetainedUntil, "Execution cleanup pending; files protected", nil
	case sc.ContainerID != "":
		if sc.RetainedUntil != nil && !time.Now().Before(*sc.RetainedUntil) {
			return sc.RetainedUntil, "Retention expired; waiting for execution cleanup", nil
		}
		return sc.RetainedUntil, "Retained execution environment; files protected", nil
	default:
		return nil, "", nil
	}
}
