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

// ChildSettings is a member's child-run setting. Child runs are on until
// the member turns them off.
type ChildSettings struct {
	Enabled bool
}

// ChildCounts are one parent's reservations: busy children hold a slot,
// queued ones wait for one.
type ChildCounts struct {
	Busy, Queued int
}

// ChildStore persists child-run reservations and settings.
type ChildStore interface {
	// ReserveChild inserts c under the depth and total limits, or returns
	// the reservation already made under c's parent and idempotency key
	// with replay true.
	ReserveChild(ctx context.Context, c *domain.ChildReservation) (reserved *domain.ChildReservation, replay bool, err error)
	GetChild(ctx context.Context, run domain.RunID) (*domain.ChildReservation, error)
	// ListChildren returns run's children, oldest first.
	ListChildren(ctx context.Context, run domain.RunID) ([]*domain.ChildReservation, error)
	ChildCounts(ctx context.Context, parent domain.RunID) (ChildCounts, error)
	// ChildrenInState returns every reservation in state, oldest first.
	ChildrenInState(ctx context.Context, state domain.ChildState) ([]*domain.ChildReservation, error)
	// ChildrenToSettle returns started children whose run ended and whose
	// parent was not told, and attached children still queued or live
	// whose parent ended or was deleted, and completed children their
	// closed parent left unreviewed.
	ChildrenToSettle(ctx context.Context) ([]*domain.ChildReservation, error)
	// IdleChildren returns the attached started children of a live parent
	// whose live run has been quiet (domain.RunActivity.Quiet) since before
	// cutoff, with when that spell began.
	IdleChildren(ctx context.Context, cutoff time.Time) ([]IdleChild, error)
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
	// ChildReported reports whether a child's latest word is a success or
	// failure report it was not woken from since.
	ChildReported(ctx context.Context, run domain.RunID) (bool, error)
	// LatestReportOfAnyOutcome is run's newest report, terminal or not, or
	// ErrNotFound.
	LatestReportOfAnyOutcome(ctx context.Context, run domain.RunID) (LatestReport, error)
	// LatestCoordReport is run's newest finalized terminal report.
	LatestCoordReport(ctx context.Context, run domain.RunID) (*CoordReport, error)
	WorkspaceChildLimits(ctx context.Context, ws domain.WorkspaceID) (domain.ChildLimits, error)
	SetWorkspaceChildLimits(ctx context.Context, ws domain.WorkspaceID, limits domain.ChildLimits) error
}

// detachTransferred detaches a run handed to another member from its parent
// and from its children, so lineage never carries authority across owners.
const detachTransferred = `UPDATE run_children SET detached = 1 WHERE (run_id = ?1 OR parent_run_id = ?1) AND owner_member_id <> ?2`

const terminalStatuses = `'completed', 'merged', 'abandoned', 'failed', 'interrupted'`

const childCols = `rc.run_id, rc.parent_run_id, rc.root_run_id, rc.tree_key, rc.workspace_id,
	rc.owner_member_id, rc.account_member_id, rc.harness, rc.mode, rc.model, rc.purpose, rc.task,
	rc.base_commit, rc.base_branch, rc.fallback_disabled, rc.fallback_order, rc.idempotency_key, rc.request_hash, rc.state,
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
	if err := row.Scan(&c.RunID, &c.ParentRunID, &c.RootRunID, &c.TreeKey, &c.WorkspaceID,
		&c.OwnerID, &c.AccountID, &c.Harness, &c.Mode, &c.Model, &c.Purpose, &c.Task,
		&c.BaseCommit, &c.BaseBranch, &c.Fallback.Disabled, &order, &c.IdempotencyKey, &c.RequestHash, &c.State,
		&c.Detached, &c.Error, &c.EndNotified, &createdAt, &startedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(order), &c.Fallback.Order); err != nil {
		return nil, fmt.Errorf("store: decode subsession fallback order: %w", err)
	}
	c.CreatedAt = decodeTime(createdAt)
	c.StartedAt = decodeTimePtr(startedAt)
	return &c, nil
}

func (d *DB) ReserveChild(ctx context.Context, c *domain.ChildReservation) (*domain.ChildReservation, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("store: reserve subsession: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // committed below
	prior, err := scanChild(tx.QueryRowContext(ctx,
		`SELECT `+childCols+` FROM run_children rc WHERE rc.parent_run_id = ? AND rc.idempotency_key = ?`,
		c.ParentRunID, c.IdempotencyKey))
	if err == nil {
		return prior, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("store: reserve subsession: %w", err)
	}
	id, ts, err := prepareCreate("run", c.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	order, err := jsonText(c.Fallback.Order, "[]")
	if err != nil {
		return nil, false, fmt.Errorf("store: reserve subsession: %w", err)
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return nil, false, fmt.Errorf("store: reserve subsession: %w", err)
	}
	c.RunID, c.CreatedAt, c.State = domain.RunID(id), ts, domain.ChildQueued
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO run_children (run_id, parent_run_id, root_run_id, tree_key, workspace_id,
			owner_member_id, account_member_id, harness, mode, model, purpose, task, base_commit,
			base_branch, fallback_disabled, fallback_order, idempotency_key, request_hash, state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.RunID, c.ParentRunID, c.RootRunID, c.TreeKey, c.WorkspaceID, c.OwnerID, c.AccountID,
		c.Harness, c.Mode, c.Model, c.Purpose, c.Task, c.BaseCommit, c.BaseBranch,
		c.Fallback.Disabled, order, c.IdempotencyKey, c.RequestHash, c.State, createdAt); err != nil {
		return nil, false, fmt.Errorf("store: reserve subsession: %w", mapConstraint(err, ErrNotFound))
	}
	if err = tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("store: reserve subsession: %w", err)
	}
	return c, false, nil
}

func (d *DB) GetChild(ctx context.Context, run domain.RunID) (*domain.ChildReservation, error) {
	c, err := scanChild(d.db.QueryRowContext(ctx, `SELECT `+childCols+` FROM run_children rc WHERE rc.run_id = ?`, run))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get subsession: %w", err)
	}
	return c, nil
}

func (d *DB) ListChildren(ctx context.Context, run domain.RunID) ([]*domain.ChildReservation, error) {
	return d.listChildren(ctx, "list subsessions",
		`SELECT `+childCols+` FROM run_children rc WHERE rc.parent_run_id = ? ORDER BY rc.created_at, rc.run_id`, run)
}

func (d *DB) ChildrenInState(ctx context.Context, state domain.ChildState) ([]*domain.ChildReservation, error) {
	return d.listChildren(ctx, "subsessions in state",
		`SELECT `+childCols+` FROM run_children rc WHERE rc.state = ? ORDER BY rc.created_at, rc.run_id`, state)
}

func (d *DB) ChildrenToSettle(ctx context.Context) ([]*domain.ChildReservation, error) {
	return d.listChildren(ctx, "subsessions to settle",
		`SELECT `+childCols+` FROM run_children rc
		 LEFT JOIN runs child ON child.id = rc.run_id
		 LEFT JOIN runs parent ON parent.id = rc.parent_run_id
		 WHERE (rc.state = 'started' AND rc.end_notified = 0 AND child.status IN (`+terminalStatuses+`))
		    OR (rc.detached = 0 AND (parent.id IS NULL OR parent.status IN (`+terminalStatuses+`))
		        AND (rc.state = 'queued' OR (rc.state = 'started' AND child.status NOT IN (`+terminalStatuses+`))))
		    OR (rc.detached = 0 AND rc.state = 'started' AND child.status = 'completed' AND child.review <> 'accepted'
		        AND (parent.status = 'merged' OR (parent.status = 'abandoned' AND parent.reason <> 'killed')
		         OR (parent.status = 'completed' AND parent.review = 'accepted')))
		 ORDER BY rc.created_at`)
}

// IdleChild is a child run in an unbroken quiet spell.
type IdleChild struct {
	Child *domain.ChildReservation
	Run   *domain.Run
	Since time.Time
}

func (d *DB) IdleChildren(ctx context.Context, cutoff time.Time) ([]IdleChild, error) {
	live, args := liveStatusArgs()
	cands, err := d.listChildren(ctx, "idle children",
		`SELECT `+childCols+` FROM run_children rc
		 JOIN runs child ON child.id = rc.run_id
		 JOIN runs parent ON parent.id = rc.parent_run_id
		 WHERE rc.detached = 0 AND rc.state = 'started'
		   AND child.status IN (`+live+`) AND parent.status IN (`+live+`)
		 ORDER BY rc.created_at`, append(args, args...)...)
	if err != nil {
		return nil, err
	}
	var out []IdleChild
	for _, c := range cands {
		run, err := d.GetRun(ctx, c.RunID)
		if err != nil {
			return nil, err
		}
		if !run.Activity.Quiet(run.Reason) {
			continue
		}
		since, err := d.quietSince(ctx, c.RunID)
		if err != nil {
			return nil, err
		}
		if !since.IsZero() && !since.After(cutoff) {
			out = append(out, IdleChild{Child: c, Run: run, Since: since})
		}
	}
	return out, nil
}

// quietSince is when the run's current unbroken quiet spell began: the
// oldest of the newest transitions that are all quiet. A reason change
// inside the spell keeps it going. It is zero when the newest transition is
// not quiet.
func (d *DB) quietSince(ctx context.Context, run domain.RunID) (time.Time, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT activity, reason, at FROM run_state_transitions WHERE run_id = ? ORDER BY rowid DESC`, run)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: read run %s transitions: %w", run, err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var since int64
	for rows.Next() {
		var (
			activity domain.RunActivity
			reason   string
			at       int64
		)
		if err := rows.Scan(&activity, &reason, &at); err != nil {
			return time.Time{}, fmt.Errorf("store: read run %s transitions: %w", run, err)
		}
		if !activity.Quiet(reason) {
			break
		}
		since = at
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, fmt.Errorf("store: read run %s transitions: %w", run, err)
	}
	if since == 0 {
		return time.Time{}, nil
	}
	return decodeTime(since), nil
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

func (d *DB) ChildCounts(ctx context.Context, parent domain.RunID) (ChildCounts, error) {
	var c ChildCounts
	err := d.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(`+childBusy+`), 0), COALESCE(SUM(rc.state = 'queued'), 0)
		 FROM run_children rc WHERE rc.parent_run_id = ?`, parent).Scan(&c.Busy, &c.Queued)
	if err != nil {
		return ChildCounts{}, fmt.Errorf("store: subsession counts: %w", err)
	}
	return c, nil
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
			return fmt.Errorf("%w: subsession %s is not %v", ErrConflict, run, from)
		}
		return fmt.Errorf("store: set subsession state: %w", err)
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
		return fmt.Errorf("store: detach subsession: %w", err)
	}
	return err
}

func (d *DB) MarkChildEndNotified(ctx context.Context, run domain.RunID) (bool, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE run_children SET end_notified = 1 WHERE run_id = ? AND end_notified = 0`, run)
	if err != nil {
		return false, fmt.Errorf("store: mark subsession end notified: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (d *DB) UnfinishedChildren(ctx context.Context, parent domain.RunID, since time.Time) (int, int, error) {
	var after int64
	if !since.IsZero() {
		var err error
		if after, err = encodeTime(since); err != nil {
			return 0, 0, fmt.Errorf("store: unfinished subsessions: %w", err)
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
		return 0, 0, fmt.Errorf("store: unfinished subsessions: %w", err)
	}
	return unfinished, unread, nil
}

func (d *DB) ChildSettings(ctx context.Context, member domain.MemberID) (ChildSettings, error) {
	s := ChildSettings{Enabled: true}
	err := d.db.QueryRowContext(ctx, `SELECT enabled FROM child_settings WHERE member_id = ?`, member).Scan(&s.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return ChildSettings{}, fmt.Errorf("store: subsession settings: %w", err)
	}
	return s, nil
}

func (d *DB) PutChildSettings(ctx context.Context, member domain.MemberID, s ChildSettings) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO child_settings (member_id, enabled, updated_at) VALUES (?, ?, unixepoch())
		 ON CONFLICT(member_id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		member, s.Enabled)
	if err != nil {
		return fmt.Errorf("store: put subsession settings: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) SetRunChildren(ctx context.Context, run domain.RunID, value string, setBy domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET children = ?, children_set_by = ? WHERE id = ?`, value, setBy, run))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: set session subsessions: %w", err)
	}
	return err
}

func (d *DB) WorkspaceChildLimits(ctx context.Context, ws domain.WorkspaceID) (domain.ChildLimits, error) {
	var l domain.ChildLimits
	err := d.db.QueryRowContext(ctx,
		`SELECT child_max_live FROM workspaces WHERE id = ?`, ws).Scan(&l.MaxLive)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, fmt.Errorf("store: workspace subsession limits: %w", err)
	}
	return l, nil
}

func (d *DB) SetWorkspaceChildLimits(ctx context.Context, ws domain.WorkspaceID, l domain.ChildLimits) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE workspaces SET child_max_live = ? WHERE id = ?`, l.MaxLive, ws))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: set workspace subsession limits: %w", err)
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

func (d *DB) ChildReported(ctx context.Context, run domain.RunID) (bool, error) {
	var reported bool
	if err := d.db.QueryRowContext(ctx, `SELECT `+childReported+` FROM run_children rc WHERE rc.run_id = ?`, run).Scan(&reported); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("store: subsession reported: %w", err)
	}
	return reported, nil
}

// LatestReport is what a run last reported and when.
type LatestReport struct {
	Outcome   string
	Summary   string
	CreatedAt time.Time
}

func (d *DB) LatestReportOfAnyOutcome(ctx context.Context, run domain.RunID) (LatestReport, error) {
	var (
		r  LatestReport
		at int64
	)
	err := d.db.QueryRowContext(ctx,
		`SELECT outcome, summary, created_at FROM (
		   SELECT outcome, summary, created_at FROM coord_reports WHERE run_id = ?1 AND state = ?2 AND superseded_at IS NULL
		   UNION ALL
		   SELECT outcome, summary, created_at FROM coord_state_reports WHERE run_id = ?1 AND applied_at IS NOT NULL)
		 ORDER BY created_at DESC LIMIT 1`, run, CoordReportFinalized).Scan(&r.Outcome, &r.Summary, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, fmt.Errorf("store: latest report of any outcome: %w", err)
	}
	r.CreatedAt = decodeTime(at)
	return r, nil
}
