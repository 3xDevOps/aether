package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeIntegrationChanged is a projection hint that an integration
// candidate, one of its verifications, or its delivery was written.
// Consumers must re-read integration.show rather than infer candidate state
// from this event.
const TypeIntegrationChanged Type = "integration.changed"

// IntegrationChangedPayload names the candidate and the durable version the
// write produced. MissionID is empty for a candidate prepared outside a
// mission.
type IntegrationChangedPayload struct {
	MissionID   domain.MissionID `json:"mission_id"`
	CandidateID string           `json:"candidate_id"`
	State       string           `json:"state"`
	Version     int64            `json:"version"`
}

func (IntegrationChangedPayload) EventType() Type { return TypeIntegrationChanged }

func init() { registerPayload[IntegrationChangedPayload](TypeIntegrationChanged) }
