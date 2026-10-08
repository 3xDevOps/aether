package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// SetArchived hides a Final run from the board (archived true) or
// restores it (false). archiveMu serializes this against Relaunch's own
// restore so the two can never race past each other. Archiving a
// non-Final run is refused; a no-op archive or restore publishes
// nothing and never changes the original archive timestamp.
func (s *Scheduler) SetArchived(ctx context.Context, run domain.RunID, actor domain.MemberID, archived bool) (*domain.Run, error) {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()

	var at *time.Time
	if archived {
		current, err := s.cfg.Store.GetRun(ctx, run)
		if err != nil {
			return nil, err
		}
		if !current.Status.Final() {
			return nil, fmt.Errorf("%w: run is %s; only merged, abandoned, failed or interrupted runs can be archived",
				ErrInvalidTransition, current.Status)
		}
		now := time.Now().UTC()
		at = &now
	}

	changed, err := s.cfg.Store.SetRunArchived(ctx, run, at)
	if err != nil {
		return nil, err
	}
	fresh, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if changed {
		s.publishArchived(ctx, fresh, actor)
	}
	return fresh, nil
}

// SetMissionArchived archives (at != nil) or restores a swarm and its runs
// in one store transaction under archiveMu, then publishes run.archived for
// each run that changed. It reports whether the swarm itself changed.
func (s *Scheduler) SetMissionArchived(ctx context.Context, mission domain.MissionID, runs []domain.RunID, actor domain.MemberID, at *time.Time) (bool, error) {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()

	changed, changedRuns, err := s.cfg.Store.SetMissionArchived(ctx, mission, runs, at)
	if err != nil {
		return false, err
	}
	for _, id := range changedRuns {
		run, err := s.cfg.Store.GetRun(ctx, id)
		if err != nil {
			return changed, fmt.Errorf("scheduler: publish archived run %s: %w", id, err)
		}
		s.publishArchived(ctx, run, actor)
	}
	return changed, nil
}

// publishArchived publishes the run.archived event and its matching
// timeline note, mirroring how run.protect publishes: one typed event
// carrying the new state, and one human-readable timeline entry.
func (s *Scheduler) publishArchived(ctx context.Context, run *domain.Run, actor domain.MemberID) {
	wire := protocol.RunFromDomain(run)
	payload := events.RunArchivedPayload{ArchivedAt: wire.ArchivedAt}
	msg := "run restored from archive"
	if wire.ArchivedAt != nil {
		msg = "run archived"
	}
	s.publish(ctx, events.Event{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
		ActorID:     actor,
		Payload:     payload,
	})
	s.publishTimeline(ctx, run.WorkspaceID, run.ID, actor, events.TimelineNote, msg)
}
