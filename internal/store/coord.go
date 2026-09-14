package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

// ErrIdempotencyConflict means a mutation key was already used for another
// message kind. The key is deliberately shared across kinds so a caller
// cannot turn a send retry into a question or reply receipt.
var ErrIdempotencyConflict = errors.New("store: idempotency key belongs to another message kind")

// ErrCoordPeerLimit means opening a new coordination conversation would
// exceed the durable per-run peer cap.
var ErrCoordPeerLimit = errors.New("store: coordination peer limit reached")

// ErrCoordReportConflict means a run already reserved its one report slot
// under a different idempotency key.
var ErrCoordReportConflict = errors.New("store: run already has a coordination report")

// ErrCoordReportIdempotencyConflict means a retry reused a report key with
// different semantic inputs.
var ErrCoordReportIdempotencyConflict = errors.New("store: coordination report idempotency conflict")

// ErrCoordReportPublicationConflict means a report publication's stable event
// ID does not match the durable outbox row.
var ErrCoordReportPublicationConflict = errors.New("store: coordination report publication conflict")

// ErrCoordAuditPublicationConflict means a coordination audit publication's
// stable event ID does not match its durable outbox row.
var ErrCoordAuditPublicationConflict = errors.New("store: coordination audit publication conflict")

// RunMessageKind classifies coordination messages on the v3 wire.
type RunMessageKind string

const (
	RunMessageKindMessage  RunMessageKind = "message"
	RunMessageKindQuestion RunMessageKind = "question"
	RunMessageKindReply    RunMessageKind = "reply"
)

func (k RunMessageKind) Valid() bool {
	switch k {
	case RunMessageKindMessage, RunMessageKindQuestion, RunMessageKindReply:
		return true
	default:
		return false
	}
}

// CoordReportState records whether a report reservation has been finalized.
type CoordReportState string

const (
	CoordReportPending   CoordReportState = "pending"
	CoordReportFinalized CoordReportState = "finalized"
)

// CoordOutcome classifies a durable worker result.
type CoordOutcome string

const (
	CoordOutcomeSuccess CoordOutcome = "success"
	CoordOutcomeFailure CoordOutcome = "failure"
	CoordOutcomeBlocked CoordOutcome = "blocked"
)

func (o CoordOutcome) Valid() bool {
	return o == CoordOutcomeSuccess || o == CoordOutcomeFailure || o == CoordOutcomeBlocked
}

// DeliveryToken is empty until a read delivers the message. Every message
// one read returns shares a single token, and acknowledging that token
// acknowledges exactly that batch - which is what makes delivery
// at-least-once: a batch whose response never reached the agent is still
// unacknowledged, so the next read returns it again.
type RunMessage struct {
	ID             string
	WorkspaceID    domain.WorkspaceID
	FromRun        domain.RunID
	ToRun          domain.RunID
	Body           string
	Kind           RunMessageKind
	CorrelationID  string
	IdempotencyKey string
	DeliveryToken  string
	CreatedAt      time.Time
	DeliveredAt    *time.Time
	AckedAt        *time.Time
}

// CoordReport is a durable outcome submitted by a run. A pending report is
// a durable reservation made before evidence capture; only finalized rows are
// externally accepted outcomes. EvidenceRefs are opaque, bounded references
// and never server filesystem paths.
type CoordReport struct {
	ID                string
	WorkspaceID       domain.WorkspaceID
	RunID             domain.RunID
	Outcome           CoordOutcome
	Summary           string
	NextAction        string
	EvidenceRefs      []string
	InputEvidenceRefs []string
	IdempotencyKey    string
	State             CoordReportState
	CreatedAt         time.Time
	FinalizedAt       *time.Time
	PublishedAt       *time.Time
}

// CoordReportPublication is the durable outbox row for a finalized report.
// EventID is stable for the report's lifetime and publication is only marked
// complete after the event log has accepted or reconciled that ID. Retry
// metadata is durable so a poison row cannot monopolize the head of the queue.
type CoordReportPublication struct {
	ReportID        string
	EventID         string
	State           string
	CreatedAt       time.Time
	PublishedAt     *time.Time
	Attempts        int
	NextAttemptAt   time.Time
	LastError       string
	QuarantinedAt   *time.Time
	QuarantineError string
}

// CoordAuditPublication retains everything needed to reconstruct its bounded
// timeline event. The projection intentionally has no foreign key to the
// mailbox or runs, so retirement cannot erase a pending or quarantined retry.
type CoordAuditPublication struct {
	MessageID       string
	EventID         string
	WorkspaceID     domain.WorkspaceID
	FromRun         domain.RunID
	ToRun           domain.RunID
	Body            string
	State           string
	CreatedAt       time.Time
	PublishedAt     *time.Time
	Attempts        int
	NextAttemptAt   time.Time
	LastError       string
	QuarantinedAt   *time.Time
	QuarantineError string
}

type CoordOutboxCursor struct {
	CreatedAt time.Time
	ID        string
}

const (
	CoordReportPublicationPending   = "pending"
	CoordReportPublicationPublished = "published"
	CoordAuditPublicationPending    = "pending"
	CoordAuditPublicationPublished  = "published"
)

// CoordOutboxCursorStore provides deterministic after-cursor traversal. The
// cursor is an optimization and may wrap to the beginning; retry metadata
// remains the durable fairness guarantee.
type CoordOutboxCursorStore interface {
	ListPendingCoordReportPublicationsAfter(context.Context, CoordOutboxCursor, int) ([]*CoordReportPublication, error)
	ListPendingCoordAuditPublicationsAfter(context.Context, CoordOutboxCursor, int) ([]*CoordAuditPublication, error)
}

// CoordOutboxRetryStore records failures without making callers depend on the
// database schema. Permanent failures are retained as quarantined rows.
type CoordOutboxRetryStore interface {
	RecordCoordReportPublicationFailure(context.Context, string, string, time.Time, bool) error
	RecordCoordAuditPublicationFailure(context.Context, string, string, time.Time, bool) error
}

// MessageStore is the run mailbox's persistence surface.
type MessageStore interface {
	// AppendRunMessage stores m, assigning its ID and CreatedAt when zero.
	// It fails ErrInboxFull when the target already holds maxUnacked
	// unacknowledged messages; retries with the same sender/key return the
	// original row without creating another message.
	AppendRunMessage(ctx context.Context, m *RunMessage, maxUnacked int) error
	// GetRunMessage returns one persisted message.
	GetRunMessage(ctx context.Context, id string) (*RunMessage, error)
	// GetRunMessageByIdempotency returns the sender's prior message for key,
	// regardless of kind, so callers can reject cross-kind reuse.
	GetRunMessageByIdempotency(ctx context.Context, from domain.RunID, key string) (*RunMessage, error)
	// GetQuestion returns a persisted question by its message ID.
	GetQuestion(ctx context.Context, id string) (*RunMessage, error)
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
	// DeleteRunMessages retires a released run's inbound mailbox.
	DeleteRunMessages(ctx context.Context, to domain.RunID) error
	AppendCoordReport(ctx context.Context, report *CoordReport) error
	ReserveCoordReport(ctx context.Context, report *CoordReport) (bool, error)
	FinalizeCoordReport(ctx context.Context, report *CoordReport) (bool, error)
	GetCoordReport(ctx context.Context, id string) (*CoordReport, error)
	GetCoordReportByIdempotency(ctx context.Context, run domain.RunID, key string) (*CoordReport, error)
	GetCoordReportPublication(ctx context.Context, id string) (*CoordReportPublication, error)
	ListPendingCoordReportPublications(ctx context.Context, limit int) ([]*CoordReportPublication, error)
	MarkCoordReportPublished(ctx context.Context, id, eventID string) error
}

// CoordAuditStore is the optional durable audit outbox seam. The SQLite DB
// implements it; narrow compatibility stores may omit it and therefore do
// not claim durable coordination-message projections.
type CoordAuditStore interface {
	GetCoordAuditPublication(context.Context, string) (*CoordAuditPublication, error)
	ListPendingCoordAuditPublications(context.Context, int) ([]*CoordAuditPublication, error)
	MarkCoordAuditPublished(context.Context, string, string) error
}

var _ CoordAuditStore = (*DB)(nil)

const runMessageCols = `id, workspace_id, from_run, to_run, body, kind, correlation_id, idempotency_key, delivery_token, created_at, delivered_at, acked_at`
const coordReportCols = `id, workspace_id, run_id, outcome, summary, next_action, evidence_refs, input_evidence_refs, idempotency_key, state, created_at, finalized_at, published_at`

func (d *DB) AppendRunMessage(ctx context.Context, m *RunMessage, maxUnacked int) error {
	_, err := d.AppendRunMessageWithPeer(ctx, m, maxUnacked, 0, false)
	return err
}

// AppendRunMessageWithPeer atomically persists a message and, for a new
// send/question, its sender-to-recipient peer relationship. The relationship
// insert is rolled back with an inbox-cap failure, so failed enqueues never
// spend a peer slot.
func (d *DB) AppendRunMessageWithPeer(ctx context.Context, m *RunMessage, maxUnacked, maxPeers int, openPeer bool) (bool, error) {
	if m == nil || m.WorkspaceID == "" || m.FromRun == "" || m.ToRun == "" {
		return false, errors.New("store: append run message: workspace_id, from_run, and to_run are required")
	}
	if len([]byte(m.Body)) > 4<<10 {
		return false, errors.New("store: append run message: body is too long")
	}
	if maxUnacked <= 0 {
		return false, errors.New("store: append run message: maxUnacked must be positive")
	}
	if openPeer && maxPeers <= 0 {
		return false, errors.New("store: append run message: maxPeers must be positive")
	}
	if m.Kind == "" {
		m.Kind = RunMessageKindMessage
	}
	if !m.Kind.Valid() {
		return false, fmt.Errorf("store: append run message: invalid kind %q", m.Kind)
	}
	id, ts, err := prepareCreate(m.CreatedAt)
	if err != nil {
		return false, err
	}
	correlation := m.CorrelationID
	if m.Kind == RunMessageKindQuestion && correlation == "" {
		correlation = id
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return false, fmt.Errorf("store: append run message: %w", err)
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: append run message: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Force the SQLite writer lock before any idempotency or peer reads.
	// Otherwise two deferred transactions can both read a snapshot and then
	// fail upgrading it with SQLITE_BUSY_SNAPSHOT instead of converging.
	if _, lerr := tx.ExecContext(ctx,
		`UPDATE run_messages SET id = id WHERE 0`,
	); lerr != nil {
		return false, fmt.Errorf("store: append run message: lock: %w", lerr)
	}
	// Read the idempotency row in the same write transaction as the eventual
	// insert. A key is shared across kinds so its reuse cannot change the
	// typed operation a retry appears to have performed.
	if m.IdempotencyKey != "" {
		prior, qerr := scanRunMessage(tx.QueryRowContext(ctx,
			`SELECT `+runMessageCols+` FROM run_messages WHERE from_run = ? AND idempotency_key = ?`,
			m.FromRun, m.IdempotencyKey))
		if qerr == nil {
			if !coordMessageEquivalent(prior, m) {
				return false, fmt.Errorf("%w: %q was used with different message inputs",
					ErrIdempotencyConflict, m.IdempotencyKey)
			}
			*m = *prior
			if commitErr := tx.Commit(); commitErr != nil {
				return false, fmt.Errorf("store: append run message: commit retry: %w", commitErr)
			}
			return false, nil
		}
		if !errors.Is(qerr, sql.ErrNoRows) {
			return false, fmt.Errorf("store: append run message: idempotency lookup: %w", qerr)
		}
	}

	if openPeer {
		var exists int
		if peerErr := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM coord_peers WHERE from_run = ? AND to_run = ?)`,
			m.FromRun, m.ToRun).Scan(&exists); peerErr != nil {
			return false, fmt.Errorf("store: append run message: peer lookup: %w", peerErr)
		}
		if exists == 0 {
			res, perr := tx.ExecContext(ctx,
				`INSERT INTO coord_peers (from_run, to_run, created_at)
				 SELECT ?, ?, ?
				 WHERE (SELECT COUNT(*) FROM coord_peers WHERE from_run = ?) < ?
				 ON CONFLICT DO NOTHING`,
				m.FromRun, m.ToRun, createdAt, m.FromRun, maxPeers)
			if perr != nil {
				return false, fmt.Errorf("store: append run message: open peer: %w", mapConstraint(perr, ErrNotFound))
			}
			inserted, rerr := res.RowsAffected()
			if rerr != nil {
				return false, fmt.Errorf("store: append run message: open peer: %w", rerr)
			}
			if inserted == 0 {
				return false, fmt.Errorf("store: append run message from %s: %w", m.FromRun, ErrCoordPeerLimit)
			}
		}
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO run_messages (`+runMessageCols+`)
		 SELECT ?, ?, ?, ?, ?, ?, ?, ?, '', ?, NULL, NULL
		 WHERE (SELECT COUNT(*) FROM run_messages WHERE to_run = ? AND acked_at IS NULL) < ?
		 ON CONFLICT DO NOTHING`,
		id, m.WorkspaceID, m.FromRun, m.ToRun, m.Body, m.Kind, correlation,
		m.IdempotencyKey, createdAt, m.ToRun, maxUnacked,
	)
	if err != nil {
		return false, fmt.Errorf("store: append run message: %w", mapConstraint(err, ErrNotFound))
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: append run message: %w", err)
	}
	if inserted == 0 {
		if m.IdempotencyKey != "" {
			prior, qerr := scanRunMessage(tx.QueryRowContext(ctx,
				`SELECT `+runMessageCols+` FROM run_messages WHERE from_run = ? AND idempotency_key = ?`,
				m.FromRun, m.IdempotencyKey))
			if qerr == nil {
				if !coordMessageEquivalent(prior, m) {
					return false, fmt.Errorf("%w: %q was used with different message inputs",
						ErrIdempotencyConflict, m.IdempotencyKey)
				}
				*m = *prior
				if err := tx.Commit(); err != nil {
					return false, fmt.Errorf("store: append run message: commit retry: %w", err)
				}
				return false, nil
			}
			if !errors.Is(qerr, sql.ErrNoRows) {
				return false, fmt.Errorf("store: append run message: idempotency lookup: %w", qerr)
			}
		}
		return false, fmt.Errorf("store: append run message to %s: %w", m.ToRun, ErrInboxFull)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO coord_audit_publications
			(message_id, event_id, workspace_id, from_run, to_run, body,
			 publication_state, created_at, published_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		id, CoordAuditEventID(id), m.WorkspaceID, m.FromRun, m.ToRun, m.Body,
		CoordAuditPublicationPending, createdAt,
	); err != nil {
		return false, fmt.Errorf("store: append run message: audit outbox: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: append run message: commit: %w", err)
	}
	m.ID, m.CreatedAt = id, ts
	m.CorrelationID = correlation
	m.DeliveryToken, m.DeliveredAt, m.AckedAt = "", nil, nil
	return true, nil
}
func coordMessageEquivalent(prior, incoming *RunMessage) bool {
	if prior == nil || incoming == nil ||
		prior.WorkspaceID != incoming.WorkspaceID ||
		prior.ToRun != incoming.ToRun ||
		prior.Kind != incoming.Kind ||
		prior.Body != incoming.Body {
		return false
	}
	if incoming.CorrelationID != "" {
		return prior.CorrelationID == incoming.CorrelationID
	}
	if incoming.Kind == RunMessageKindQuestion {
		return prior.CorrelationID == prior.ID
	}
	return prior.CorrelationID == ""
}

// CoordAuditEventID returns the stable event ID used for message audit rows.
func CoordAuditEventID(messageID string) string {
	return "coord-message:" + messageID
}

func (d *DB) GetRunMessage(ctx context.Context, id string) (*RunMessage, error) {
	if id == "" {
		return nil, errors.New("store: get run message: id is required")
	}
	row := d.db.QueryRowContext(ctx, `SELECT `+runMessageCols+` FROM run_messages WHERE id = ?`, id)
	m, err := scanRunMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get run message %s: %w", id, err)
	}
	return m, nil
}

func (d *DB) GetRunMessageByIdempotency(ctx context.Context, from domain.RunID, key string) (*RunMessage, error) {
	if from == "" || key == "" {
		return nil, ErrNotFound
	}
	row := d.db.QueryRowContext(ctx,
		`SELECT `+runMessageCols+` FROM run_messages WHERE from_run = ? AND idempotency_key = ?`,
		from, key)
	m, err := scanRunMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get run message idempotency %s: %w", key, err)
	}
	return m, nil
}

func (d *DB) GetQuestion(ctx context.Context, id string) (*RunMessage, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	row := d.db.QueryRowContext(ctx,
		`SELECT `+runMessageCols+` FROM run_messages WHERE id = ? AND kind = ?`,
		id, RunMessageKindQuestion)
	m, err := scanRunMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get question %s: %w", id, err)
	}
	return m, nil
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

	// Take the write lock before reading. The event log shares this database
	// and can otherwise make SQLite reject a read-to-write lock upgrade.
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
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete run messages for %s: begin: %w", to, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// Published rows are only reconciliation cache. Pending and quarantined
	// rows retain their immutable snapshots for later publication/inspection.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM coord_audit_publications
		 WHERE publication_state = ?
		   AND message_id IN (SELECT id FROM run_messages WHERE to_run = ?)`,
		CoordAuditPublicationPublished, to); err != nil {
		return fmt.Errorf("store: delete run messages for %s: clean audit outbox: %w", to, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_messages WHERE to_run = ?`, to); err != nil {
		return fmt.Errorf("store: delete run messages for %s: %w", to, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete run messages for %s: commit: %w", to, err)
	}
	return nil
}

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
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.FromRun, &m.ToRun, &m.Body,
		&m.Kind, &m.CorrelationID, &m.IdempotencyKey, &m.DeliveryToken,
		&createdAt, &deliveredAt, &ackedAt); err != nil {
		return nil, err
	}
	m.CreatedAt = decodeTime(createdAt)
	m.DeliveredAt = decodeTimePtr(deliveredAt)
	m.AckedAt = decodeTimePtr(ackedAt)
	return &m, nil
}

// AppendCoordReport is retained for callers that already have a completed
// evidence set. It uses the same durable reservation/finalization path as
// coord.report, so even legacy callers cannot create a second report for a
// run.
func (d *DB) AppendCoordReport(ctx context.Context, report *CoordReport) error {
	if err := validateCoordReport(report); err != nil {
		return err
	}
	_, err := d.ReserveCoordReport(ctx, report)
	if err != nil {
		return err
	}
	if report.State == CoordReportPending {
		_, err = d.FinalizeCoordReport(ctx, report)
	}
	return err
}

const (
	maxCoordReportSummaryBytes     = 4 << 10
	maxCoordReportNextActionBytes  = 512
	maxCoordReportEvidenceRefs     = 32
	maxCoordReportEvidenceRefBytes = 512
)

func validateCoordReport(report *CoordReport) error {
	if report == nil || report.WorkspaceID == "" || report.RunID == "" ||
		report.Outcome == "" || report.Summary == "" || report.IdempotencyKey == "" {
		return errors.New("store: coord report: workspace_id, run_id, outcome, and idempotency_key are required")
	}
	if !report.Outcome.Valid() {
		return fmt.Errorf("store: coord report: invalid outcome %q", report.Outcome)
	}
	if len([]byte(report.Summary)) > maxCoordReportSummaryBytes ||
		len([]byte(report.NextAction)) > maxCoordReportNextActionBytes {
		return errors.New("store: coord report: summary or next action is too long")
	}
	if err := validateCoordReportRefs(report.EvidenceRefs); err != nil {
		return err
	}
	if report.InputEvidenceRefs != nil {
		if err := validateCoordReportRefs(report.InputEvidenceRefs); err != nil {
			return fmt.Errorf("store: coord report input evidence refs: %w", err)
		}
	}
	return nil
}

func validateCoordReportRefs(refs []string) error {
	if len(refs) > maxCoordReportEvidenceRefs {
		return errors.New("store: coord report: too many evidence references")
	}
	for _, ref := range refs {
		if ref == "" || len([]byte(ref)) > maxCoordReportEvidenceRefBytes ||
			strings.ContainsAny(ref, "\x00\r\n\t") ||
			strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, `\`) {
			return errors.New("store: coord report: invalid evidence reference")
		}
	}
	return nil
}

// ReserveCoordReport durably claims a run's one report slot before any
// evidence capture. A retry with the same key returns the persisted pending
// or finalized row; another key receives ErrCoordReportConflict.
func (d *DB) ReserveCoordReport(ctx context.Context, report *CoordReport) (bool, error) {
	if err := validateCoordReport(report); err != nil {
		return false, err
	}
	refs, err := marshalCollaborationJSON(report.EvidenceRefs, "[]")
	if err != nil {
		return false, fmt.Errorf("store: reserve coord report evidence refs: %w", err)
	}
	inputRefs := report.InputEvidenceRefs
	if inputRefs == nil {
		inputRefs = report.EvidenceRefs
	}
	inputRefs = append([]string(nil), inputRefs...)
	inputRefsJSON, err := marshalCollaborationJSON(inputRefs, "[]")
	if err != nil {
		return false, fmt.Errorf("store: reserve coord report input evidence refs: %w", err)
	}
	report.InputEvidenceRefs = inputRefs
	id, ts, err := prepareCreate(report.CreatedAt)
	if err != nil {
		return false, err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return false, fmt.Errorf("store: reserve coord report: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: reserve coord report: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx, `INSERT INTO coord_reports (`+coordReportCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)
		ON CONFLICT (run_id) DO NOTHING`,
		id, report.WorkspaceID, report.RunID, report.Outcome, report.Summary,
		report.NextAction, refs, inputRefsJSON, report.IdempotencyKey, CoordReportPending, createdAt)
	if err != nil {
		return false, fmt.Errorf("store: reserve coord report: %w", mapConstraint(err, ErrNotFound))
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: reserve coord report: %w", err)
	}
	if inserted == 0 {
		prior, qerr := scanCoordReport(tx.QueryRowContext(ctx,
			`SELECT `+coordReportCols+` FROM coord_reports WHERE run_id = ?`, report.RunID))
		if errors.Is(qerr, sql.ErrNoRows) {
			return false, fmt.Errorf("store: reserve coord report: duplicate reservation")
		}
		if qerr != nil {
			return false, fmt.Errorf("store: reserve coord report: read existing: %w", qerr)
		}
		if prior.IdempotencyKey != report.IdempotencyKey {
			return false, fmt.Errorf("%w: run %s", ErrCoordReportConflict, report.RunID)
		}
		if prior.Outcome != report.Outcome || prior.Summary != report.Summary ||
			prior.NextAction != report.NextAction ||
			!equalStringSlices(prior.InputEvidenceRefs, report.InputEvidenceRefs) {
			return false, fmt.Errorf("%w: run %s", ErrCoordReportIdempotencyConflict, report.RunID)
		}
		*report = *prior
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("store: reserve coord report: commit retry: %w", err)
		}
		return false, nil
	}
	report.ID = id
	report.CreatedAt = ts
	report.State = CoordReportPending
	report.FinalizedAt, report.PublishedAt = nil, nil
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: reserve coord report: commit: %w", err)
	}
	return true, nil
}
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// CoordReportEventID returns the stable event ID used by the report outbox.
func CoordReportEventID(reportID string) string {
	return "coord-report:" + reportID
}

// FinalizeCoordReport atomically turns a pending reservation into the one
// externally accepted report. It returns true only for the transition that
// won the finalization race.
func (d *DB) FinalizeCoordReport(ctx context.Context, report *CoordReport) (bool, error) {
	if err := validateCoordReport(report); err != nil {
		return false, err
	}
	if report.ID == "" {
		return false, errors.New("store: finalize coord report: id is required")
	}
	refs, err := marshalCollaborationJSON(report.EvidenceRefs, "[]")
	if err != nil {
		return false, fmt.Errorf("store: finalize coord report evidence refs: %w", err)
	}
	ts := time.Now().UTC()
	finalizedAt, err := encodeTime(ts)
	if err != nil {
		return false, fmt.Errorf("store: finalize coord report: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: finalize coord report: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx,
		`UPDATE coord_reports
		 SET evidence_refs = ?, state = ?, finalized_at = ?, published_at = NULL
		 WHERE id = ? AND run_id = ? AND idempotency_key = ? AND state = ?`,
		refs, CoordReportFinalized, finalizedAt, report.ID, report.RunID,
		report.IdempotencyKey, CoordReportPending)
	if err != nil {
		return false, fmt.Errorf("store: finalize coord report: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: finalize coord report: %w", err)
	}
	if updated == 1 {
		eventID := CoordReportEventID(report.ID)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO coord_report_publications
			 (report_id, event_id, publication_state, created_at, published_at)
			 VALUES (?, ?, ?, ?, NULL)
			 ON CONFLICT (report_id) DO NOTHING`,
			report.ID, eventID, CoordReportPublicationPending, finalizedAt,
		); err != nil {
			return false, fmt.Errorf("store: finalize coord report: outbox: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("store: finalize coord report: commit: %w", err)
		}
		report.State = CoordReportFinalized
		report.FinalizedAt = &ts
		report.PublishedAt = nil
		return true, nil
	}
	_ = tx.Rollback()
	prior, qerr := d.GetCoordReportByIdempotency(ctx, report.RunID, report.IdempotencyKey)
	if errors.Is(qerr, ErrNotFound) {
		return false, ErrNotFound
	}
	if qerr != nil {
		return false, qerr
	}
	*report = *prior
	return false, nil
}

func (d *DB) GetCoordReportPublication(ctx context.Context, id string) (*CoordReportPublication, error) {
	if id == "" {
		return nil, errors.New("store: get coord report publication: id is required")
	}
	pub, err := scanCoordReportPublication(d.db.QueryRowContext(ctx,
		`SELECT report_id, event_id, publication_state, created_at, published_at,
		        attempts, next_attempt_at, last_error, quarantined_at, quarantine_error
		 FROM coord_report_publications WHERE report_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get coord report publication %s: %w", id, err)
	}
	return pub, nil
}

func (d *DB) ListPendingCoordReportPublications(ctx context.Context, limit int) ([]*CoordReportPublication, error) {
	return d.ListPendingCoordReportPublicationsAfter(ctx, CoordOutboxCursor{}, limit)
}

func (d *DB) ListPendingCoordReportPublicationsAfter(ctx context.Context, cursor CoordOutboxCursor, limit int) ([]*CoordReportPublication, error) {
	if limit <= 0 {
		return nil, errors.New("store: list coord report publications: limit must be positive")
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("store: list coord report publications: %w", err)
	}
	query := `SELECT report_id, event_id, publication_state, created_at, published_at,
	                 attempts, next_attempt_at, last_error, quarantined_at, quarantine_error
	          FROM coord_report_publications
	          WHERE publication_state = ? AND quarantined_at IS NULL
	            AND (next_attempt_at = 0 OR next_attempt_at <= ?)`
	args := []any{CoordReportPublicationPending, now}
	if !cursor.CreatedAt.IsZero() {
		created, cerr := encodeTime(cursor.CreatedAt)
		if cerr != nil {
			return nil, fmt.Errorf("store: list coord report publications: %w", cerr)
		}
		query += ` AND (created_at > ? OR (created_at = ? AND report_id > ?))`
		args = append(args, created, created, cursor.ID)
	}
	query += ` ORDER BY created_at, report_id LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list coord report publications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*CoordReportPublication
	for rows.Next() {
		pub, serr := scanCoordReportPublication(rows)
		if serr != nil {
			return nil, fmt.Errorf("store: list coord report publications: %w", serr)
		}
		out = append(out, pub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list coord report publications: %w", err)
	}
	return out, nil
}
func (d *DB) MarkCoordReportPublished(ctx context.Context, id, eventID string) error {
	if id == "" || eventID == "" {
		return errors.New("store: mark coord report published: report_id and event_id are required")
	}
	stamp, err := encodeTime(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store: mark coord report published: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: mark coord report published: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx,
		`UPDATE coord_report_publications
		 SET publication_state = ?, published_at = ?
		 WHERE report_id = ? AND event_id = ? AND publication_state = ?
		   AND quarantined_at IS NULL`,
		CoordReportPublicationPublished, stamp, id, eventID, CoordReportPublicationPending)
	if err != nil {
		return fmt.Errorf("store: mark coord report published: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: mark coord report published: %w", err)
	}
	if updated == 0 {
		pub, qerr := scanCoordReportPublication(tx.QueryRowContext(ctx,
			`SELECT report_id, event_id, publication_state, created_at, published_at,
			        attempts, next_attempt_at, last_error, quarantined_at, quarantine_error
			 FROM coord_report_publications WHERE report_id = ?`, id))
		if errors.Is(qerr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if qerr != nil {
			return fmt.Errorf("store: mark coord report published: read outbox: %w", qerr)
		}
		if pub.EventID != eventID {
			return ErrCoordReportPublicationConflict
		}
		if pub.State == CoordReportPublicationPublished {
			return nil
		}
		return errors.New("store: mark coord report published: publication was not pending")
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE coord_reports SET published_at = ? WHERE id = ? AND state = ?`,
		stamp, id, CoordReportFinalized); err != nil {
		return fmt.Errorf("store: mark coord report published: report: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: mark coord report published: commit: %w", err)
	}
	return nil
}
func (d *DB) RecordCoordReportPublicationFailure(ctx context.Context, id, lastError string, nextAttemptAt time.Time, quarantine bool) error {
	return d.recordCoordPublicationFailure(ctx, "coord_report_publications", "report_id", id, lastError, nextAttemptAt, quarantine)
}

func (d *DB) recordCoordPublicationFailure(ctx context.Context, table, key, id, lastError string, nextAttemptAt time.Time, quarantine bool) error {
	if id == "" {
		return errors.New("store: record coord publication failure: id is required")
	}
	if len(lastError) > 4096 {
		lastError = lastError[:4096]
	}
	next := int64(0)
	if !nextAttemptAt.IsZero() {
		var err error
		next, err = encodeTime(nextAttemptAt.UTC())
		if err != nil {
			return fmt.Errorf("store: record coord publication failure: %w", err)
		}
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store: record coord publication failure: %w", err)
	}
	query := `UPDATE ` + table + `
		SET attempts = attempts + 1, next_attempt_at = ?, last_error = ?`
	args := []any{next, lastError}
	if quarantine {
		query += `, quarantined_at = ?, quarantine_error = ?`
		args = append(args, now, lastError)
	}
	query += ` WHERE ` + key + ` = ? AND publication_state = ? AND quarantined_at IS NULL`
	args = append(args, id, CoordReportPublicationPending)
	res, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: record coord publication failure: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: record coord publication failure: %w", err)
	}
	if updated == 0 {
		pub, qerr := d.GetCoordReportPublication(ctx, id)
		if errors.Is(qerr, ErrNotFound) {
			return ErrNotFound
		}
		if qerr != nil {
			return qerr
		}
		if pub.State == CoordReportPublicationPublished || pub.QuarantinedAt != nil {
			return nil
		}
		return errors.New("store: record coord publication failure: publication was not pending")
	}
	return nil
}

func (d *DB) RecordCoordAuditPublicationFailure(ctx context.Context, id, lastError string, nextAttemptAt time.Time, quarantine bool) error {
	if id == "" {
		return errors.New("store: record coord audit publication failure: id is required")
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
	query += ` WHERE message_id = ? AND publication_state = ? AND quarantined_at IS NULL`
	args = append(args, id, CoordAuditPublicationPending)
	res, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: record coord audit publication failure: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: record coord audit publication failure: %w", err)
	}
	if updated == 0 {
		pub, qerr := d.GetCoordAuditPublication(ctx, id)
		if errors.Is(qerr, ErrNotFound) {
			return ErrNotFound
		}
		if qerr != nil {
			return qerr
		}
		if pub.State == CoordAuditPublicationPublished || pub.QuarantinedAt != nil {
			return nil
		}
		return errors.New("store: record coord audit publication failure: publication was not pending")
	}
	return nil
}

func (d *DB) GetCoordReport(ctx context.Context, id string) (*CoordReport, error) {
	if id == "" {
		return nil, errors.New("store: get coord report: id is required")
	}
	row := d.db.QueryRowContext(ctx, `SELECT `+coordReportCols+` FROM coord_reports WHERE id = ?`, id)
	report, err := scanCoordReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get coord report %s: %w", id, err)
	}
	return report, nil
}

func (d *DB) GetCoordReportByIdempotency(ctx context.Context, run domain.RunID, key string) (*CoordReport, error) {
	if run == "" || key == "" {
		return nil, ErrNotFound
	}
	row := d.db.QueryRowContext(ctx,
		`SELECT `+coordReportCols+` FROM coord_reports WHERE run_id = ? AND idempotency_key = ?`,
		run, key)
	report, err := scanCoordReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get coord report idempotency %s: %w", key, err)
	}
	return report, nil
}

func scanCoordReport(row interface{ Scan(...any) error }) (*CoordReport, error) {
	var (
		report      CoordReport
		refs        string
		inputRefs   string
		state       string
		createdAt   int64
		finalizedAt *int64
		publishedAt *int64
	)
	if err := row.Scan(&report.ID, &report.WorkspaceID, &report.RunID, &report.Outcome,
		&report.Summary, &report.NextAction, &refs, &inputRefs, &report.IdempotencyKey, &state,
		&createdAt, &finalizedAt, &publishedAt); err != nil {
		return nil, err
	}
	if refs == "" {
		refs = "[]"
	}
	if err := json.Unmarshal([]byte(refs), &report.EvidenceRefs); err != nil {
		return nil, fmt.Errorf("store: decode coord report evidence refs: %w", err)
	}
	if report.EvidenceRefs == nil {
		report.EvidenceRefs = []string{}
	}
	if inputRefs == "" {
		inputRefs = "[]"
	}
	if err := json.Unmarshal([]byte(inputRefs), &report.InputEvidenceRefs); err != nil {
		return nil, fmt.Errorf("store: decode coord report input evidence refs: %w", err)
	}
	if report.InputEvidenceRefs == nil {
		report.InputEvidenceRefs = []string{}
	}
	report.State = CoordReportState(state)
	report.CreatedAt = decodeTime(createdAt)
	report.FinalizedAt = decodeTimePtr(finalizedAt)
	report.PublishedAt = decodeTimePtr(publishedAt)
	return &report, nil
}

func scanCoordReportPublication(row interface{ Scan(...any) error }) (*CoordReportPublication, error) {
	var (
		pub           CoordReportPublication
		createdAt     int64
		publishedAt   *int64
		nextAttemptAt int64
		quarantinedAt *int64
	)
	if err := row.Scan(&pub.ReportID, &pub.EventID, &pub.State, &createdAt, &publishedAt,
		&pub.Attempts, &nextAttemptAt, &pub.LastError, &quarantinedAt, &pub.QuarantineError); err != nil {
		return nil, err
	}
	pub.CreatedAt = decodeTime(createdAt)
	pub.PublishedAt = decodeTimePtr(publishedAt)
	if nextAttemptAt != 0 {
		pub.NextAttemptAt = decodeTime(nextAttemptAt)
	}
	pub.QuarantinedAt = decodeTimePtr(quarantinedAt)
	return &pub, nil
}
func (d *DB) GetCoordAuditPublication(ctx context.Context, id string) (*CoordAuditPublication, error) {
	if id == "" {
		return nil, errors.New("store: get coord audit publication: id is required")
	}
	pub, err := scanCoordAuditPublication(d.db.QueryRowContext(ctx,
		`SELECT message_id, event_id, workspace_id, from_run, to_run, body,
		        publication_state, created_at, published_at,
		        attempts, next_attempt_at, last_error, quarantined_at, quarantine_error
		 FROM coord_audit_publications WHERE message_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get coord audit publication %s: %w", id, err)
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
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM coord_audit_publications
		 WHERE publication_state = ? AND NOT EXISTS
		       (SELECT 1 FROM run_messages WHERE run_messages.id = coord_audit_publications.message_id)`,
		CoordAuditPublicationPublished); err != nil {
		return nil, fmt.Errorf("store: clean coord audit publications: %w", err)
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("store: list coord audit publications: %w", err)
	}
	query := `SELECT message_id, event_id, workspace_id, from_run, to_run, body,
	                 publication_state, created_at, published_at,
	                 attempts, next_attempt_at, last_error, quarantined_at, quarantine_error
	          FROM coord_audit_publications
	          WHERE publication_state = ? AND quarantined_at IS NULL
	            AND (next_attempt_at = 0 OR next_attempt_at <= ?)`
	args := []any{CoordAuditPublicationPending, now}
	if !cursor.CreatedAt.IsZero() {
		created, cerr := encodeTime(cursor.CreatedAt)
		if cerr != nil {
			return nil, fmt.Errorf("store: list coord audit publications: %w", cerr)
		}
		query += ` AND (created_at > ? OR (created_at = ? AND message_id > ?))`
		args = append(args, created, created, cursor.ID)
	}
	query += ` ORDER BY created_at, message_id LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list coord audit publications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*CoordAuditPublication
	for rows.Next() {
		pub, serr := scanCoordAuditPublication(rows)
		if serr != nil {
			return nil, fmt.Errorf("store: list coord audit publications: %w", serr)
		}
		out = append(out, pub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list coord audit publications: %w", err)
	}
	return out, nil
}

func (d *DB) MarkCoordAuditPublished(ctx context.Context, id, eventID string) error {
	if id == "" || eventID == "" {
		return errors.New("store: mark coord audit published: message_id and event_id are required")
	}
	stamp, err := encodeTime(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store: mark coord audit published: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE coord_audit_publications
		 SET publication_state = ?, published_at = ?
		 WHERE message_id = ? AND event_id = ? AND publication_state = ?
		   AND quarantined_at IS NULL`,
		CoordAuditPublicationPublished, stamp, id, eventID, CoordAuditPublicationPending)
	if err != nil {
		return fmt.Errorf("store: mark coord audit published: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: mark coord audit published: %w", err)
	}
	if updated > 0 {
		return nil
	}
	pub, qerr := d.GetCoordAuditPublication(ctx, id)
	if errors.Is(qerr, ErrNotFound) {
		return ErrNotFound
	}
	if qerr != nil {
		return qerr
	}
	if pub.EventID != eventID {
		return ErrCoordAuditPublicationConflict
	}
	if pub.State == CoordAuditPublicationPublished {
		return nil
	}
	return errors.New("store: mark coord audit published: publication was not pending")
}
func scanCoordAuditPublication(row interface{ Scan(...any) error }) (*CoordAuditPublication, error) {
	var (
		pub           CoordAuditPublication
		createdAt     int64
		publishedAt   *int64
		nextAttemptAt int64
		quarantinedAt *int64
	)
	if err := row.Scan(&pub.MessageID, &pub.EventID, &pub.WorkspaceID, &pub.FromRun, &pub.ToRun, &pub.Body,
		&pub.State, &createdAt, &publishedAt, &pub.Attempts, &nextAttemptAt, &pub.LastError,
		&quarantinedAt, &pub.QuarantineError); err != nil {
		return nil, err
	}
	pub.CreatedAt = decodeTime(createdAt)
	pub.PublishedAt = decodeTimePtr(publishedAt)
	if nextAttemptAt != 0 {
		pub.NextAttemptAt = decodeTime(nextAttemptAt)
	}
	pub.QuarantinedAt = decodeTimePtr(quarantinedAt)
	return &pub, nil
}
