package mission

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// publishMissionChanged emits a projection hint only after the caller's
// durable mutation has committed. Consumers must re-read the mission from the
// authoritative mission APIs; the payload carries versions for refresh
// ordering, not a freeform state snapshot.
func (s *Service) publishMissionChanged(ctx context.Context, missionID domain.MissionID) error {
	if s.cfg.Bus == nil {
		return nil
	}
	mission, err := s.cfg.Missions.GetMission(ctx, missionID)
	if err != nil {
		return fmt.Errorf("mission: load changed mission %q for event: %w", missionID, err)
	}
	if mission == nil {
		return errors.New("mission: changed mission is unavailable")
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: mission.WorkspaceID,
		Payload: events.MissionChangedPayload{
			MissionID:            mission.ID,
			IntegratorGeneration: mission.IntegratorGeneration,
			AcceptedSetVersion:   mission.AcceptedSetVersion,
		},
	}); err != nil {
		return fmt.Errorf("mission: publish changed event for %q: %w", mission.ID, err)
	}
	return nil
}
