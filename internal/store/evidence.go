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

func (d *DB) CreateEvidencePacket(ctx context.Context, p *EvidencePacket) error {
	if p == nil || p.WorkspaceID == "" || p.RunID == "" || p.IdempotencyKey == "" {
		return errors.New("store: create evidence packet: workspace_id, run_id, and idempotency_key are required")
	}
	origin := p.Origin
	if !origin.Valid() {
		if p.CreatorID != "" {
			origin = EvidenceOrigin{Kind: EvidenceOriginHuman, ID: string(p.CreatorID)}
		} else {
			return errors.New("store: create evidence packet: valid origin is required")
		}
	}
	if origin.Kind == EvidenceOriginHuman {
		p.CreatorID = domain.MemberID(origin.ID)
	} else {
		p.CreatorID = ""
	}
	p.Origin = origin
	if p.PublicationOwner == "" {
		p.PublicationOwner = EvidencePublicationOwnerGeneric
	}
	if !p.PublicationOwner.Valid() {
		return fmt.Errorf("store: create evidence packet: invalid publication owner %q", p.PublicationOwner)
	}
	if p.Availability == "" {
		p.Availability = EvidenceAvailable
	}
	if p.Availability != EvidenceAvailable && p.Availability != EvidenceExpired {
		return fmt.Errorf("store: create evidence packet: invalid availability %q", p.Availability)
	}
	if !p.Trigger.Valid() {
		return fmt.Errorf("store: create evidence packet: invalid trigger %q", p.Trigger)
	}
	changed, err := marshalCollaborationJSON(p.ChangedFiles, "[]")
	if err != nil {
		return fmt.Errorf("store: create evidence packet changed files: %w", err)
	}
	sources, err := marshalCollaborationJSON(p.Sources, "[]")
	if err != nil {
		return fmt.Errorf("store: create evidence packet sources: %w", err)
	}
	related, err := marshalCollaborationJSON(p.RelatedRoomMessageIDs, "[]")
	if err != nil {
		return fmt.Errorf("store: create evidence packet related messages: %w", err)
	}
	unresolved, err := marshalCollaborationJSON(p.UnresolvedFacts, "[]")
	if err != nil {
		return fmt.Errorf("store: create evidence packet unresolved facts: %w", err)
	}
	id, created, err := prepareCreate(p.CreatedAt)
	if err != nil {
		return err
	}
	expiresAt, err := encodeTimePtr(p.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: create evidence packet expires at: %w", err)
	}
	expiredAt, err := encodeTimePtr(p.ExpiredAt)
	if err != nil {
		return fmt.Errorf("store: create evidence packet expired at: %w", err)
	}
	captured := p.CapturedAt
	if captured.IsZero() {
		captured = created
	}
	updated := p.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	capturedAt, err := encodeTime(captured)
	if err != nil {
		return fmt.Errorf("store: create evidence packet: %w", err)
	}
	createdAt, err := encodeTime(created)
	if err != nil {
		return fmt.Errorf("store: create evidence packet: %w", err)
	}
	updatedAt, err := encodeTime(updated)
	if err != nil {
		return fmt.Errorf("store: create evidence packet: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create evidence packet: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx, `INSERT INTO evidence_packets (`+evidencePacketCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (origin_kind, origin_id, run_id, idempotency_key) DO NOTHING`,
		id, p.WorkspaceID, p.RunID, origin.Kind, origin.ID, p.OwnerID, p.CreatorID, p.PublicationOwner,
		p.Trigger, p.Objective, capturedAt, expiresAt, p.Availability, expiredAt,
		p.EventBoundary, p.BaseRevision, p.RetainedRevision, changed, sources, related,
		unresolved, p.NextAction, p.Provenance, p.IdempotencyKey, createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("store: create evidence packet: %w", mapConstraint(err, ErrNotFound))
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: create evidence packet: %w", err)
	}
	packetID := id
	if inserted == 0 {
		stored, gerr := getEvidencePacketByOriginTx(ctx, tx, origin, p.RunID, p.IdempotencyKey)
		if gerr != nil {
			return fmt.Errorf("store: create evidence packet idempotency lookup: %w", gerr)
		}
		*p = *stored
		packetID = p.ID
	}
	if p.PublicationOwner == EvidencePublicationOwnerGeneric {
		if _, err := tx.ExecContext(ctx, `INSERT INTO evidence_publications
			(packet_id, event_id, attempts, next_attempt_at, last_error, created_at, updated_at)
			VALUES (?, ?, 0, ?, '', ?, ?)
			ON CONFLICT (packet_id) DO NOTHING`,
			packetID, "evidence:"+packetID, createdAt, createdAt, updatedAt); err != nil {
			return fmt.Errorf("store: create evidence publication: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM evidence_staging WHERE origin_kind = ? AND origin_id = ? AND run_id = ? AND idempotency_key = ?`,
		origin.Kind, origin.ID, p.RunID, p.IdempotencyKey); err != nil {
		return fmt.Errorf("store: clear evidence staging: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create evidence packet: commit: %w", err)
	}
	if inserted > 0 {
		p.ID, p.CapturedAt, p.CreatedAt, p.UpdatedAt = id, captured, created, updated
		p.ExpiresAt, p.ExpiredAt = decodeTimePtr(expiresAt), decodeTimePtr(expiredAt)
	}
	return nil
}

func (d *DB) GetEvidencePacketByIdempotency(ctx context.Context, creator domain.MemberID, run domain.RunID, key string) (*EvidencePacket, error) {
	return d.GetEvidencePacketByOrigin(ctx, EvidenceOrigin{Kind: EvidenceOriginHuman, ID: string(creator)}, run, key)
}

func (d *DB) GetEvidencePacketByOrigin(ctx context.Context, origin EvidenceOrigin, run domain.RunID, key string) (*EvidencePacket, error) {
	if !origin.Valid() || run == "" || key == "" {
		return nil, ErrNotFound
	}
	return getEvidencePacketByIdempotencyRow(d.db.QueryRowContext(ctx,
		`SELECT `+evidencePacketCols+` FROM evidence_packets WHERE origin_kind = ? AND origin_id = ? AND run_id = ? AND idempotency_key = ?`,
		origin.Kind, origin.ID, run, key))
}
func (d *DB) ListPendingEvidencePublications(ctx context.Context, now time.Time, before string, limit int) ([]*EvidencePublication, string, error) {
	nowN, err := encodeTime(now)
	if err != nil {
		return nil, "", fmt.Errorf("store: list evidence publications: %w", err)
	}
	limit = normalizeCollaborationLimit(limit)
	query := `SELECT packet_id, event_id, attempts, next_attempt_at, last_error, published_at, created_at, updated_at
		FROM evidence_publications WHERE published_at IS NULL AND next_attempt_at <= ?`
	args := []any{nowN}
	if before != "" {
		query += ` AND (created_at, packet_id) > (SELECT created_at, packet_id FROM evidence_publications WHERE packet_id = ?)`
		args = append(args, before)
	}
	query += ` ORDER BY created_at ASC, packet_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list evidence publications: %w", err)
	}
	items, err := collect(rows, scanEvidencePublication)
	if err != nil {
		return nil, "", fmt.Errorf("store: list evidence publications: %w", err)
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].PacketID
	}
	return items, next, nil
}

func (d *DB) MarkEvidencePublicationPublished(ctx context.Context, packetID string, publishedAt time.Time) error {
	n, err := encodeTime(publishedAt)
	if err != nil {
		return err
	}
	res, err := d.db.ExecContext(ctx, `UPDATE evidence_publications SET published_at = ?, updated_at = ? WHERE packet_id = ? AND published_at IS NULL`, n, n, packetID)
	if err != nil {
		return fmt.Errorf("store: mark evidence publication published: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		var exists int
		if err := d.db.QueryRowContext(ctx, `SELECT 1 FROM evidence_publications WHERE packet_id = ?`, packetID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
	}
	return nil
}

func (d *DB) MarkEvidencePublicationFailure(ctx context.Context, packetID string, attempts int, next time.Time, lastError string) error {
	nextN, err := encodeTime(next)
	if err != nil {
		return err
	}
	updatedN, err := encodeTime(time.Now().UTC())
	if err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE evidence_publications SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE packet_id = ? AND published_at IS NULL`, attempts, nextN, lastError, updatedN, packetID); err != nil {
		return fmt.Errorf("store: mark evidence publication failure: %w", err)
	}
	return nil
}

func getEvidencePacketByOriginTx(ctx context.Context, tx *sql.Tx, origin EvidenceOrigin, run domain.RunID, key string) (*EvidencePacket, error) {
	return getEvidencePacketByIdempotencyRow(tx.QueryRowContext(ctx,
		`SELECT `+evidencePacketCols+` FROM evidence_packets WHERE origin_kind = ? AND origin_id = ? AND run_id = ? AND idempotency_key = ?`,
		origin.Kind, origin.ID, run, key))
}

func getEvidencePacketByIdempotencyRow(row interface{ Scan(...any) error }) (*EvidencePacket, error) {
	p, err := scanEvidencePacket(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (d *DB) GetEvidencePacket(ctx context.Context, id string) (*EvidencePacket, error) {
	p, err := scanEvidencePacket(d.db.QueryRowContext(ctx, `SELECT `+evidencePacketCols+` FROM evidence_packets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get evidence packet: %w", err)
	}
	return p, nil
}

func (d *DB) ListEvidencePackets(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, before string, limit int) (*EvidencePacketPage, error) {
	if workspace == "" {
		return nil, errors.New("store: list evidence packets: workspace_id is required")
	}
	limit = normalizeCollaborationLimit(limit)
	query := `SELECT ` + evidencePacketCols + ` FROM evidence_packets WHERE workspace_id = ?`
	args := []any{workspace}
	if run != "" {
		query += ` AND run_id = ?`
		args = append(args, run)
	}
	if beforeAt, beforeID, ok := decodeEvidenceCursor(before); ok {
		query += ` AND (captured_at, id) < (?, ?)`
		args = append(args, beforeAt.UnixNano(), beforeID)
	} else if before != "" {
		// Accept legacy ID cursors during the rolling migration. New cursors
		// are always self-contained and do not rely on a live row.
		query += ` AND (captured_at, id) < (SELECT captured_at, id FROM evidence_packets WHERE id = ?)`
		args = append(args, before)
	}
	query += ` ORDER BY captured_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list evidence packets: %w", err)
	}
	items, err := collect(rows, scanEvidencePacket)
	if err != nil {
		return nil, err
	}
	page := &EvidencePacketPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextBefore = EncodeEvidenceCursor(page.Items[len(page.Items)-1].CapturedAt, page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

// ListExpiredEvidencePackets returns the oldest expired packets first. The
// expiry predicate deliberately excludes NULL values so non-retained packets
// are never selected for cleanup.
func (d *DB) ListExpiredEvidencePackets(ctx context.Context, expiredBefore time.Time, limit int) ([]*EvidencePacket, error) {
	expiredBeforeN, err := encodeTime(expiredBefore)
	if err != nil {
		return nil, fmt.Errorf("store: list expired evidence packets: %w", err)
	}
	limit = normalizeCollaborationLimit(limit)
	rows, err := d.db.QueryContext(ctx, `SELECT `+evidencePacketCols+`
		FROM evidence_packets
		WHERE availability = ? AND expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY expires_at ASC, id ASC
		LIMIT ?`, EvidenceAvailable, expiredBeforeN, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list expired evidence packets: %w", err)
	}
	items, err := collect(rows, scanEvidencePacket)
	if err != nil {
		return nil, fmt.Errorf("store: list expired evidence packets: %w", err)
	}
	return items, nil
}

func (d *DB) MarkEvidenceExpired(ctx context.Context, id string, expiredAt time.Time, sources []EvidenceSourceFact) error {
	if id == "" {
		return ErrNotFound
	}
	expiredAtN, err := encodeTime(expiredAt)
	if err != nil {
		return fmt.Errorf("store: mark evidence expired: %w", err)
	}
	sourcesJSON, err := marshalCollaborationJSON(sources, "[]")
	if err != nil {
		return fmt.Errorf("store: mark evidence expired sources: %w", err)
	}
	updatedAt, err := encodeTime(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store: mark evidence expired: %w", err)
	}
	res, err := d.db.ExecContext(ctx, `UPDATE evidence_packets
		SET availability = ?, expired_at = ?, retained_revision = '', base_revision = '',
			sources = ?, updated_at = ?
		WHERE id = ? AND availability = ?`,
		EvidenceExpired, expiredAtN, sourcesJSON, updatedAt, id, EvidenceAvailable)
	if err != nil {
		return fmt.Errorf("store: mark evidence expired: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: mark evidence expired: %w", err)
	}
	if n > 0 {
		return nil
	}
	p, getErr := d.GetEvidencePacket(ctx, id)
	if errors.Is(getErr, ErrNotFound) {
		return ErrNotFound
	}
	if getErr != nil {
		return getErr
	}
	if p != nil && p.Availability == EvidenceExpired {
		return nil
	}
	return ErrConflict
}

// DeleteEvidencePacket removes packet metadata after its retained artifacts
// have been removed. It is idempotent so interrupted cleanup can retry.
func (d *DB) DeleteEvidencePacket(ctx context.Context, id string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM evidence_packets WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete evidence packet: %w", err)
	}
	return nil
}

func (d *DB) PurgeExpiredEvidenceTombstones(ctx context.Context, before time.Time, limit int) (int, error) {
	beforeN, err := encodeTime(before)
	if err != nil {
		return 0, fmt.Errorf("store: purge evidence tombstones: %w", err)
	}
	limit = normalizeCollaborationLimit(limit)
	res, err := d.db.ExecContext(ctx, `DELETE FROM evidence_packets
		WHERE id IN (
			SELECT id FROM evidence_packets
			WHERE availability = ? AND expired_at IS NOT NULL AND expired_at <= ?
			ORDER BY expired_at ASC, id ASC LIMIT ?
		)`, EvidenceExpired, beforeN, limit)
	if err != nil {
		return 0, fmt.Errorf("store: purge evidence tombstones: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge evidence tombstones: %w", err)
	}
	return int(n), nil
}
func (d *DB) CreateEvidenceStaging(ctx context.Context, s *EvidenceStaging) error {
	if s == nil || s.ID == "" || s.WorkspaceID == "" || s.RunID == "" || s.IdempotencyKey == "" {
		return errors.New("store: create evidence staging: id, workspace_id, run_id, and idempotency_key are required")
	}
	origin := s.Origin
	if !origin.Valid() {
		if s.CreatorID != "" {
			origin = EvidenceOrigin{Kind: EvidenceOriginHuman, ID: string(s.CreatorID)}
		} else {
			return errors.New("store: create evidence staging: valid origin is required")
		}
	}
	if origin.Kind == EvidenceOriginHuman {
		s.CreatorID = domain.MemberID(origin.ID)
	} else {
		s.CreatorID = ""
	}
	s.Origin = origin
	created := s.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	createdAt, err := encodeTime(created)
	if err != nil {
		return fmt.Errorf("store: create evidence staging: %w", err)
	}
	expiresAt, err := encodeTime(s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: create evidence staging expiry: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO evidence_staging
		(id, workspace_id, run_id, origin_kind, origin_id, creator_id, idempotency_key, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		s.ID, s.WorkspaceID, s.RunID, origin.Kind, origin.ID, s.CreatorID, s.IdempotencyKey, expiresAt, createdAt); err != nil {
		return fmt.Errorf("store: create evidence staging: %w", mapConstraint(err, ErrNotFound))
	}
	s.CreatedAt, s.ExpiresAt = created, time.Unix(0, expiresAt).UTC()
	return nil
}

func (d *DB) ListEvidenceStaging(ctx context.Context, before time.Time, limit int) ([]*EvidenceStaging, error) {
	beforeAt, err := encodeTime(before)
	if err != nil {
		return nil, fmt.Errorf("store: list evidence staging: %w", err)
	}
	limit = normalizeCollaborationLimit(limit)
	rows, err := d.db.QueryContext(ctx, `SELECT id, workspace_id, run_id, origin_kind, origin_id, creator_id, idempotency_key, expires_at, created_at
		FROM evidence_staging WHERE created_at <= ? ORDER BY created_at ASC, id ASC LIMIT ?`, beforeAt, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list evidence staging: %w", err)
	}
	items, err := collect(rows, scanEvidenceStaging)
	if err != nil {
		return nil, fmt.Errorf("store: list evidence staging: %w", err)
	}
	return items, nil
}

func (d *DB) DeleteEvidenceStaging(ctx context.Context, id string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM evidence_staging WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete evidence staging: %w", err)
	}
	return nil
}

func scanEvidencePublication(row interface{ Scan(...any) error }) (*EvidencePublication, error) {
	var (
		p                             EvidencePublication
		nextAttempt, created, updated int64
		published                     *int64
	)
	if err := row.Scan(&p.PacketID, &p.EventID, &p.Attempts, &nextAttempt, &p.LastError, &published, &created, &updated); err != nil {
		return nil, err
	}
	p.NextAttemptAt, p.CreatedAt, p.UpdatedAt = decodeTime(nextAttempt), decodeTime(created), decodeTime(updated)
	p.PublishedAt = decodeTimePtr(published)
	return &p, nil
}
func scanEvidenceStaging(row interface{ Scan(...any) error }) (*EvidenceStaging, error) {
	var (
		s                    EvidenceStaging
		originKind           EvidenceOriginKind
		originID             string
		expiresAt, createdAt int64
	)
	if err := row.Scan(&s.ID, &s.WorkspaceID, &s.RunID, &originKind, &originID, &s.CreatorID,
		&s.IdempotencyKey, &expiresAt, &createdAt); err != nil {
		return nil, err
	}
	s.Origin = EvidenceOrigin{Kind: originKind, ID: originID}
	s.ExpiresAt, s.CreatedAt = decodeTime(expiresAt), decodeTime(createdAt)
	return &s, nil
}
func scanEvidencePacket(row interface{ Scan(...any) error }) (*EvidencePacket, error) {
	var (
		p                                               EvidencePacket
		originKind                                      EvidenceOriginKind
		originID, ownerID                               string
		eventBoundary, capturedAt, createdAt, updatedAt int64
		expiresAt, expiredAt                            *int64
		changed, sources, related, unresolved           sql.NullString
	)
	if err := row.Scan(&p.ID, &p.WorkspaceID, &p.RunID, &originKind, &originID, &ownerID, &p.CreatorID,
		&p.PublicationOwner, &p.Trigger, &p.Objective, &capturedAt, &expiresAt, &p.Availability, &expiredAt, &eventBoundary,
		&p.BaseRevision, &p.RetainedRevision, &changed, &sources, &related, &unresolved, &p.NextAction,
		&p.Provenance, &p.IdempotencyKey, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	p.Origin = EvidenceOrigin{Kind: originKind, ID: originID}
	p.OwnerID = domain.MemberID(ownerID)
	if p.PublicationOwner == "" {
		p.PublicationOwner = EvidencePublicationOwnerGeneric
	}
	if p.Availability == "" {
		p.Availability = EvidenceAvailable
	}
	p.EventBoundary = uint64(eventBoundary)
	for _, item := range []struct {
		raw sql.NullString
		dst any
	}{
		{changed, &p.ChangedFiles},
		{sources, &p.Sources},
		{related, &p.RelatedRoomMessageIDs},
		{unresolved, &p.UnresolvedFacts},
	} {
		if !item.raw.Valid || item.raw.String == "" {
			continue
		}
		if err := json.Unmarshal([]byte(item.raw.String), item.dst); err != nil {
			return nil, fmt.Errorf("store: decode evidence packet metadata: %w", err)
		}
	}
	p.ExpiresAt, p.ExpiredAt = decodeTimePtr(expiresAt), decodeTimePtr(expiredAt)
	p.CapturedAt, p.CreatedAt, p.UpdatedAt = decodeTime(capturedAt), decodeTime(createdAt), decodeTime(updatedAt)
	return &p, nil
}
