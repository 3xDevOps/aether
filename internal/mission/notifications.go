package mission

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

func (s *Service) missionChanged(ctx context.Context, missionID domain.MissionID, kind domain.MissionChange, by domain.RunID) error {
	if err := s.cfg.Missions.RecordMissionChange(ctx, missionID, kind, by); err != nil {
		return fmt.Errorf("mission: count %s on %q: %w", kind, missionID, err)
	}
	return s.publishMissionChanged(ctx, missionID)
}

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
			ChangeSeq:            mission.ChangeSeq,
		},
	}); err != nil {
		return fmt.Errorf("mission: publish changed event for %q: %w", mission.ID, err)
	}
	return nil
}
