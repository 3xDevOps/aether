package scheduler

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// profileService is the optional scheduler seam for pinning agent profile
// snapshots. Materialization happens directly through profile.Service in
// the profile transport layer.
type profileService interface {
	Latest(ctx context.Context, member, harness string) (domain.ProfileSnapshot, error)
	PinRun(ctx context.Context, runID domain.RunID, id domain.ProfileSnapshotID) error
}

func (s *Scheduler) pinLatestProfile(ctx context.Context, run *domain.Run) error {
	if s.cfg.Profiles == nil {
		return nil
	}
	snap, err := s.cfg.Profiles.Latest(ctx, string(run.MemberID), run.Harness)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := s.cfg.Profiles.PinRun(ctx, run.ID, snap.ID); err != nil {
		return err
	}
	run.ProfileSnapshotID = snap.ID
	return nil
}
