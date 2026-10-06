package events

import "github.com/3xDevOps/Aether/internal/domain"

const (
	// TypeCoordMessage leaves the body behind coord.messages.list.
	TypeCoordMessage      Type = "coord.message"
	TypeCoordMessageAcked Type = "coord.message.acked"
)

type CoordMessagePayload struct {
	MessageID     string             `json:"message_id"`
	WorkspaceID   domain.WorkspaceID `json:"workspace_id"`
	MissionID     domain.MissionID   `json:"mission_id,omitempty"`
	FromRunID     domain.RunID       `json:"from_run_id"`
	ToRunID       domain.RunID       `json:"to_run_id"`
	Kind          string             `json:"kind"`
	CorrelationID string             `json:"correlation_id,omitempty"`
}

func (CoordMessagePayload) EventType() Type { return TypeCoordMessage }

type CoordMessageAckedPayload struct {
	MessageID string       `json:"message_id"`
	ToRunID   domain.RunID `json:"to_run_id"`
	AckedAt   string       `json:"acked_at"`
}

func (CoordMessageAckedPayload) EventType() Type { return TypeCoordMessageAcked }

func init() {
	registerPayload[CoordMessagePayload](TypeCoordMessage)
	registerPayload[CoordMessageAckedPayload](TypeCoordMessageAcked)
}
