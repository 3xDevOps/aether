package protocol

import (
	"time"

	"github.com/3xDevOps/Aether/internal/store"
)

const MethodCoordMessagesList = "coord.messages.list"

const CoordMessageKindReport = "report"

// CoordMessagesListParams.RunID matches either side of a message.
type CoordMessagesListParams struct {
	WorkspaceID   string `json:"workspace_id"`
	MissionID     string `json:"mission_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	Before        string `json:"before,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type RunMessage struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	MissionID     string  `json:"mission_id,omitempty"`
	FromRunID     string  `json:"from_run_id"`
	ToRunID       string  `json:"to_run_id"`
	Kind          string  `json:"kind"`
	CorrelationID string  `json:"correlation_id,omitempty"`
	Body          string  `json:"body"`
	CreatedAt     string  `json:"created_at"`
	DeliveredAt   *string `json:"delivered_at,omitempty"`
	AckedAt       *string `json:"acked_at,omitempty"`
	Outcome       string  `json:"outcome,omitempty"`
	Summary       string  `json:"summary,omitempty"`
	NextAction    string  `json:"next_action,omitempty"`
}

type CoordMessagesListResult struct {
	Messages   []RunMessage `json:"messages"`
	NextBefore string       `json:"next_before,omitempty"`
}

func CoordMessagesPageFromStore(page *store.RunMessagePage) CoordMessagesListResult {
	out := CoordMessagesListResult{Messages: make([]RunMessage, 0, len(page.Items)), NextBefore: page.NextBefore}
	for _, m := range page.Items {
		row := RunMessage{
			ID: m.ID, WorkspaceID: string(m.WorkspaceID), MissionID: string(m.MissionID),
			FromRunID: string(m.FromRun), ToRunID: string(m.ToRun), Kind: string(m.Kind),
			CorrelationID: m.CorrelationID, Body: m.Body,
			CreatedAt:   m.CreatedAt.UTC().Format(time.RFC3339Nano),
			DeliveredAt: nanoTimePtr(m.DeliveredAt), AckedAt: nanoTimePtr(m.AckedAt),
		}
		if m.Report != nil {
			row.Kind = CoordMessageKindReport
			row.Outcome, row.Summary, row.NextAction = string(m.Report.Outcome), m.Report.Summary, m.Report.NextAction
		}
		out.Messages = append(out.Messages, row)
	}
	return out
}

// nanoTimePtr keeps sub-second order, which a thread of quick replies needs.
func nanoTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}
