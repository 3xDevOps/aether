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
	HandoffEvidencePending      = "pending"
	HandoffEvidenceAvailable    = "available"
	HandoffEvidenceUnavailable  = "unavailable"
	HandoffPublicationPending   = "pending"
	HandoffPublicationPublished = "published"
	HandoffTimelinePending      = "pending"
	HandoffTimelinePublished    = "published"
	HandoffCoauthorPending      = "pending"
	HandoffCoauthorPublished    = "published"
)

// HandoffOutbox is the durable receipt created after ownership commits. The
// operation ID is generated for each transfer, not from its member pair, so
// A-B-A-B cycles cannot collapse into one record.
type HandoffOutbox struct {
	ID               string
	WorkspaceID      domain.WorkspaceID
	RunID            domain.RunID
	ActorID          domain.MemberID
	FromMemberID     domain.MemberID
	ToMemberID       domain.MemberID
	EvidencePacketID string
	EvidenceState    string
	TimelineState    string
	CoauthorState    string
	PublicationState string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	PublishedAt      *time.Time
}

// HandoffOutboxStore is intentionally separate from Store so narrow in-memory
// stores remain usable. The SQLite DB implements it when the v28 migration is
// present; production handoff requires the atomic transfer method below.
type HandoffOutboxStore interface {
	TransferRunWithHandoff(context.Context, *HandoffOutbox) error
	CreateHandoffOutbox(context.Context, *HandoffOutbox) error
	GetHandoffOutbox(context.Context, string) (*HandoffOutbox, error)
	ListPendingHandoffOutbox(context.Context, int) ([]*HandoffOutbox, error)
	SetHandoffEvidence(context.Context, string, string, string) error
	MarkHandoffTimeline(context.Context, string) error
	MarkHandoffCoauthor(context.Context, string) error
	MarkHandoffPublished(context.Context, string, time.Time) error
}

// HandoffOutboxPager provides a stable cursor for replay. It prevents old
// pending rows whose capture is permanently unavailable from starving newer
// transfers behind a single LIMIT query.
type HandoffOutboxPager interface {
	ListPendingHandoffOutboxPage(context.Context, string, int) ([]*HandoffOutbox, string, error)
}

func validateHandoffOutbox(h *HandoffOutbox) error {
	if h == nil || h.ID == "" || h.WorkspaceID == "" || h.RunID == "" ||
		h.ActorID == "" || h.FromMemberID == "" || h.ToMemberID == "" {
		return errors.New("store: handoff outbox requires operation, workspace, run, actor, from, and to")
	}
	if h.EvidenceState == "" {
		h.EvidenceState = HandoffEvidencePending
	}
	if h.EvidenceState != HandoffEvidencePending && h.EvidenceState != HandoffEvidenceAvailable && h.EvidenceState != HandoffEvidenceUnavailable {
		return fmt.Errorf("store: handoff outbox invalid evidence state %q", h.EvidenceState)
	}
	if h.TimelineState == "" {
		h.TimelineState = HandoffTimelinePending
	}
	if h.TimelineState != HandoffTimelinePending && h.TimelineState != HandoffTimelinePublished {
		return fmt.Errorf("store: handoff outbox invalid timeline state %q", h.TimelineState)
	}
	if h.CoauthorState == "" {
		h.CoauthorState = HandoffCoauthorPending
	}
	if h.CoauthorState != HandoffCoauthorPending && h.CoauthorState != HandoffCoauthorPublished {
		return fmt.Errorf("store: handoff outbox invalid coauthor state %q", h.CoauthorState)
	}
	if h.PublicationState == "" {
		h.PublicationState = HandoffPublicationPending
	}
	if h.PublicationState != HandoffPublicationPending && h.PublicationState != HandoffPublicationPublished {
		return fmt.Errorf("store: handoff outbox invalid publication state %q", h.PublicationState)
	}
	return nil
}

func (d *DB) CreateHandoffOutbox(ctx context.Context, h *HandoffOutbox) error {
	if err := validateHandoffOutbox(h); err != nil {
		return err
	}
	now := h.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	h.CreatedAt = now
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = now
	}
	created, err := encodeTime(h.CreatedAt)
	if err != nil {
		return err
	}
	updated, err := encodeTime(h.UpdatedAt)
	if err != nil {
		return err
	}
	published, err := encodeTimePtr(h.PublishedAt)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO handoff_outbox
		(id, workspace_id, run_id, actor_id, from_member_id, to_member_id,
		 evidence_packet_id, evidence_state, timeline_state, coauthor_state,
		 publication_state, created_at, updated_at, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		h.ID, h.WorkspaceID, h.RunID, h.ActorID, h.FromMemberID, h.ToMemberID,
		h.EvidencePacketID, h.EvidenceState, h.TimelineState, h.CoauthorState,
		h.PublicationState, created, updated, published)
	if err != nil {
		return fmt.Errorf("store: create handoff outbox: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

// TransferRunWithHandoff commits ownership and its pending publication
// receipt in one transaction. A successful return means recovery can always
// discover the transfer's operation identity.
func (d *DB) TransferRunWithHandoff(ctx context.Context, h *HandoffOutbox) error {
	if err := validateHandoffOutbox(h); err != nil {
		return err
	}
	now := h.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	h.CreatedAt = now
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = now
	}
	created, err := encodeTime(h.CreatedAt)
	if err != nil {
		return err
	}
	updated, err := encodeTime(h.UpdatedAt)
	if err != nil {
		return err
	}
	published, err := encodeTimePtr(h.PublishedAt)
	if err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin handoff transfer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE runs SET member_id = ? WHERE id = ? AND member_id = ?`,
		h.ToMemberID, h.RunID, h.FromMemberID)
	if err != nil {
		return fmt.Errorf("store: transfer run: %w", mapConstraint(err, ErrNotFound))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: transfer run rows: %w", err)
	}
	if affected == 0 {
		var owner domain.MemberID
		if queryErr := tx.QueryRowContext(ctx, `SELECT member_id FROM runs WHERE id = ?`, h.RunID).Scan(&owner); queryErr == nil && owner == h.ToMemberID {
			var existing HandoffOutbox
			var existingCreated, existingUpdated int64
			var existingPublished *int64
			queryErr = tx.QueryRowContext(ctx, `SELECT `+handoffOutboxCols+` FROM handoff_outbox WHERE id = ?`, h.ID).Scan(
				&existing.ID, &existing.WorkspaceID, &existing.RunID, &existing.ActorID,
				&existing.FromMemberID, &existing.ToMemberID, &existing.EvidencePacketID,
				&existing.EvidenceState, &existing.TimelineState, &existing.CoauthorState,
				&existing.PublicationState, &existingCreated, &existingUpdated, &existingPublished)
			if queryErr == nil && existing.RunID == h.RunID && existing.FromMemberID == h.FromMemberID &&
				existing.ToMemberID == h.ToMemberID {
				if commitErr := tx.Commit(); commitErr != nil {
					return fmt.Errorf("store: commit repeated handoff transfer: %w", commitErr)
				}
				return nil
			}
		}
		return ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO handoff_outbox
		(id, workspace_id, run_id, actor_id, from_member_id, to_member_id,
		 evidence_packet_id, evidence_state, timeline_state, coauthor_state,
		 publication_state, created_at, updated_at, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		h.ID, h.WorkspaceID, h.RunID, h.ActorID, h.FromMemberID, h.ToMemberID,
		h.EvidencePacketID, h.EvidenceState, h.TimelineState, h.CoauthorState,
		h.PublicationState, created, updated, published)
	if err != nil {
		return fmt.Errorf("store: create handoff outbox: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit handoff transfer: %w", err)
	}
	return nil
}

func scanHandoffOutbox(row interface{ Scan(...any) error }) (*HandoffOutbox, error) {
	var h HandoffOutbox
	var created, updated int64
	var published *int64
	if err := row.Scan(&h.ID, &h.WorkspaceID, &h.RunID, &h.ActorID, &h.FromMemberID, &h.ToMemberID,
		&h.EvidencePacketID, &h.EvidenceState, &h.TimelineState, &h.CoauthorState,
		&h.PublicationState, &created, &updated, &published); err != nil {
		return nil, err
	}
	h.CreatedAt = decodeTime(created)
	h.UpdatedAt = decodeTime(updated)
	h.PublishedAt = decodeTimePtr(published)
	return &h, nil
}

const handoffOutboxCols = `id, workspace_id, run_id, actor_id, from_member_id, to_member_id,
	evidence_packet_id, evidence_state, timeline_state, coauthor_state,
	publication_state, created_at, updated_at, published_at`

func (d *DB) GetHandoffOutbox(ctx context.Context, id string) (*HandoffOutbox, error) {
	h, err := scanHandoffOutbox(d.db.QueryRowContext(ctx, `SELECT `+handoffOutboxCols+` FROM handoff_outbox WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get handoff outbox: %w", err)
	}
	return h, nil
}

func (d *DB) ListPendingHandoffOutbox(ctx context.Context, limit int) ([]*HandoffOutbox, error) {
	items, _, err := d.ListPendingHandoffOutboxPage(ctx, "", limit)
	return items, err
}

func (d *DB) ListPendingHandoffOutboxPage(ctx context.Context, before string, limit int) ([]*HandoffOutbox, string, error) {
	if limit <= 0 || limit > MaxCollaborationPageSize {
		limit = MaxCollaborationPageSize
	}
	query := `SELECT ` + handoffOutboxCols + ` FROM handoff_outbox
		WHERE (timeline_state != ? OR coauthor_state != ? OR evidence_state = ? OR publication_state = ?)`
	args := []any{HandoffTimelinePublished, HandoffCoauthorPublished, HandoffEvidencePending, HandoffPublicationPending}
	if before != "" {
		query += ` AND (created_at, id) > (SELECT created_at, id FROM handoff_outbox WHERE id = ?)`
		args = append(args, before)
	}
	query += ` ORDER BY created_at, id LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list pending handoff outbox: %w", err)
	}
	items, err := collect(rows, scanHandoffOutbox)
	if err != nil {
		return nil, "", fmt.Errorf("store: list pending handoff outbox: %w", err)
	}
	next := ""
	if len(items) == limit && len(items) > 0 {
		next = items[len(items)-1].ID
	}
	return items, next, nil
}

func (d *DB) SetHandoffEvidence(ctx context.Context, id, packetID, state string) error {
	if state != HandoffEvidenceAvailable && state != HandoffEvidenceUnavailable {
		return fmt.Errorf("store: invalid handoff evidence state %q", state)
	}
	now := time.Now().UTC()
	updated, err := encodeTime(now)
	if err != nil {
		return err
	}
	result, err := d.db.ExecContext(ctx, `UPDATE handoff_outbox
		SET evidence_packet_id = ?, evidence_state = ?, updated_at = ?
		WHERE id = ? AND evidence_state = ?`, packetID, state, updated, id, HandoffEvidencePending)
	if err != nil {
		return fmt.Errorf("store: set handoff evidence: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set handoff evidence: %w", err)
	}
	if affected == 0 {
		h, getErr := d.GetHandoffOutbox(ctx, id)
		if getErr != nil {
			return fmt.Errorf("store: set handoff evidence: %w", getErr)
		}
		if h.EvidenceState != state || (state == HandoffEvidenceAvailable && h.EvidencePacketID != packetID) {
			return fmt.Errorf("store: handoff evidence already settled")
		}
	}
	return nil
}

func (d *DB) MarkHandoffTimeline(ctx context.Context, id string) error {
	return d.markHandoffPhase(ctx, id, "timeline_state", HandoffTimelinePending, HandoffTimelinePublished)
}

func (d *DB) MarkHandoffCoauthor(ctx context.Context, id string) error {
	return d.markHandoffPhase(ctx, id, "coauthor_state", HandoffCoauthorPending, HandoffCoauthorPublished)
}

func (d *DB) markHandoffPhase(ctx context.Context, id, column, pending, complete string) error {
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return err
	}
	result, err := d.db.ExecContext(ctx, `UPDATE handoff_outbox SET `+column+` = ?, updated_at = ? WHERE id = ? AND `+column+` = ?`,
		complete, now, id, pending)
	if err != nil {
		return fmt.Errorf("store: mark handoff %s: %w", column, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: mark handoff %s: %w", column, err)
	}
	if affected == 0 {
		h, getErr := d.GetHandoffOutbox(ctx, id)
		if getErr != nil {
			return fmt.Errorf("store: mark handoff %s: %w", column, getErr)
		}
		var state string
		switch column {
		case "timeline_state":
			state = h.TimelineState
		case "coauthor_state":
			state = h.CoauthorState
		}
		if state != complete {
			return fmt.Errorf("store: handoff %s is not pending", column)
		}
	}
	return nil
}

func (d *DB) MarkHandoffPublished(ctx context.Context, id string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	stamp, err := encodeTime(at)
	if err != nil {
		return err
	}
	result, err := d.db.ExecContext(ctx, `UPDATE handoff_outbox
		SET publication_state = ?, published_at = ?, updated_at = ?
		WHERE id = ? AND publication_state = ?`, HandoffPublicationPublished, stamp, stamp, id, HandoffPublicationPending)
	if err != nil {
		return fmt.Errorf("store: mark handoff publication: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: mark handoff publication: %w", err)
	}
	if affected == 0 {
		h, getErr := d.GetHandoffOutbox(ctx, id)
		if getErr != nil {
			return fmt.Errorf("store: mark handoff publication: %w", getErr)
		}
		if h.PublicationState != HandoffPublicationPublished {
			return fmt.Errorf("store: handoff publication is not pending")
		}
	}
	return nil
}
