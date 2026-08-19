package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrInboxFull is returned when a run's inbox already holds the maximum
// number of unacknowledged messages. Nothing is ever dropped silently:
// the sender is told the condition by name.
var ErrInboxFull = errors.New("store: run inbox is full")

// RunMessage is one message between two runs the conflict radar put in
// file conflict.
//
// DeliveryToken is empty until a read delivers the message. Every message
// one read returns shares a single token, and acknowledging that token
// acknowledges exactly that batch - which is what makes delivery
// at-least-once: a batch whose response never reached the agent is still
// unacknowledged, so the next read returns it again.
type RunMessage struct {
	ID            string
	SessionID     domain.SessionID
	FromRun       domain.RunID
	ToRun         domain.RunID
	Body          string
	DeliveryToken string
	CreatedAt     time.Time
	DeliveredAt   *time.Time
	AckedAt       *time.Time
}

// MessageStore is the run mailbox's persistence surface.
type MessageStore interface {
	// AppendRunMessage stores m, assigning its ID and CreatedAt when zero.
	// It fails ErrInboxFull when the target already holds maxUnacked
	// unacknowledged messages; the check and the insert are one statement,
	// so concurrent senders cannot race past the cap.
	AppendRunMessage(ctx context.Context, m *RunMessage, maxUnacked int) error
	// CountUnackedRunMessages returns how many messages the run has not
	// acknowledged yet, delivered or not.
	CountUnackedRunMessages(ctx context.Context, to domain.RunID) (int, error)
	// DeliverRunMessages acknowledges ackToken's batch, then returns the
	// run's next batch and the token binding it. An empty, unknown, or
	// another run's token acknowledges nothing. A batch already delivered
	// and not yet acknowledged is returned again under its original token;
	// only when no batch is outstanding is a new one selected, stamped,
	// and tokenized - all in one transaction. An empty inbox returns no
	// messages and no token.
	DeliverRunMessages(ctx context.Context, to domain.RunID, ackToken string, limit int) ([]*RunMessage, string, error)
	// DeleteRunMessages removes every message addressed to the run, acked
	// or not. Called when the run's coordination is released: the run is
	// done reading, and the timeline notes stamped at send time remain the
	// audit trail.
	DeleteRunMessages(ctx context.Context, to domain.RunID) error
}

var _ MessageStore = (*DB)(nil)

const runMessageCols = `id, session_id, from_run, to_run, body, delivery_token, created_at, delivered_at, acked_at`

func (d *DB) AppendRunMessage(ctx context.Context, m *RunMessage, maxUnacked int) error {
	if m.SessionID == "" || m.FromRun == "" || m.ToRun == "" {
		return errors.New("store: append run message: session_id, from_run, and to_run are required")
	}
	if maxUnacked <= 0 {
		return errors.New("store: append run message: maxUnacked must be positive")
	}
	id, ts, err := prepareCreate(m.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: append run message: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO run_messages (`+runMessageCols+`)
		 SELECT ?, ?, ?, ?, ?, '', ?, NULL, NULL
		 WHERE (SELECT COUNT(*) FROM run_messages WHERE to_run = ? AND acked_at IS NULL) < ?`,
		id, m.SessionID, m.FromRun, m.ToRun, m.Body, createdAt, m.ToRun, maxUnacked,
	)
	if err != nil {
		return fmt.Errorf("store: append run message: %w", mapConstraint(err, ErrNotFound))
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: append run message: %w", err)
	}
	if inserted == 0 {
		return fmt.Errorf("store: append run message to %s: %w", m.ToRun, ErrInboxFull)
	}
	m.ID, m.CreatedAt = id, ts
	m.DeliveryToken, m.DeliveredAt, m.AckedAt = "", nil, nil
	return nil
}

func (d *DB) CountUnackedRunMessages(ctx context.Context, to domain.RunID) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_messages WHERE to_run = ? AND acked_at IS NULL`, to,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count unacked run messages for %s: %w", to, err)
	}
	return n, nil
}

func (d *DB) DeliverRunMessages(ctx context.Context, to domain.RunID, ackToken string, limit int) ([]*RunMessage, string, error) {
	if to == "" {
		return nil, "", errors.New("store: deliver run messages: to_run is required")
	}
	if limit <= 0 {
		return nil, "", errors.New("store: deliver run messages: limit must be positive")
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, "", fmt.Errorf("store: deliver run messages: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("store: deliver run messages: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// The acknowledgement runs unconditionally, and first, on purpose. A
	// transaction that reads before it writes has to upgrade to a write
	// lock partway through, and SQLite refuses that upgrade with
	// SQLITE_BUSY_SNAPSHOT when another connection committed in between -
	// which happens whenever the event log, sharing this database file,
	// records an event. That one error is not covered by the busy handler,
	// so the write lock is taken up front instead. No token can match a
	// delivered row while delivery_token <> '' holds, so an absent, unknown,
	// or another run's token still acknowledges nothing.
	if _, aerr := tx.ExecContext(ctx,
		`UPDATE run_messages SET acked_at = ?
		 WHERE to_run = ? AND delivery_token <> '' AND delivery_token = ? AND acked_at IS NULL`,
		now, to, ackToken,
	); aerr != nil {
		return nil, "", fmt.Errorf("store: acknowledge run message batch: %w", aerr)
	}

	outstanding, err := outstandingToken(ctx, tx, to)
	if err != nil {
		return nil, "", err
	}
	if outstanding != "" {
		msgs, rerr := readBatch(ctx, tx,
			`SELECT `+runMessageCols+` FROM run_messages
			 WHERE to_run = ? AND acked_at IS NULL AND delivery_token = ? ORDER BY id`, to, outstanding)
		if rerr != nil {
			return nil, "", rerr
		}
		return msgs, outstanding, commitBatch(tx)
	}

	msgs, err := readBatch(ctx, tx,
		`SELECT `+runMessageCols+` FROM run_messages
		 WHERE to_run = ? AND acked_at IS NULL AND delivery_token = '' ORDER BY id LIMIT ?`, to, limit)
	if err != nil {
		return nil, "", err
	}
	if len(msgs) == 0 {
		return nil, "", commitBatch(tx)
	}
	token, err := newID()
	if err != nil {
		return nil, "", err
	}
	args := make([]any, 0, len(msgs)+2)
	args = append(args, token, now)
	for _, m := range msgs {
		args = append(args, m.ID)
	}
	if _, uerr := tx.ExecContext(ctx,
		`UPDATE run_messages SET delivery_token = ?, delivered_at = ?
		 WHERE id IN (`+placeholders(len(msgs))+`)`, args...,
	); uerr != nil {
		return nil, "", fmt.Errorf("store: stamp run message delivery: %w", uerr)
	}
	delivered := decodeTime(now)
	for _, m := range msgs {
		m.DeliveryToken, m.DeliveredAt = token, &delivered
	}
	return msgs, token, commitBatch(tx)
}

func (d *DB) DeleteRunMessages(ctx context.Context, to domain.RunID) error {
	if to == "" {
		return errors.New("store: delete run messages: to_run is required")
	}
	if _, err := d.db.ExecContext(ctx, `DELETE FROM run_messages WHERE to_run = ?`, to); err != nil {
		return fmt.Errorf("store: delete run messages for %s: %w", to, err)
	}
	return nil
}

// outstandingToken returns the token of the run's delivered but still
// unacknowledged batch, empty when none is outstanding. At most one batch
// can be outstanding, because a new one is only ever selected when this
// returns empty.
func outstandingToken(ctx context.Context, tx *sql.Tx, to domain.RunID) (string, error) {
	var token string
	err := tx.QueryRowContext(ctx,
		`SELECT delivery_token FROM run_messages
		 WHERE to_run = ? AND acked_at IS NULL AND delivery_token <> '' ORDER BY id LIMIT 1`, to,
	).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read outstanding message batch for %s: %w", to, err)
	}
	return token, nil
}

func readBatch(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]*RunMessage, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read run messages: %w", err)
	}
	return collect(rows, scanRunMessage)
}

func commitBatch(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: deliver run messages: commit: %w", err)
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func scanRunMessage(row interface{ Scan(...any) error }) (*RunMessage, error) {
	var (
		m           RunMessage
		createdAt   int64
		deliveredAt *int64
		ackedAt     *int64
	)
	if err := row.Scan(&m.ID, &m.SessionID, &m.FromRun, &m.ToRun, &m.Body,
		&m.DeliveryToken, &createdAt, &deliveredAt, &ackedAt); err != nil {
		return nil, err
	}
	m.CreatedAt = decodeTime(createdAt)
	m.DeliveredAt = decodeTimePtr(deliveredAt)
	m.AckedAt = decodeTimePtr(ackedAt)
	return &m, nil
}
