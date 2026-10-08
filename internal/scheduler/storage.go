package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// RetentionInfo describes execution ownership without probing or mutating the
// runtime. CleanupError contains only bounded, public operation diagnostics.
type RetentionInfo struct {
	RetainedUntil  *time.Time
	CleanupPending bool
	CleanupError   string
}

// Retention uses the live owner first, because its latest sidecar write may
// have failed. A missing owner is empty; unreadable ownership remains an error.
func (s *Scheduler) Retention(run *domain.Run) (RetentionInfo, error) {
	info, _, err := s.retention(run)
	return info, err
}

func (s *Scheduler) StorageRetention(ctx context.Context, run domain.RunID) (*time.Time, string, error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, "", err
	}
	info, reason, err := s.retention(r)
	return info.RetainedUntil, reason, err
}

func (s *Scheduler) retention(run *domain.Run) (RetentionInfo, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retentionLocked(run)
}

// retentionLocked shares the snapshot contract with run.retention publication.
// The caller holds s.mu.
func (s *Scheduler) retentionLocked(run *domain.Run) (RetentionInfo, string, error) {
	if run == nil {
		return RetentionInfo{}, "", errors.New("scheduler: retention requires a run")
	}
	var sc sidecar
	entry := s.runs[run.ID]
	live := entry != nil
	finalizing := live && entry.finalizing
	if live {
		sc.RunID, sc.ContainerID = string(run.ID), string(entry.containerID)
		sc.DestroyPending, sc.EvidencePending = entry.destroyPending, entry.evidencePending
		sc.CleanupError = entry.cleanupError
		if entry.retainedUntil != nil {
			until := *entry.retainedUntil
			sc.RetainedUntil = &until
		}
	}
	if !live {
		var err error
		sc, err = s.readSidecar(run.ID)
		if errors.Is(err, os.ErrNotExist) {
			if !run.Status.Terminal() {
				return RetentionInfo{}, "Active run", nil
			}
			return RetentionInfo{}, "", nil
		}
		if err != nil {
			return RetentionInfo{}, "", fmt.Errorf("scheduler: read storage ownership for %s: %w", run.ID, err)
		}
	}
	if sc.RunID != string(run.ID) {
		return RetentionInfo{}, "", fmt.Errorf("scheduler: storage ownership does not match run %s", run.ID)
	}
	info := RetentionInfo{
		CleanupPending: finalizing || sc.EvidencePending || sc.DestroyPending || sc.CleanupError != "",
		CleanupError:   publicCleanupError(sc.CleanupError),
	}
	// A reopened row can precede clearing its old sidecar. Its stale deadline
	// must not appear as an expiry of active work.
	if run.Status.Terminal() {
		info.RetainedUntil = sc.RetainedUntil
	}
	switch {
	case finalizing:
		return info, "Run finalization in progress", nil
	case sc.EvidencePending:
		return info, "Evidence preservation pending; files protected", nil
	case sc.DestroyPending:
		return info, "Execution cleanup pending; files protected", nil
	case !run.Status.Terminal():
		return RetentionInfo{}, "Active run", nil
	case sc.ContainerID != "":
		if sc.RetainedUntil != nil && !time.Now().Before(*sc.RetainedUntil) {
			info.CleanupPending = true
			return info, "Retention expired; waiting for execution cleanup", nil
		}
		return info, "Retained execution environment; files protected", nil
	default:
		return RetentionInfo{}, "", nil
	}
}
