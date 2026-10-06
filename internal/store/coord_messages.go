package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

var ErrInvalidCursor = errors.New("invalid page cursor")

type RunMessageHistoryStore interface {
	ListRunMessages(context.Context, RunMessageFilter) (*RunMessagePage, error)
	ListMissionRunIDs(context.Context, domain.MissionID) ([]domain.RunID, error)
}

type RunMessageFilter struct {
	WorkspaceID   domain.WorkspaceID
	MissionID     domain.MissionID
	RunID         domain.RunID
	CorrelationID string
	Before        string
	Limit         int
}

// ListedRunMessage.Report is set when the message forwards the sender's
// worker report to its integrator.
type ListedRunMessage struct {
	RunMessage
	Report *CoordReport
}

type RunMessagePage struct {
	Items      []*ListedRunMessage
	NextBefore string
}

func (d *DB) ListRunMessages(ctx context.Context, f RunMessageFilter) (*RunMessagePage, error) {
	if f.WorkspaceID == "" {
		return nil, errors.New("store: list run messages: workspace_id is required")
	}
	limit := normalizeCollaborationLimit(f.Limit)
	query := `SELECT m.id, m.workspace_id, m.mission_id, m.from_run, m.to_run, m.body, m.kind,
			m.correlation_id, m.idempotency_key, m.delivery_token, m.created_at, m.delivered_at,
			m.acked_at, m.retired_at, report.outcome, report.summary, report.next_action
		FROM run_messages m
		LEFT JOIN coord_reports report ON m.kind = 'message'
			AND report.id = m.correlation_id AND report.run_id = m.from_run
		WHERE `
	args := []any{f.WorkspaceID}
	if f.RunID != "" {
		// Unary + keeps SQLite off the workspace index, which would walk the
		// workspace's whole history, so it reads the run's from/to indexes.
		query += `+m.workspace_id = ? AND (m.from_run = ? OR m.to_run = ?)`
		args = append(args, f.RunID, f.RunID)
	} else {
		query += `m.workspace_id = ?`
	}
	if f.MissionID != "" {
		query += ` AND m.mission_id = ?`
		args = append(args, f.MissionID)
	}
	if f.CorrelationID != "" {
		query += ` AND m.correlation_id = ?`
		args = append(args, f.CorrelationID)
	}
	if f.Before != "" {
		beforeAt, beforeID, ok := decodeEvidenceCursor(f.Before)
		if !ok {
			return nil, fmt.Errorf("store: list run messages: %w %q", ErrInvalidCursor, f.Before)
		}
		query += ` AND (m.created_at, m.id) < (?, ?)`
		args = append(args, beforeAt.UnixNano(), beforeID)
	}
	query += ` ORDER BY m.created_at DESC, m.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list run messages: %w", err)
	}
	items, err := collect(rows, scanListedRunMessage)
	if err != nil {
		return nil, fmt.Errorf("store: list run messages: %w", err)
	}
	page := &RunMessagePage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.NextBefore = EncodeEvidenceCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func scanListedRunMessage(row interface{ Scan(...any) error }) (*ListedRunMessage, error) {
	var (
		m                               ListedRunMessage
		mission                         sql.NullString
		createdAt                       int64
		deliveredAt, ackedAt, retiredAt *int64
		outcome, summary, nextAction    sql.NullString
	)
	if err := row.Scan(&m.ID, &m.WorkspaceID, &mission, &m.FromRun, &m.ToRun, &m.Body,
		&m.Kind, &m.CorrelationID, &m.IdempotencyKey, &m.DeliveryToken,
		&createdAt, &deliveredAt, &ackedAt, &retiredAt,
		&outcome, &summary, &nextAction); err != nil {
		return nil, err
	}
	m.MissionID = domain.MissionID(mission.String)
	m.CreatedAt = decodeTime(createdAt)
	m.DeliveredAt = decodeTimePtr(deliveredAt)
	m.AckedAt = decodeTimePtr(ackedAt)
	m.RetiredAt = decodeTimePtr(retiredAt)
	if outcome.Valid {
		m.Report = &CoordReport{
			ID: m.CorrelationID, RunID: m.FromRun, Outcome: CoordOutcome(outcome.String),
			Summary: summary.String, NextAction: nextAction.String,
		}
	}
	return &m, nil
}
