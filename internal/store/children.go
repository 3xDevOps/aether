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

// ErrChildLimit is a tree that reached its depth or total child limit.
var ErrChildLimit = errors.New("store: child run limit reached")

// ChildSettings is a member's child-run setting and the state of the
// suggestion to turn it on.
type ChildSettings struct {
	Enabled           bool
	Fallback          bool
	SharedWhenMissing bool
	Configured        bool
	Snoozes           int
	SnoozedUntil      time.Time
	NoNudge           bool
}

// ChildCounts are one tree's reservations: busy children hold a slot,
// queued ones wait for one, total is every reservation ever made.
type ChildCounts struct {
	Busy, Queued, Total int
}

// ChildStore persists child-run reservations and settings.
type ChildStore interface {
	// ReserveChild inserts c under the depth and total limits, or returns
	// the reservation already made under c's parent and idempotency key
	// with replay true.
	ReserveChild(ctx context.Context, c *domain.ChildReservation, limits domain.ChildLimits) (reserved *domain.ChildReservation, replay bool, err error)
	GetChild(ctx context.Context, run domain.RunID) (*domain.ChildReservation, error)
	// ListChildren returns run's descendants, nearest first.
	ListChildren(ctx context.Context, run domain.RunID, descendants bool) ([]*domain.ChildReservation, error)
	ChildCounts(ctx context.Context, tree string) (ChildCounts, error)
	// ChildrenInState returns every reservation in state, oldest first.
	ChildrenInState(ctx context.Context, state domain.ChildState) ([]*domain.ChildReservation, error)
	// ChildrenToSettle returns started children whose run ended and whose
	// parent was not told, and attached children still queued or live
	// whose parent ended or was deleted.
	ChildrenToSettle(ctx context.Context) ([]*domain.ChildReservation, error)
	// BusyChildren counts the parent's children that hold a slot.
	BusyChildren(ctx context.Context, parent domain.RunID) (int, error)
	// SetChildState moves a reservation from one of from to to.
	SetChildState(ctx context.Context, run domain.RunID, to domain.ChildState, errText string, from ...domain.ChildState) error
	DetachChild(ctx context.Context, run domain.RunID) error
	// MarkChildEndNotified reports whether this call set the flag.
	MarkChildEndNotified(ctx context.Context, run domain.RunID) (bool, error)
	// UnfinishedChildren counts parent's attached children still queued,
	// starting or not done, and their mail to parent unread since since.
	UnfinishedChildren(ctx context.Context, parent domain.RunID, since time.Time) (unfinished, unread int, err error)
	ChildSettings(ctx context.Context, member domain.MemberID) (ChildSettings, error)
	PutChildSettings(ctx context.Context, member domain.MemberID, settings ChildSettings) error
	SetRunChildren(ctx context.Context, run domain.RunID, value string, setBy domain.MemberID) error
	MarkChildRefused(ctx context.Context, run domain.RunID) error
	// LatestCoordReport is run's newest finalized terminal report.
	LatestCoordReport(ctx context.Context, run domain.RunID) (*CoordReport, error)
	WorkspaceChildLimits(ctx context.Context, ws domain.WorkspaceID) (domain.ChildLimits, error)
	SetWorkspaceChildLimits(ctx context.Context, ws domain.WorkspaceID, limits domain.ChildLimits) error
}

// detachTransferred detaches a run handed to another member from its parent
// and from its children, so lineage never carries authority across owners.
const detachTransferred = `UPDATE run_children SET detached = 1 WHERE (run_id = ?1 OR parent_run_id = ?1) AND owner_member_id <> ?2`

const terminalStatuses = `'completed', 'merged', 'abandoned', 'failed', 'interrupted'`

const childCols = `rc.run_id, rc.parent_run_id, rc.root_run_id, rc.tree_key, rc.mission_id, rc.workspace_id,
	rc.owner_member_id, rc.account_member_id, rc.harness, rc.mode, rc.model, rc.purpose, rc.task, rc.depth,
	rc.base_commit, rc.base_branch, rc.fallback_disabled, rc.fallback_order, rc.idempotency_key, rc.state,
	rc.detached, rc.error, rc.end_notified, rc.created_at, rc.started_at`

// childReported holds for a reservation whose run's latest word is a
// finalized success or failure report, before its run parks on it, and that
// was not woken from parked since.
const childReported = `EXISTS (SELECT 1 FROM coord_reports cr WHERE cr.run_id = rc.run_id AND cr.state = 'finalized'
	AND cr.superseded_at IS NULL AND cr.outcome IN ('success', 'failure')
	AND NOT EXISTS (SELECT 1 FROM run_state_transitions t WHERE t.run_id = rc.run_id
		AND t.from_status = 'needs-attention' AND t.to_status = 'running' AND t.at > cr.created_at))`

// childBusy holds for a reservation that occupies a slot: launching, or
// started and provisioning or working. A child that reported, waits on its
// own children or waits for a human holds none.
const childBusy = `(rc.state = 'launching' OR (rc.state = 'started' AND NOT ` + childReported + ` AND EXISTS (
	SELECT 1 FROM runs r WHERE r.id = rc.run_id AND (r.status IN ('queued', 'provisioning')
		OR (r.status IN ('running', 'needs-attention') AND r.activity IN ('', 'working', 'waiting-ci', 'waiting-tools', 'disconnected'))))))`

// childUnfinished holds for a reservation its parent still waits on.
const childUnfinished = `(rc.state IN ('queued', 'launching') OR (rc.state = 'started' AND NOT ` + childReported + ` AND EXISTS (
	SELECT 1 FROM runs r WHERE r.id = rc.run_id AND r.status IN ('queued', 'provisioning', 'running', 'needs-attention')
		AND r.activity NOT IN ('finished', 'failed', 'answered'))))`

func scanChild(row interface{ Scan(...any) error }) (*domain.ChildReservation, error) {
	var (
		c         domain.ChildReservation
		order     string
		createdAt int64
		startedAt *int64
	)
	if err := row.Scan(&c.RunID, &c.ParentRunID, &c.RootRunID, &c.TreeKey, &c.MissionID, &c.WorkspaceID,
		&c.OwnerID, &c.AccountID, &c.Harness, &c.Mode, &c.Model, &c.Purpose, &c.Task, &c.Depth,
		&c.BaseCommit, &c.BaseBranch, &c.Fallback.Disabled, &order, &c.IdempotencyKey, &c.State,
		&c.Detached, &c.Error, &c.EndNotified, &createdAt, &startedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(order), &c.Fallback.Order); err != nil {
		return nil, fmt.Errorf("store: decode child fallback order: %w", err)
	}
	c.CreatedAt = decodeTime(createdAt)
	c.StartedAt = decodeTimePtr(startedAt)
	return &c, nil
}

func (d *DB) ReserveChild(ctx context.Context, c *domain.ChildReservation, limits domain.ChildLimits) (*domain.ChildReservation, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("store: reserve child: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // committed below
	prior, err := scanChild(tx.QueryRowContext(ctx,
		`SELECT `+childCols+` FROM run_children rc WHERE rc.parent_run_id = ? AND rc.idempotency_key = ?`,
		c.ParentRunID, c.IdempotencyKey))
	if err == nil {
		return prior, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("store: reserve child: %w", err)
	}
	if c.Depth > limits.MaxDepth {
		return nil, false, fmt.Errorf("%w: a child of this run would be %d levels deep; this workspace allows %d", ErrChildLimit, c.Depth, limits.MaxDepth)
	}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_children WHERE tree_key = ?`, c.TreeKey).Scan(&total); err != nil {
		return nil, false, fmt.Errorf("store: reserve child: %w", err)
	}
	if total >= limits.MaxTotal {
		return nil, false, fmt.Errorf("%w: this tree already started %d child runs; this workspace allows %d", ErrChildLimit, total, limits.MaxTotal)
	}
	id, ts, err := prepareCreate("run", c.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	order, err := missionJSON(c.Fallback.Order, "[]")
	if err != nil {
		return nil, false, fmt.Errorf("store: reserve child: %w", err)
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return nil, false, fmt.Errorf("store: reserve child: %w", err)
	}
	c.RunID, c.CreatedAt, c.State = domain.RunID(id), ts, domain.ChildQueued
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO run_children (run_id, parent_run_id, root_run_id, tree_key, mission_id, workspace_id,
			owner_member_id, account_member_id, harness, mode, model, purpose, task, depth, base_commit,
			base_branch, fallback_disabled, fallback_order, idempotency_key, state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.RunID, c.ParentRunID, c.RootRunID, c.TreeKey, c.MissionID, c.WorkspaceID, c.OwnerID, c.AccountID,
		c.Harness, c.Mode, c.Model, c.Purpose, c.Task, c.Depth, c.BaseCommit, c.BaseBranch,
		c.Fallback.Disabled, order, c.IdempotencyKey, c.State, createdAt); err != nil {
		return nil, false, fmt.Errorf("store: reserve child: %w", mapConstraint(err, ErrNotFound))
	}
	if err = tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("store: reserve child: %w", err)
	}
	return c, false, nil
}

func (d *DB) GetChild(ctx context.Context, run domain.RunID) (*domain.ChildReservation, error) {
	c, err := scanChild(d.db.QueryRowContext(ctx, `SELECT `+childCols+` FROM run_children rc WHERE rc.run_id = ?`, run))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get child: %w", err)
	}
	return c, nil
}

func (d *DB) ListChildren(ctx context.Context, run domain.RunID, descendants bool) ([]*domain.ChildReservation, error) {
	query := `SELECT ` + childCols + ` FROM run_children rc WHERE rc.parent_run_id = ? ORDER BY rc.created_at, rc.run_id`
	if descendants {
		query = `WITH RECURSIVE tree(id) AS (
			SELECT run_id FROM run_children WHERE parent_run_id = ?
			UNION SELECT c.run_id FROM run_children c JOIN tree ON c.parent_run_id = tree.id)
		SELECT ` + childCols + ` FROM run_children rc JOIN tree ON tree.id = rc.run_id ORDER BY rc.depth, rc.created_at, rc.run_id`
	}
	return d.listChildren(ctx, "list children", query, run)
}

func (d *DB) ChildrenInState(ctx context.Context, state domain.ChildState) ([]*domain.ChildReservation, error) {
	return d.listChildren(ctx, "children in state",
		`SELECT `+childCols+` FROM run_children rc WHERE rc.state = ? ORDER BY rc.created_at, rc.run_id`, state)
}

func (d *DB) ChildrenToSettle(ctx context.Context) ([]*domain.ChildReservation, error) {
	return d.listChildren(ctx, "children to settle",
		`SELECT `+childCols+` FROM run_children rc
		 LEFT JOIN runs child ON child.id = rc.run_id
		 LEFT JOIN runs parent ON parent.id = rc.parent_run_id
		 WHERE (rc.state = 'started' AND rc.end_notified = 0 AND child.status IN (`+terminalStatuses+`))
		    OR (rc.detached = 0 AND (parent.id IS NULL OR parent.status IN (`+terminalStatuses+`))
		        AND (rc.state = 'queued' OR (rc.state = 'started' AND child.status NOT IN (`+terminalStatuses+`))))
		 ORDER BY rc.depth, rc.created_at`)
}

func (d *DB) listChildren(ctx context.Context, what, query string, args ...any) ([]*domain.ChildReservation, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []*domain.ChildReservation
	for rows.Next() {
		c, scanErr := scanChild(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: %s: %w", what, scanErr)
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}

func (d *DB) ChildCounts(ctx context.Context, tree string) (ChildCounts, error) {
	var c ChildCounts
	err := d.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(`+childBusy+`), 0), COALESCE(SUM(rc.state = 'queued'), 0), COUNT(*)
		 FROM run_children rc WHERE rc.tree_key = ?`, tree).Scan(&c.Busy, &c.Queued, &c.Total)
	if err != nil {
		return ChildCounts{}, fmt.Errorf("store: child counts: %w", err)
	}
	return c, nil
}

func (d *DB) BusyChildren(ctx context.Context, parent domain.RunID) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_children rc WHERE rc.parent_run_id = ? AND `+childBusy, parent).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: busy children: %w", err)
	}
	return n, nil
}

func (d *DB) SetChildState(ctx context.Context, run domain.RunID, to domain.ChildState, errText string, from ...domain.ChildState) error {
	query := `UPDATE run_children SET state = ?, error = ?,
		started_at = CASE WHEN ? = 'started' THEN ? ELSE started_at END
		WHERE run_id = ? AND state IN (?` + repeatPlaceholders(len(from)-1) + `)`
	args := []any{to, errText, to, time.Now().UTC().UnixNano(), run}
	for _, s := range from {
		args = append(args, s)
	}
	if err := notFoundOnZeroRows(d.db.ExecContext(ctx, query, args...)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: child %s is not %v", ErrConflict, run, from)
		}
		return fmt.Errorf("store: set child state: %w", err)
	}
	return nil
}

func repeatPlaceholders(n int) string {
	out := ""
	for range n {
		out += ", ?"
	}
	return out
}

func (d *DB) DetachChild(ctx context.Context, run domain.RunID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx, `UPDATE run_children SET detached = 1 WHERE run_id = ?`, run))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: detach child: %w", err)
	}
	return err
}

func (d *DB) MarkChildEndNotified(ctx context.Context, run domain.RunID) (bool, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE run_children SET end_notified = 1 WHERE run_id = ? AND end_notified = 0`, run)
	if err != nil {
		return false, fmt.Errorf("store: mark child end notified: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (d *DB) UnfinishedChildren(ctx context.Context, parent domain.RunID, since time.Time) (int, int, error) {
	var after int64
	if !since.IsZero() {
		var err error
		if after, err = encodeTime(since); err != nil {
			return 0, 0, fmt.Errorf("store: unfinished children: %w", err)
		}
	}
	var unfinished, unread int
	err := d.db.QueryRowContext(ctx,
		`SELECT
			(SELECT COUNT(*) FROM run_children rc WHERE rc.parent_run_id = ?1 AND rc.detached = 0 AND `+childUnfinished+`),
			(SELECT COUNT(*) FROM run_messages m JOIN run_children rc ON rc.run_id = m.from_run
			 WHERE m.to_run = ?1 AND rc.parent_run_id = ?1 AND m.created_at > ?2 AND m.acked_at IS NULL AND m.retired_at IS NULL)`,
		parent, after).Scan(&unfinished, &unread)
	if err != nil {
		return 0, 0, fmt.Errorf("store: unfinished children: %w", err)
	}
	return unfinished, unread, nil
}

func (d *DB) ChildSettings(ctx context.Context, member domain.MemberID) (ChildSettings, error) {
	s := ChildSettings{Fallback: true}
	var until *int64
	err := d.db.QueryRowContext(ctx,
		`SELECT enabled, fallback, shared_missing, configured, nudge_snoozes, nudge_until, nudge_off
		 FROM child_settings WHERE member_id = ?`, member).
		Scan(&s.Enabled, &s.Fallback, &s.SharedWhenMissing, &s.Configured, &s.Snoozes, &until, &s.NoNudge)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return ChildSettings{}, fmt.Errorf("store: child settings: %w", err)
	}
	if until != nil {
		s.SnoozedUntil = time.Unix(*until, 0).UTC()
	}
	return s, nil
}

func (d *DB) PutChildSettings(ctx context.Context, member domain.MemberID, s ChildSettings) error {
	var until *int64
	if !s.SnoozedUntil.IsZero() {
		u := s.SnoozedUntil.Unix()
		until = &u
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO child_settings (member_id, enabled, fallback, shared_missing, configured, nudge_snoozes, nudge_until, nudge_off, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, unixepoch())
		 ON CONFLICT(member_id) DO UPDATE SET enabled = excluded.enabled, fallback = excluded.fallback,
		   shared_missing = excluded.shared_missing, configured = excluded.configured,
		   nudge_snoozes = excluded.nudge_snoozes, nudge_until = excluded.nudge_until,
		   nudge_off = excluded.nudge_off, updated_at = excluded.updated_at`,
		member, s.Enabled, s.Fallback, s.SharedWhenMissing, s.Configured, s.Snoozes, until, s.NoNudge)
	if err != nil {
		return fmt.Errorf("store: put child settings: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) SetRunChildren(ctx context.Context, run domain.RunID, value string, setBy domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET children = ?, children_set_by = ? WHERE id = ?`, value, setBy, run))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: set run children: %w", err)
	}
	return err
}

func (d *DB) MarkChildRefused(ctx context.Context, run domain.RunID) error {
	if _, err := d.db.ExecContext(ctx, `UPDATE runs SET child_refused = 1 WHERE id = ?`, run); err != nil {
		return fmt.Errorf("store: mark child refused: %w", err)
	}
	return nil
}

func (d *DB) WorkspaceChildLimits(ctx context.Context, ws domain.WorkspaceID) (domain.ChildLimits, error) {
	var l domain.ChildLimits
	err := d.db.QueryRowContext(ctx,
		`SELECT child_max_live, child_max_depth, child_max_total FROM workspaces WHERE id = ?`, ws).
		Scan(&l.MaxLive, &l.MaxDepth, &l.MaxTotal)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, fmt.Errorf("store: workspace child limits: %w", err)
	}
	return l, nil
}

func (d *DB) SetWorkspaceChildLimits(ctx context.Context, ws domain.WorkspaceID, l domain.ChildLimits) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE workspaces SET child_max_live = ?, child_max_depth = ?, child_max_total = ? WHERE id = ?`,
		l.MaxLive, l.MaxDepth, l.MaxTotal, ws))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: set workspace child limits: %w", err)
	}
	return err
}

func (d *DB) LatestCoordReport(ctx context.Context, run domain.RunID) (*CoordReport, error) {
	report, err := scanCoordReport(d.db.QueryRowContext(ctx,
		`SELECT `+coordReportCols+` FROM coord_reports WHERE run_id = ? AND state = ? AND superseded_at IS NULL
		 ORDER BY created_at DESC, rowid DESC LIMIT 1`, run, CoordReportFinalized))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest coord report: %w", err)
	}
	return report, nil
}
