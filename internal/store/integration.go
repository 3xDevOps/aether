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

// ErrIntegrationConflict reports an optimistic-concurrency failure while
// updating or deleting a candidate aggregate. It is distinct from ErrConflict,
// which denotes a uniqueness violation during creation.
var ErrIntegrationConflict = errors.New("store: integration candidate version conflict")

// IntegrationCandidate is the indexed envelope around one durable candidate
// aggregate. Payload contains the protocol.Candidate JSON. ActorKey,
// IdempotencyKey, and Digest are immutable bindings for prepare retries; an
// aggregate update never rewrites them.
type IntegrationCandidate struct {
	ID             string
	WorkspaceID    domain.WorkspaceID
	ActorKey       string
	IdempotencyKey string
	Digest         string
	State          string
	Version        int64
	Payload        json.RawMessage
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

const integrationCandidateCols = `id, workspace_id, actor_key, idempotency_key,
	digest, state, version, payload, created_at, expires_at`

const maxIntegrationCandidatePayloadBytes = 16 << 20

func validateIntegrationCandidate(c *IntegrationCandidate, op string) error {
	if c == nil {
		return fmt.Errorf("store: %s integration candidate: record is nil", op)
	}
	if c.ID == "" || c.WorkspaceID == "" || c.ActorKey == "" ||
		c.IdempotencyKey == "" || c.Digest == "" || c.State == "" {
		return fmt.Errorf("store: %s integration candidate: id, workspace_id, actor_key, idempotency_key, digest, and state are required", op)
	}
	if len(c.IdempotencyKey) > 256 {
		return fmt.Errorf("store: %s integration candidate: idempotency key is too long", op)
	}
	if len(c.Payload) == 0 || len(c.Payload) > maxIntegrationCandidatePayloadBytes || !json.Valid(c.Payload) {
		return fmt.Errorf("store: %s integration candidate: payload is empty, too large, or not valid JSON", op)
	}
	if c.Version < 1 {
		return fmt.Errorf("store: %s integration candidate: version must be positive", op)
	}
	if c.CreatedAt.IsZero() || c.ExpiresAt.IsZero() {
		return fmt.Errorf("store: %s integration candidate: created_at and expires_at are required", op)
	}
	return nil
}

// IntegrationCandidateSummary is the bounded list projection of an aggregate.
// DeliveryRequest and DeliveryReceipt contain only their small JSON objects;
// the complete payload remains available through GetIntegrationCandidate/Show.
type IntegrationCandidateSummary struct {
	CandidateID            string
	WorkspaceID            domain.WorkspaceID
	State                  string
	CandidateRevision      string
	TargetRef              string
	ExpectedTargetRevision string
	DeliveryRequest        json.RawMessage
	DeliveryReceipt        json.RawMessage
	CreatedAt              time.Time
	ExpiresAt              time.Time
}

func scanIntegrationCandidate(row interface{ Scan(...any) error }) (*IntegrationCandidate, error) {
	var (
		c                     IntegrationCandidate
		workspace, actor, key string
		payload               []byte
		created, expires      int64
	)
	if err := row.Scan(&c.ID, &workspace, &actor, &key, &c.Digest, &c.State,
		&c.Version, &payload, &created, &expires); err != nil {
		return nil, err
	}
	c.WorkspaceID = domain.WorkspaceID(workspace)
	c.ActorKey = actor
	c.IdempotencyKey = key
	c.Payload = append(json.RawMessage(nil), payload...)
	c.CreatedAt = decodeTime(created)
	c.ExpiresAt = decodeTime(expires)
	return &c, nil
}

func scanIntegrationCandidateSummary(row interface{ Scan(...any) error }) (*IntegrationCandidateSummary, error) {
	var (
		s                 IntegrationCandidateSummary
		workspace, state  string
		revision, target  string
		expected          string
		delivery, receipt sql.NullString
		created, expires  int64
	)
	if err := row.Scan(&s.CandidateID, &workspace, &state, &revision, &target,
		&expected, &delivery, &receipt, &created, &expires); err != nil {
		return nil, err
	}
	s.WorkspaceID = domain.WorkspaceID(workspace)
	s.State = state
	s.CandidateRevision = revision
	s.TargetRef = target
	s.ExpectedTargetRevision = expected
	if delivery.Valid {
		s.DeliveryRequest = json.RawMessage(delivery.String)
	}
	if receipt.Valid {
		s.DeliveryReceipt = json.RawMessage(receipt.String)
	}
	s.CreatedAt = decodeTime(created)
	s.ExpiresAt = decodeTime(expires)
	return &s, nil
}

// CreateIntegrationCandidate inserts a candidate's immutable idempotency
// envelope and its initial aggregate payload. The caller supplies the ID so it
// can use the same identity in candidate-owned Git refs before later updates.
func (d *DB) CreateIntegrationCandidate(ctx context.Context, c *IntegrationCandidate) error {
	if c == nil {
		return errors.New("store: create integration candidate: record is nil")
	}
	if c.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		c.ID = id
	}
	if c.Version == 0 {
		c.Version = 1
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.ExpiresAt.IsZero() {
		return errors.New("store: create integration candidate: expires_at is required")
	}
	if err := validateIntegrationCandidate(c, "create"); err != nil {
		return err
	}
	created, err := encodeTime(c.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: create integration candidate: %w", err)
	}
	expires, err := encodeTime(c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: create integration candidate: %w", err)
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO integration_candidates
		(id, workspace_id, actor_key, idempotency_key, digest, state, version,
		 payload, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.ActorKey, c.IdempotencyKey, c.Digest, c.State,
		c.Version, []byte(c.Payload), created, expires)
	if err != nil {
		return fmt.Errorf("store: create integration candidate: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) GetIntegrationCandidate(ctx context.Context, id string) (*IntegrationCandidate, error) {
	c, err := scanIntegrationCandidate(d.db.QueryRowContext(ctx,
		`SELECT `+integrationCandidateCols+` FROM integration_candidates WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get integration candidate: %w", err)
	}
	return c, nil
}

func (d *DB) GetIntegrationCandidateByKey(ctx context.Context, workspace domain.WorkspaceID, actorKey, idempotencyKey string) (*IntegrationCandidate, error) {
	c, err := scanIntegrationCandidate(d.db.QueryRowContext(ctx,
		`SELECT `+integrationCandidateCols+` FROM integration_candidates
		 WHERE workspace_id = ? AND actor_key = ? AND idempotency_key = ?`,
		workspace, actorKey, idempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get integration candidate by key: %w", err)
	}
	return c, nil
}

// UpdateIntegrationCandidate performs a compare-and-swap on Version. Only
// mutable aggregate envelope fields are written; the idempotency binding and
// creation metadata remain exactly those established by Create.
func (d *DB) UpdateIntegrationCandidate(ctx context.Context, c *IntegrationCandidate, expectedVersion int64) error {
	if err := validateIntegrationCandidate(c, "update"); err != nil {
		return err
	}
	if expectedVersion < 1 || c.Version != expectedVersion+1 {
		return fmt.Errorf("store: update integration candidate: %w", ErrIntegrationConflict)
	}
	expires, err := encodeTime(c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: update integration candidate: %w", err)
	}
	res, err := d.db.ExecContext(ctx, `UPDATE integration_candidates
		SET state = ?, version = ?, payload = ?, expires_at = ?
		WHERE id = ? AND version = ?`,
		c.State, c.Version, []byte(c.Payload), expires, c.ID, expectedVersion)
	if err != nil {
		return fmt.Errorf("store: update integration candidate: %w", mapConstraint(err, ErrNotFound))
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update integration candidate: %w", err)
	}
	if rows != 0 {
		return nil
	}
	var exists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT 1 FROM integration_candidates WHERE id = ?`, c.ID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("store: update integration candidate lookup: %w", err)
	}
	return ErrIntegrationConflict
}

func (d *DB) ListIntegrationCandidates(ctx context.Context, workspace domain.WorkspaceID, limit int) ([]*IntegrationCandidateSummary, error) {
	if workspace == "" {
		return nil, errors.New("store: list integration candidates: workspace_id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	// Select only the scalar summary and the two small delivery objects. The
	// aggregate payload (inputs, verification output, mutations) is never
	// loaded for a list page.
	rows, err := d.db.QueryContext(ctx, `SELECT id, workspace_id, state,
		COALESCE(json_extract(payload, '$.candidate_revision'), ''),
		COALESCE(json_extract(payload, '$.target_ref'), ''),
		COALESCE(json_extract(payload, '$.expected_target_revision'), ''),
		json_extract(payload, '$.delivery_request'),
		json_extract(payload, '$.delivery_receipt'),
		created_at, expires_at
		FROM integration_candidates WHERE workspace_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, workspace, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list integration candidates: %w", err)
	}
	return collect(rows, scanIntegrationCandidateSummary)
}

func (d *DB) ListIntegrationCandidateIDs(ctx context.Context, workspace domain.WorkspaceID) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM integration_candidates WHERE workspace_id = ? ORDER BY id`, workspace)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListIntegrationCleanupCandidates returns bounded recovery/retention work.
// Running verification is found inside a frozen aggregate so a restart cannot
// strand a persisted runtime merely because its top-level state is frozen.
// The service owns active-skip and retry cursors; this method deliberately has
// no mutable cursor state and is safe to call after a process restart.
func (d *DB) ListIntegrationCleanupCandidates(ctx context.Context, now time.Time, limit int) ([]*IntegrationCandidate, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if limit <= 0 {
		limit = 100
	}
	nowNanos, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: list integration cleanup candidates: %w", err)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+integrationCandidateCols+`
		FROM integration_candidates
		WHERE (
			state IN ('preparing', 'deleting', 'expired')
			OR expires_at <= ?
			OR (state = 'frozen' AND EXISTS (
				SELECT 1 FROM json_each(integration_candidates.payload, '$.verifications')
				WHERE json_extract(json_each.value, '$.status') = 'running'
			))
		)
		ORDER BY id ASC LIMIT ?`, nowNanos, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list integration cleanup candidates: %w", err)
	}
	return collect(rows, scanIntegrationCandidate)
}

// ListIntegrationCleanupCandidatesAfter is the restart-safe keyset traversal
// used by the service's bounded cleanup loop. It intentionally keys on the
// immutable candidate ID instead of looking up a deleted cursor row: an active
// or permanently failing first page cannot starve later IDs.
func (d *DB) ListIntegrationCleanupCandidatesAfter(ctx context.Context, now time.Time, afterID string, limit int) ([]*IntegrationCandidate, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if limit <= 0 {
		limit = 100
	}
	nowNanos, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: list integration cleanup candidates: %w", err)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+integrationCandidateCols+`
		FROM integration_candidates
		WHERE (
			state IN ('preparing', 'deleting', 'expired')
			OR expires_at <= ?
			OR (state = 'frozen' AND EXISTS (
				SELECT 1 FROM json_each(integration_candidates.payload, '$.verifications')
				WHERE json_extract(json_each.value, '$.status') = 'running'
			))
		) AND id > ?
		ORDER BY id ASC LIMIT ?`, nowNanos, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list integration cleanup candidates after cursor: %w", err)
	}
	return collect(rows, scanIntegrationCandidate)
}

func (d *DB) DeleteIntegrationCandidate(ctx context.Context, id string, expectedVersion int64) error {
	if id == "" {
		return fmt.Errorf("store: delete integration candidate: id is required")
	}
	if expectedVersion < 1 {
		return ErrIntegrationConflict
	}
	res, err := d.db.ExecContext(ctx,
		`DELETE FROM integration_candidates WHERE id = ? AND version = ?`, id, expectedVersion)
	if err != nil {
		return fmt.Errorf("store: delete integration candidate: %w", mapConstraint(err, ErrInUse))
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete integration candidate: %w", err)
	}
	if rows != 0 {
		return nil
	}
	var exists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT 1 FROM integration_candidates WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("store: delete integration candidate lookup: %w", err)
	}
	return ErrIntegrationConflict
}
