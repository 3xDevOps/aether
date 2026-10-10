package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// Release destroys only a terminal run's retained resources. Both the
// terminal check and cleanup share the container's lifecycle lock with
// Relaunch, so a release cannot destroy an active or reopened run.
func (s *Scheduler) Release(ctx context.Context, run domain.RunID, _ domain.MemberID) error {
	for {
		s.mu.Lock()
		entry := s.runs[run]
		if entry == nil {
			current, err := s.cfg.Store.GetRun(ctx, run)
			if err != nil {
				s.mu.Unlock()
				return err
			}
			if !current.Status.Terminal() || s.pending[run] != nil {
				s.mu.Unlock()
				return fmt.Errorf("%w: release requires a finished run (%s)", ErrInvalidTransition, current.Status)
			}
			sc, err := s.readSidecar(run)
			if errors.Is(err, os.ErrNotExist) {
				s.mu.Unlock()
				return nil
			}
			if err != nil {
				s.mu.Unlock()
				return fmt.Errorf("scheduler: inspect retained sidecar: %w", err)
			}
			if sc.RunID != string(run) {
				s.mu.Unlock()
				return fmt.Errorf("scheduler: retained sidecar run ID mismatch")
			}
			s.rememberRetentionLocked(current)
			if sc.DestroyPending && sc.RunUser == "" {
				// A legacy sidecar may lack its ownership metadata. Inspect
				// outside s.mu, then recheck the row and sidecar before admitting
				// a terminal owner. Never admit an active destroy-pending owner
				// from a stale terminal snapshot.
				s.mu.Unlock()
				s.recoverDestroyMetadata(ctx, runtime.ID(sc.ContainerID), &sc)
				s.mu.Lock()
				if s.runs[run] != nil || s.pending[run] != nil {
					s.mu.Unlock()
					continue
				}
				current, err = s.cfg.Store.GetRun(ctx, run)
				if err != nil {
					s.mu.Unlock()
					return err
				}
				if !current.Status.Terminal() {
					s.mu.Unlock()
					return fmt.Errorf("%w: release requires a finished run (%s)", ErrInvalidTransition, current.Status)
				}
				latest, readErr := s.readSidecar(run)
				if errors.Is(readErr, os.ErrNotExist) {
					s.mu.Unlock()
					continue
				}
				if readErr != nil {
					s.mu.Unlock()
					return fmt.Errorf("scheduler: inspect retained sidecar: %w", readErr)
				}
				if latest.RunID != sc.RunID || latest.ContainerID != sc.ContainerID || latest.DestroyPending != sc.DestroyPending {
					s.mu.Unlock()
					continue
				}
				if latest.RunUser == "" {
					latest.RunUser = sc.RunUser
					if latest.Home == "" {
						latest.Home = sc.Home
					}
				}
				sc = latest
			}
			if sc.DestroyPending {
				entry = s.adoptDestroyOwnerLocked(current, sc)
				if entry == nil {
					s.mu.Unlock()
					return fmt.Errorf("scheduler: cannot adopt retained destroy owner")
				}
			} else if sc.ContainerID != "" && (sc.Retained || sc.RetainedUntil != nil) &&
				(sc.Mode != "" || current.Mode != "") {
				entry = s.entryFromSidecar(current, sc)
				entry.retained = true
				s.runs[run] = entry
				s.syncRunUserReservationsLocked()
			} else {
				s.mu.Unlock()
				return fmt.Errorf("%w: release cleanup still in progress", ErrInvalidTransition)
			}
		}
		s.mu.Unlock()

		entry.lifecycleMu.Lock()
		s.mu.Lock()
		if s.runs[run] != entry {
			s.mu.Unlock()
			entry.lifecycleMu.Unlock()
			continue
		}
		current, err := s.cfg.Store.GetRun(ctx, run)
		if err != nil {
			s.mu.Unlock()
			entry.lifecycleMu.Unlock()
			return err
		}
		if !current.Status.Terminal() {
			s.mu.Unlock()
			entry.lifecycleMu.Unlock()
			return fmt.Errorf("%w: release requires a finished run (%s)", ErrInvalidTransition, current.Status)
		}
		retained, destroyPending := entry.retained, entry.destroyPending
		s.mu.Unlock()
		switch {
		case destroyPending:
			err = s.retryDestroyPendingLocked(ctx, entry)
		case retained:
			err = s.expireRetainedLocked(ctx, entry, "")
		default:
			err = fmt.Errorf("%w: release cleanup still in progress", ErrInvalidTransition)
		}
		entry.lifecycleMu.Unlock()
		return err
	}
}
