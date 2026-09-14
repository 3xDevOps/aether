package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// RoomMessageKind classifies a durable message in a run room.
type RoomMessageKind string

const (
	RoomMessageComment      RoomMessageKind = "comment"
	RoomMessageSteerRequest RoomMessageKind = "steer_request"
	RoomMessageQuestion     RoomMessageKind = "question"
	RoomMessageReply        RoomMessageKind = "reply"
	RoomMessageSystem       RoomMessageKind = "system"
)

func (k RoomMessageKind) Valid() bool {
	switch k {
	case RoomMessageComment, RoomMessageSteerRequest, RoomMessageQuestion, RoomMessageReply, RoomMessageSystem:
		return true
	default:
		return false
	}
}

// RoomMessageState is the durable delivery/decision state of a room message.
type RoomMessageState string

const (
	RoomMessageQueued    RoomMessageState = "queued"
	RoomMessageSent      RoomMessageState = "sent"
	RoomMessageNotSent   RoomMessageState = "not_sent"
	RoomMessageUncertain RoomMessageState = "uncertain"
	RoomMessageDenied    RoomMessageState = "denied"
	RoomMessageCancelled RoomMessageState = "cancelled"
)

func (s RoomMessageState) Valid() bool {
	switch s {
	case RoomMessageQueued, RoomMessageSent, RoomMessageNotSent, RoomMessageUncertain, RoomMessageDenied, RoomMessageCancelled:
		return true
	default:
		return false
	}
}

// RoomAnchor identifies a bounded location in a diff or transcript. Path is
// an opaque container-visible reference, never a server host path.
type RoomAnchor struct {
	Kind             string `json:"kind,omitempty"`
	Path             string `json:"path,omitempty"`
	StartLine        int    `json:"start_line,omitempty"`
	EndLine          int    `json:"end_line,omitempty"`
	TranscriptOffset int64  `json:"transcript_offset,omitempty"`
}

// RoomMessageFailure is intentionally limited to sanitized, operator-safe
// failure information. Callers must not put host paths, credentials, or raw
// subprocess output in it.
type RoomMessageFailure struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// RoomMessage is one durable run-room message. Attachments are opaque
// container-visible references and are not interpreted by the store.
type RoomMessage struct {
	ID          string
	WorkspaceID domain.WorkspaceID
	RunID       domain.RunID
	// ActorID is retained after member removal as the immutable identity that
	// authored the message. ActorDisplayName is the display snapshot captured
	// when the message is created; neither field grants current access.
	ActorID          domain.MemberID
	ActorDisplayName string
	Kind             RoomMessageKind
	Body             string
	Attachments      []string
	Anchor           *RoomAnchor
	CorrelationID    string
	IdempotencyKey   string
	State            RoomMessageState
	// DeliverAfter is the server-selected instant at which delivery may be
	// attempted. A nil value means the message is immediately settleable.
	DeliverAfter *time.Time
	DecidedBy    domain.MemberID
	DecidedAt    *time.Time
	DeliveredAt  *time.Time
	Failure      *RoomMessageFailure
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const (
	// MaxCollaborationPageSize bounds every room and evidence list query.
	MaxCollaborationPageSize     = 100
	DefaultCollaborationPageSize = 50
)

// RoomMessagePage uses an opaque cursor. Items are
// newest first; NextBefore is empty when the page is exhausted.
type RoomMessagePage struct {
	Items      []*RoomMessage
	NextBefore string
}

// RunProtectionStore combines a run protection update with cancellation of
// every still-queued steer in one SQLite transaction. Callers publish the
// returned rows only after this method commits.
type RunProtectionStore interface {
	SetRunProtectedAndCancelQueuedSteerRequests(context.Context, domain.RunID, bool, domain.MemberID, time.Time) ([]*RoomMessage, error)
}

// RoomMessageStore is the durable run-room message persistence surface.
type RoomMessageStore interface {
	CreateRoomMessage(context.Context, *RoomMessage) error
	GetRoomMessage(context.Context, string) (*RoomMessage, error)
	ListRoomMessages(context.Context, domain.WorkspaceID, domain.RunID, string, int) (*RoomMessagePage, error)
	// ClaimRoomMessage atomically moves one queued row to uncertain. A
	// normal claim requires the row to be due; force ignores its deadline and
	// records the approving member and timestamp. The boolean is false when
	// another claimant or a moderation decision already won.
	ClaimRoomMessage(context.Context, string, time.Time, bool, string) (bool, error)
	// CancelQueuedSteerRequests atomically cancels every queued steering row
	// for one run and returns the rows that this call won.
	CancelQueuedSteerRequests(context.Context, domain.RunID, domain.MemberID, time.Time) ([]*RoomMessage, error)
	// TransitionRoomMessage atomically settles a claimed row or resolves an
	// uncertain delivery. A settled decision cannot be overwritten.
	TransitionRoomMessage(context.Context, string, RoomMessageState, *time.Time, *RoomMessageFailure) error
	// DecideRoomMessage atomically records a moderation decision only when the
	// row is still in from. It returns false when another transition won.
	DecideRoomMessage(context.Context, string, RoomMessageState, RoomMessageState, string, time.Time) (bool, error)
}

// CollaborationStore is the durable Release A collaboration surface.
type CollaborationStore interface {
	RoomMessageStore
}

var _ CollaborationStore = (*DB)(nil)

const roomMessageCols = `id, workspace_id, run_id, actor_id, actor_display_name, kind, body, attachments, anchor, correlation_id, idempotency_key, state, deliver_after, decided_by, decided_at, delivered_at, failure, created_at, updated_at`

func marshalCollaborationJSON(v any, empty string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return empty, nil
	}
	return string(b), nil
}
func (d *DB) CreateRoomMessage(ctx context.Context, m *RoomMessage) error {
	if m == nil || m.WorkspaceID == "" || m.RunID == "" || m.ActorID == "" || m.Kind == "" || m.IdempotencyKey == "" {
		return errors.New("store: create room message: workspace_id, run_id, actor_id, kind, and idempotency_key are required")
	}
	if !m.Kind.Valid() {
		return fmt.Errorf("store: create room message: invalid kind %q", m.Kind)
	}
	var actorDisplayName string
	if err := d.db.QueryRowContext(ctx, `SELECT display_name FROM members WHERE id = ?`, m.ActorID).Scan(&actorDisplayName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: create room message actor: %w", err)
	}
	m.ActorDisplayName = actorDisplayName
	attachments, err := marshalCollaborationJSON(m.Attachments, "[]")
	if err != nil {
		return fmt.Errorf("store: create room message attachments: %w", err)
	}
	anchor, err := marshalCollaborationJSON(m.Anchor, "")
	if err != nil {
		return fmt.Errorf("store: create room message anchor: %w", err)
	}
	id, created, err := prepareCreate(m.CreatedAt)
	if err != nil {
		return err
	}
	deliverAfter, err := encodeTimePtr(m.DeliverAfter)
	if err != nil {
		return fmt.Errorf("store: create room message deliver after: %w", err)
	}
	state := RoomMessageQueued
	var deliveredAt *int64
	if m.Kind != RoomMessageSteerRequest {
		// Room-only records are immutable facts, never moderation candidates.
		// Their final state is established in the INSERT itself.
		state = RoomMessageSent
		if m.DeliveredAt == nil {
			delivered := created
			m.DeliveredAt = &delivered
		}
		encodedDeliveredAt, encodeErr := encodeTime(*m.DeliveredAt)
		if encodeErr != nil {
			return fmt.Errorf("store: create room message delivered at: %w", encodeErr)
		}
		deliveredAt = &encodedDeliveredAt
		deliverAfter = nil
	}
	updated := m.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	createdAt, err := encodeTime(created)
	if err != nil {
		return fmt.Errorf("store: create room message: %w", err)
	}
	updatedAt, err := encodeTime(updated)
	if err != nil {
		return fmt.Errorf("store: create room message: %w", err)
	}
	res, err := d.db.ExecContext(ctx, `INSERT INTO room_messages (`+roomMessageCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, ?, NULL, ?, ?)
		ON CONFLICT (actor_id, run_id, idempotency_key) DO NOTHING`,
		id, m.WorkspaceID, m.RunID, m.ActorID, m.ActorDisplayName, m.Kind, m.Body, attachments, nullableJSON(anchor),
		m.CorrelationID, m.IdempotencyKey, state, deliverAfter, deliveredAt, createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("store: create room message: %w", mapConstraint(err, ErrNotFound))
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create room message: %w", err)
	}
	if inserted == 0 {
		stored, gerr := d.GetRoomMessageByIdempotency(ctx, m.ActorID, m.RunID, m.IdempotencyKey)
		if gerr != nil {
			return fmt.Errorf("store: create room message idempotency lookup: %w", gerr)
		}
		*m = *stored
		return nil
	}
	m.ID, m.State, m.CreatedAt, m.UpdatedAt = id, state, created, updated
	m.DeliverAfter = decodeTimePtr(deliverAfter)
	m.DecidedBy, m.DecidedAt, m.Failure = "", nil, nil
	if deliveredAt == nil {
		m.DeliveredAt = nil
	}
	return nil
}

func nullableJSON(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (d *DB) GetRoomMessageByIdempotency(ctx context.Context, actor domain.MemberID, run domain.RunID, key string) (*RoomMessage, error) {
	return scanRoomMessage(d.db.QueryRowContext(ctx, `SELECT `+roomMessageCols+` FROM room_messages WHERE actor_id = ? AND run_id = ? AND idempotency_key = ?`, actor, run, key))
}

func (d *DB) GetRoomMessage(ctx context.Context, id string) (*RoomMessage, error) {
	m, err := scanRoomMessage(d.db.QueryRowContext(ctx, `SELECT `+roomMessageCols+` FROM room_messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get room message: %w", err)
	}
	return m, nil
}

func normalizeCollaborationLimit(limit int) int {
	if limit <= 0 {
		return DefaultCollaborationPageSize
	}
	if limit > MaxCollaborationPageSize {
		return MaxCollaborationPageSize
	}
	return limit
}

func (d *DB) ListRoomMessages(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, before string, limit int) (*RoomMessagePage, error) {
	if workspace == "" {
		return nil, errors.New("store: list room messages: workspace_id is required")
	}
	limit = normalizeCollaborationLimit(limit)
	query := `SELECT ` + roomMessageCols + ` FROM room_messages WHERE workspace_id = ?`
	args := []any{workspace}
	if run != "" {
		query += ` AND run_id = ?`
		args = append(args, run)
	}
	if before != "" {
		query += ` AND (created_at, id) < (SELECT created_at, id FROM room_messages WHERE id = ?)`
		args = append(args, before)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list room messages: %w", err)
	}
	items, err := collect(rows, scanRoomMessage)
	if err != nil {
		return nil, err
	}
	page := &RoomMessagePage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextBefore = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

// ClaimRoomMessage atomically reserves one queued message for delivery. The
// uncertain state is intentional: a process crash after the PTY write leaves
// a durable outcome that a retry can resolve without allowing moderation to
// overwrite the claim.
func (d *DB) ClaimRoomMessage(ctx context.Context, id string, now time.Time, force bool, decidedBy string) (bool, error) {
	if id == "" {
		return false, errors.New("store: claim room message: id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowN, err := encodeTime(now)
	if err != nil {
		return false, fmt.Errorf("store: claim room message: %w", err)
	}
	updatedAt, err := encodeTime(time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("store: claim room message: %w", err)
	}
	var query string
	var args []any
	if force {
		if decidedBy == "" {
			return false, errors.New("store: claim room message: decided_by is required for force")
		}
		query = `UPDATE room_messages
			SET state = ?, decided_by = ?, decided_at = ?, updated_at = ?
			WHERE id = ? AND state = ?`
		args = []any{RoomMessageUncertain, decidedBy, nowN, updatedAt, id, RoomMessageQueued}
	} else {
		query = `UPDATE room_messages SET state = ?, updated_at = ?
			WHERE id = ? AND state = ?
			  AND (deliver_after IS NULL OR deliver_after <= ?)`
		args = []any{RoomMessageUncertain, updatedAt, id, RoomMessageQueued, nowN}
	}
	res, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("store: claim room message: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: claim room message: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	if _, err := d.GetRoomMessage(ctx, id); errors.Is(err, ErrNotFound) {
		return false, ErrNotFound
	}
	return false, nil
}

// CancelQueuedSteerRequests atomically fences every queued steer for a run.
// The write-locking no-op update is deliberate: it prevents a read-to-write
// SQLite snapshot upgrade from allowing a concurrent claimant to slip in
// between the candidate read and the cancellation update.
func (d *DB) CancelQueuedSteerRequests(ctx context.Context, run domain.RunID, by domain.MemberID, decidedAt time.Time) ([]*RoomMessage, error) {
	if run == "" || by == "" {
		return nil, errors.New("store: cancel queued steer requests: run and decided_by are required")
	}
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	decidedAtN, err := encodeTime(decidedAt)
	if err != nil {
		return nil, fmt.Errorf("store: cancel queued steer requests: %w", err)
	}
	updatedAt, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("store: cancel queued steer requests: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: cancel queued steer requests: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, lockErr := tx.ExecContext(ctx, `UPDATE room_messages SET updated_at = updated_at WHERE run_id = ? AND kind = ? AND state = ?`, run, RoomMessageSteerRequest, RoomMessageQueued); lockErr != nil {
		return nil, fmt.Errorf("store: cancel queued steer requests: lock: %w", lockErr)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM room_messages WHERE run_id = ? AND kind = ? AND state = ? ORDER BY created_at, id`, run, RoomMessageSteerRequest, RoomMessageQueued)
	if err != nil {
		return nil, fmt.Errorf("store: cancel queued steer requests: select: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("store: cancel queued steer requests: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("store: cancel queued steer requests: rows: %w", err)
	}
	_ = rows.Close()
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("store: cancel queued steer requests: commit: %w", err)
		}
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE room_messages SET state = ?, decided_by = ?, decided_at = ?, updated_at = ?
		WHERE run_id = ? AND kind = ? AND state = ?`, RoomMessageCancelled, by, decidedAtN, updatedAt, run, RoomMessageSteerRequest, RoomMessageQueued); err != nil {
		return nil, fmt.Errorf("store: cancel queued steer requests: update: %w", err)
	}
	messages := make([]*RoomMessage, 0, len(ids))
	for _, id := range ids {
		msg, err := scanRoomMessage(tx.QueryRowContext(ctx, `SELECT `+roomMessageCols+` FROM room_messages WHERE id = ?`, id))
		if err != nil {
			return nil, fmt.Errorf("store: cancel queued steer requests: read: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: cancel queued steer requests: commit: %w", err)
	}
	return messages, nil
}

// SetRunProtectedAndCancelQueuedSteerRequests changes protection and, when
// enabling it, settles every still-deliverable steer in the same transaction.
// The run update acquires SQLite's writer lock before candidate selection, so
// no claimant can commit between the protection decision and cancellation.
func (d *DB) SetRunProtectedAndCancelQueuedSteerRequests(ctx context.Context, run domain.RunID, protected bool, by domain.MemberID, decidedAt time.Time) ([]*RoomMessage, error) {
	if run == "" {
		return nil, errors.New("store: protect run: run is required")
	}
	if protected && by == "" {
		return nil, errors.New("store: protect run: decided_by is required")
	}
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	decidedAtN, err := encodeTime(decidedAt)
	if err != nil {
		return nil, fmt.Errorf("store: protect run: %w", err)
	}
	updatedAt, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("store: protect run: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: protect run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE runs SET protected = ? WHERE id = ?`, protected, run)
	if err != nil {
		return nil, fmt.Errorf("store: protect run: update: %w", err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, fmt.Errorf("store: protect run: rows: %w", rowsErr)
	} else if n == 0 {
		return nil, ErrNotFound
	}
	if !protected {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, fmt.Errorf("store: protect run: commit: %w", commitErr)
		}
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM room_messages WHERE run_id = ? AND kind = ? AND state = ? ORDER BY created_at, id`, run, RoomMessageSteerRequest, RoomMessageQueued)
	if err != nil {
		return nil, fmt.Errorf("store: protect run: select: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("store: protect run: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("store: protect run: rows: %w", err)
	}
	_ = rows.Close()
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("store: protect run: commit: %w", err)
		}
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE room_messages SET state = ?, decided_by = ?, decided_at = ?, updated_at = ?
		WHERE run_id = ? AND kind = ? AND state = ?`, RoomMessageCancelled, by, decidedAtN, updatedAt, run, RoomMessageSteerRequest, RoomMessageQueued); err != nil {
		return nil, fmt.Errorf("store: protect run: cancel: %w", err)
	}
	messages := make([]*RoomMessage, 0, len(ids))
	for _, id := range ids {
		msg, err := scanRoomMessage(tx.QueryRowContext(ctx, `SELECT `+roomMessageCols+` FROM room_messages WHERE id = ?`, id))
		if err != nil {
			return nil, fmt.Errorf("store: protect run: read: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: protect run: commit: %w", err)
	}
	return messages, nil
}
func (d *DB) TransitionRoomMessage(ctx context.Context, id string, state RoomMessageState, deliveredAt *time.Time, failure *RoomMessageFailure) error {
	if id == "" {
		return errors.New("store: transition room message: id is required")
	}
	if !state.Valid() || state == RoomMessageQueued || state == RoomMessageDenied || state == RoomMessageCancelled {
		return fmt.Errorf("store: transition room message: invalid target state %q", state)
	}
	var delivery any
	if deliveredAt != nil {
		n, err := encodeTime(*deliveredAt)
		if err != nil {
			return fmt.Errorf("store: transition room message: %w", err)
		}
		delivery = n
	}
	failureJSON, err := marshalCollaborationJSON(failure, "")
	if err != nil {
		return fmt.Errorf("store: transition room message failure: %w", err)
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return err
	}
	// Delivery may claim a queued row only once it is due. Resolving an
	// uncertain delivery may record sent, not_sent, or uncertain again, but
	// none of these paths can overwrite a moderation decision.
	query := `UPDATE room_messages SET state = ?, delivered_at = ?, failure = ?, updated_at = ?
		WHERE id = ? AND (
			(state = ? AND (deliver_after IS NULL OR deliver_after <= ?))
			OR (state = ? AND ? IN (?, ?, ?))
		)`
	res, err := d.db.ExecContext(ctx, query, state, delivery, nullableJSON(failureJSON), now, id,
		RoomMessageQueued, now, RoomMessageUncertain, state, RoomMessageSent, RoomMessageNotSent, RoomMessageUncertain)
	if err != nil {
		return fmt.Errorf("store: transition room message: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: transition room message: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := d.GetRoomMessage(ctx, id); errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return fmt.Errorf("store: transition room message %s: %w", id, ErrConflict)
}

func (d *DB) DecideRoomMessage(ctx context.Context, id string, from, to RoomMessageState, decidedBy string, decidedAt time.Time) (bool, error) {
	if id == "" {
		return false, errors.New("store: decide room message: id is required")
	}
	if from != RoomMessageQueued {
		return false, fmt.Errorf("store: decide room message: source state must be queued, got %q", from)
	}
	if to != RoomMessageDenied && to != RoomMessageCancelled {
		return false, fmt.Errorf("store: decide room message: invalid decision state %q", to)
	}
	if decidedBy == "" {
		return false, errors.New("store: decide room message: decided_by is required")
	}
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	decidedAtN, err := encodeTime(decidedAt)
	if err != nil {
		return false, fmt.Errorf("store: decide room message: %w", err)
	}
	updatedAt, err := encodeTime(time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("store: decide room message: %w", err)
	}
	res, err := d.db.ExecContext(ctx, `UPDATE room_messages
		SET state = ?, decided_by = ?, decided_at = ?, updated_at = ?
		WHERE id = ? AND kind = ? AND state = ?`,
		to, decidedBy, decidedAtN, updatedAt, id, RoomMessageSteerRequest, from)
	if err != nil {
		return false, fmt.Errorf("store: decide room message: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: decide room message: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	if _, err := d.GetRoomMessage(ctx, id); errors.Is(err, ErrNotFound) {
		return false, ErrNotFound
	}
	return false, nil
}
func scanRoomMessage(row interface{ Scan(...any) error }) (*RoomMessage, error) {
	var (
		m            RoomMessage
		attachments  sql.NullString
		anchor       sql.NullString
		failure      sql.NullString
		deliverAfter *int64
		decidedAt    *int64
		deliveredAt  *int64
		createdAt    int64
		updatedAt    int64
	)
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.RunID, &m.ActorID, &m.ActorDisplayName, &m.Kind, &m.Body, &attachments, &anchor,
		&m.CorrelationID, &m.IdempotencyKey, &m.State, &deliverAfter, &m.DecidedBy, &decidedAt,
		&deliveredAt, &failure, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if attachments.Valid && attachments.String != "" {
		if err := json.Unmarshal([]byte(attachments.String), &m.Attachments); err != nil {
			return nil, fmt.Errorf("store: decode room message attachments: %w", err)
		}
	}
	if anchor.Valid && anchor.String != "" {
		m.Anchor = &RoomAnchor{}
		if err := json.Unmarshal([]byte(anchor.String), m.Anchor); err != nil {
			return nil, fmt.Errorf("store: decode room message anchor: %w", err)
		}
	}
	if failure.Valid && failure.String != "" {
		m.Failure = &RoomMessageFailure{}
		if err := json.Unmarshal([]byte(failure.String), m.Failure); err != nil {
			return nil, fmt.Errorf("store: decode room message failure: %w", err)
		}
	}
	m.DeliverAfter = decodeTimePtr(deliverAfter)
	m.DecidedAt = decodeTimePtr(decidedAt)
	m.DeliveredAt = decodeTimePtr(deliveredAt)
	m.CreatedAt, m.UpdatedAt = decodeTime(createdAt), decodeTime(updatedAt)
	return &m, nil
}
