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
}

type RunMessageFilter struct {
	WorkspaceID   domain.WorkspaceID
	RunID         domain.RunID
	CorrelationID string
	Before        string
	Limit         int
}

// ListedRunMessage.Report is set when the message forwards the sender's
// child's report to its parent.
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
		return nil, errors.New("store: list session messages: workspace_id is required")
	}
	limit := normalizeCollaborationLimit(f.Limit)
	query := `SELECT m.rowid, m.id, m.workspace_id, m.from_run, m.to_run, m.body, m.kind,
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
	if f.CorrelationID != "" {
		query += ` AND m.correlation_id = ?`
		args = append(args, f.CorrelationID)
	}
	if beforeAt, beforeRowid, ok := decodeMessageCursor(f.Before); ok {
		query += ` AND (m.created_at, m.rowid) < (?, ?)`
		args = append(args, beforeAt, beforeRowid)
	} else if f.Before != "" {
		// A cursor issued before cursors carried the rowid holds the message ID.
		beforeAt, beforeID, ok := decodeEvidenceCursor(f.Before)
		if !ok {
			return nil, fmt.Errorf("store: list session messages: %w %q", ErrInvalidCursor, f.Before)
		}
		query += ` AND (m.created_at, m.rowid) < (?, (SELECT rowid FROM run_messages WHERE id = ?))`
		args = append(args, beforeAt.UnixNano(), beforeID)
	}
	query += ` ORDER BY m.created_at DESC, m.rowid DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list session messages: %w", err)
	}
	var rowids []int64
	items, err := collect(rows, scanWithRowid(scanListedRunMessage, &rowids))
	if err != nil {
		return nil, fmt.Errorf("store: list session messages: %w", err)
	}
	page := &RunMessagePage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextBefore = encodeMessageCursor(page.Items[limit-1].CreatedAt, rowids[limit-1])
	}
	return page, nil
}

func (d *DB) LastSentRunMessages(ctx context.Context, from domain.RunID) (map[domain.RunID]*RunMessage, error) {
	// rowid, not created_at: created_at is stamped before the write lock, so
	// concurrent sends can commit out of timestamp order.
	rows, err := d.db.QueryContext(ctx, `SELECT `+runMessageCols+` FROM run_messages
		WHERE rowid IN (SELECT MAX(rowid) FROM run_messages WHERE from_run = ? GROUP BY to_run)`, from)
	if err != nil {
		return nil, fmt.Errorf("store: last sent session messages for %s: %w", from, err)
	}
	msgs, err := collect(rows, scanRunMessage)
	if err != nil {
		return nil, fmt.Errorf("store: last sent session messages for %s: %w", from, err)
	}
	byPeer := make(map[domain.RunID]*RunMessage, len(msgs))
	for _, m := range msgs {
		byPeer[m.ToRun] = m
	}
	return byPeer, nil
}

func scanListedRunMessage(row interface{ Scan(...any) error }) (*ListedRunMessage, error) {
	var (
		m                               ListedRunMessage
		createdAt                       int64
		deliveredAt, ackedAt, retiredAt *int64
		outcome, summary, nextAction    sql.NullString
	)
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.FromRun, &m.ToRun, &m.Body,
		&m.Kind, &m.CorrelationID, &m.IdempotencyKey, &m.DeliveryToken,
		&createdAt, &deliveredAt, &ackedAt, &retiredAt,
		&outcome, &summary, &nextAction); err != nil {
		return nil, err
	}
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
