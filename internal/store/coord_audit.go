package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const (
	CoordAuditMessage = "coord.message"
	CoordAuditAcked   = "coord.message.acked"
)

// CoordAuditPublication is an unpublished event: a sent message or its
// acknowledgement. It has no foreign key to the mailbox or runs, so run
// deletion cannot erase a pending or quarantined retry, and it is deleted
// once published. It never holds the message body.
type CoordAuditPublication struct {
	EventID         string
	EventType       string
	MessageID       string
	WorkspaceID     domain.WorkspaceID
	MissionID       domain.MissionID
	FromRun         domain.RunID
	ToRun           domain.RunID
	Kind            string
	CorrelationID   string
	AckedAt         *time.Time
	CreatedAt       time.Time
	Attempts        int
	NextAttemptAt   time.Time
	LastError       string
	QuarantinedAt   *time.Time
	QuarantineError string
}

// CoordAuditStore is the optional durable audit outbox seam. The SQLite DB
// implements it; narrow compatibility stores may omit it and therefore do
// not claim durable coordination-message projections.
type CoordAuditStore interface {
	GetCoordAuditPublication(context.Context, string) (*CoordAuditPublication, error)
	ListPendingCoordAuditPublications(context.Context, int) ([]*CoordAuditPublication, error)
	MarkCoordAuditPublished(context.Context, string) error
}

var _ CoordAuditStore = (*DB)(nil)

const (
	coordMessageEventPrefix = "coord-message:"
	coordAckedEventPrefix   = "coord-message-acked:"
)

func CoordAuditEventID(messageID string) string {
	return coordMessageEventPrefix + messageID
}

func CoordAuditAckedEventID(messageID string) string {
	return coordAckedEventPrefix + messageID
}

// messageKindSQL reads a run_messages row aliased m as its listed kind: a
// worker report forwarded to the integrator is a message correlated with
// the sender's coord_reports row.
const messageKindSQL = `CASE WHEN m.kind = 'message' AND EXISTS (
	SELECT 1 FROM coord_reports report WHERE report.id = m.correlation_id AND report.run_id = m.from_run
) THEN 'report' ELSE m.kind END`

const coordAuditCols = `event_id, event_type, message_id, workspace_id, mission_id, from_run, to_run,
	kind, correlation_id, acked_at, created_at,
	attempts, next_attempt_at, last_error, quarantined_at, quarantine_error`

func enqueueCoordAudit(ctx context.Context, tx *sql.Tx, eventType, where string, args ...any) error {
	prefix, createdAt := coordMessageEventPrefix, `m.created_at`
	if eventType == CoordAuditAcked {
		prefix, createdAt = coordAckedEventPrefix, `m.acked_at`
	}
	query := `INSERT INTO coord_audit_publications
		(event_id, event_type, message_id, workspace_id, mission_id, from_run, to_run,
		 kind, correlation_id, acked_at, created_at)
		SELECT ? || m.id, ?, m.id, m.workspace_id, COALESCE(m.mission_id, ''), m.from_run, m.to_run,
		       ` + messageKindSQL + `, m.correlation_id, m.acked_at, ` + createdAt + `
		FROM run_messages m WHERE ` + where + `
		ON CONFLICT (event_id) DO NOTHING`
	if _, err := tx.ExecContext(ctx, query, append([]any{prefix, eventType}, args...)...); err != nil {
		return fmt.Errorf("audit outbox: %w", err)
	}
	return nil
}

func (d *DB) GetCoordAuditPublication(ctx context.Context, eventID string) (*CoordAuditPublication, error) {
	if eventID == "" {
		return nil, errors.New("store: get coord audit publication: event_id is required")
	}
	pub, err := scanCoordAuditPublication(d.db.QueryRowContext(ctx,
		`SELECT `+coordAuditCols+` FROM coord_audit_publications WHERE event_id = ?`, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get coord audit publication %s: %w", eventID, err)
	}
	return pub, nil
}

func (d *DB) ListPendingCoordAuditPublications(ctx context.Context, limit int) ([]*CoordAuditPublication, error) {
	return d.ListPendingCoordAuditPublicationsAfter(ctx, CoordOutboxCursor{}, limit)
}

func (d *DB) ListPendingCoordAuditPublicationsAfter(ctx context.Context, cursor CoordOutboxCursor, limit int) ([]*CoordAuditPublication, error) {
	if limit <= 0 {
		return nil, errors.New("store: list coord audit publications: limit must be positive")
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("store: list coord audit publications: %w", err)
	}
	query := `SELECT ` + coordAuditCols + `
	          FROM coord_audit_publications
	          WHERE quarantined_at IS NULL AND (next_attempt_at = 0 OR next_attempt_at <= ?)`
	args := []any{now}
	if !cursor.CreatedAt.IsZero() {
		created, cerr := encodeTime(cursor.CreatedAt)
		if cerr != nil {
			return nil, fmt.Errorf("store: list coord audit publications: %w", cerr)
		}
		query += ` AND (created_at > ? OR (created_at = ? AND event_id > ?))`
		args = append(args, created, created, cursor.ID)
	}
	query += ` ORDER BY created_at, event_id LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list coord audit publications: %w", err)
	}
	out, err := collect(rows, scanCoordAuditPublication)
	if err != nil {
		return nil, fmt.Errorf("store: list coord audit publications: %w", err)
	}
	return out, nil
}

// MarkCoordAuditPublished deletes the publication. A row already gone was
// published by the other of the send path and the outbox loop.
func (d *DB) MarkCoordAuditPublished(ctx context.Context, eventID string) error {
	if eventID == "" {
		return errors.New("store: mark coord audit published: event_id is required")
	}
	res, err := d.db.ExecContext(ctx,
		`DELETE FROM coord_audit_publications WHERE event_id = ? AND quarantined_at IS NULL`, eventID)
	if err != nil {
		return fmt.Errorf("store: mark coord audit published: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: mark coord audit published: %w", err)
	}
	if deleted > 0 {
		return nil
	}
	if _, err := d.GetCoordAuditPublication(ctx, eventID); errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("store: mark coord audit published: publication is quarantined")
}

func (d *DB) RecordCoordAuditPublicationFailure(ctx context.Context, eventID, lastError string, nextAttemptAt time.Time, quarantine bool) error {
	if eventID == "" {
		return errors.New("store: record coord audit publication failure: event_id is required")
	}
	if len(lastError) > 4096 {
		lastError = lastError[:4096]
	}
	next := int64(0)
	if !nextAttemptAt.IsZero() {
		var err error
		next, err = encodeTime(nextAttemptAt.UTC())
		if err != nil {
			return fmt.Errorf("store: record coord audit publication failure: %w", err)
		}
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store: record coord audit publication failure: %w", err)
	}
	query := `UPDATE coord_audit_publications
		SET attempts = attempts + 1, next_attempt_at = ?, last_error = ?`
	args := []any{next, lastError}
	if quarantine {
		query += `, quarantined_at = ?, quarantine_error = ?`
		args = append(args, now, lastError)
	}
	query += ` WHERE event_id = ? AND quarantined_at IS NULL`
	args = append(args, eventID)
	if _, err := d.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: record coord audit publication failure: %w", err)
	}
	return nil
}

func scanCoordAuditPublication(row interface{ Scan(...any) error }) (*CoordAuditPublication, error) {
	var (
		pub           CoordAuditPublication
		ackedAt       *int64
		createdAt     int64
		nextAttemptAt int64
		quarantinedAt *int64
	)
	if err := row.Scan(&pub.EventID, &pub.EventType, &pub.MessageID, &pub.WorkspaceID, &pub.MissionID,
		&pub.FromRun, &pub.ToRun, &pub.Kind, &pub.CorrelationID, &ackedAt,
		&createdAt, &pub.Attempts, &nextAttemptAt, &pub.LastError,
		&quarantinedAt, &pub.QuarantineError); err != nil {
		return nil, err
	}
	pub.AckedAt = decodeTimePtr(ackedAt)
	pub.CreatedAt = decodeTime(createdAt)
	if nextAttemptAt != 0 {
		pub.NextAttemptAt = decodeTime(nextAttemptAt)
	}
	pub.QuarantinedAt = decodeTimePtr(quarantinedAt)
	return &pub, nil
}
