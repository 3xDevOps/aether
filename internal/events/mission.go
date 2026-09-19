package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeMissionChanged is a projection hint that a mission's authoritative
// state changed. Consumers must re-read mission.show or mission.list rather
// than infer task, attempt, or submission state from this event.
const TypeMissionChanged Type = "mission.changed"

// MissionChangedPayload identifies the authoritative mission snapshot version
// that caused a projection refresh. The envelope's WorkspaceID scopes delivery;
// these fields let consumers reject stale refresh work when useful.
type MissionChangedPayload struct {
	MissionID            domain.MissionID `json:"mission_id"`
	IntegratorGeneration uint64           `json:"integrator_generation"`
	AcceptedSetVersion   uint64           `json:"accepted_set_version"`
}

func (MissionChangedPayload) EventType() Type { return TypeMissionChanged }

func init() { registerPayload[MissionChangedPayload](TypeMissionChanged) }
