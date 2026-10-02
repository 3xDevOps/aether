package mission

import (
	"context"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

// ValidateWake is checked inside shared control admission, not while waiting
// for mail. Submitted attempts still occupy capacity but must not be woken.
func (s *Service) ValidateWake(ctx context.Context, run domain.RunID) error {
	m, attempt, err := s.resolveAssignment(ctx, run)
	if err != nil {
		return err
	}
	if m != nil && m.Phase == domain.MissionPhaseRejected {
		return fmt.Errorf("%w: mission has been rejected", store.ErrMissionStale)
	}
	if attempt != nil && !attemptLive(attempt.State) {
		return fmt.Errorf("%w: worker attempt has finished", store.ErrMissionStale)
	}
	return nil
}
