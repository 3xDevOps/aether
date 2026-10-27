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

// RunWait is one condition a run registered with coord.report until: a
// watch its owner run owns. Watch.Failures, Error and Observed hold the
// consecutive failed reads, the last error and the last read; WaitState is
// the state the run is told about: registered until its report is
// applied. The conditions of one report share a Revision. HeadSHA is the
// commit a checks condition resolves on, Observation what it resolved on,
// and Detail text from GitHub or another agent. DeliveryNextAt is set while
// the wake is due to the run's inbox.
type RunWait struct {
	Watch
	Revision  int64
	ReportKey string
	Spec      string
	Kind      string
	TargetRun domain.RunID
	WaitState string
	Outcome   string
	Summary   string
	Cause     string
	// EndedBy is the member who cancelled the wait, if one did.
	EndedBy          domain.MemberID
	HeadSHA          string
	Observation      string
	Detail           string
	SourceCheckedAt  *time.Time
	ErrorAt          *time.Time
	ResolvedAt       *time.Time
	DeliveredAt      *time.Time
	DeliveryAttempts int
	DeliveryNextAt   *time.Time
	DeliveryError    string
}

// Wait states, as protocol.Wait* names them, and the state of a condition
// whose report is not applied yet.
const (
	waitRegistered = "registered"
	waitPending    = "pending"
	waitResolved   = "resolved"
	waitExpired    = "expired"
	waitError      = "error"
)

// Woke reports whether the wait's state wakes its run.
func (w *RunWait) Woke() bool {
	return w.WaitState == waitResolved || w.WaitState == waitExpired || w.WaitState == waitError
}

const runWaitColumns = watchColumns + `, wait_revision, wait_report_key, wait_spec, wait_kind, wait_target_run, wait_state,
	wait_outcome, wait_summary, wait_cause, wait_ended_by, wait_head_sha, wait_observation, wait_detail, wait_source_checked_at, wait_error_at, wait_resolved_at, wait_delivered_at,
	wait_delivery_attempts, wait_delivery_next_at, wait_delivery_error`

// trailingScan scans a row's trailing columns into extra after the ones its
// caller asks for.
type trailingScan struct {
	row   interface{ Scan(...any) error }
	extra []any
}

func (e trailingScan) Scan(dest ...any) error { return e.row.Scan(append(dest, e.extra...)...) }

func scanRunWait(row interface{ Scan(...any) error }) (*RunWait, error) {
	var (
		r                                         RunWait
		checked, errAt, resolved, delivered, next *int64
	)
	w, err := scanWatch(trailingScan{row: row, extra: []any{&r.Revision, &r.ReportKey, &r.Spec, &r.Kind, &r.TargetRun,
		&r.WaitState, &r.Outcome, &r.Summary, &r.Cause, &r.EndedBy, &r.HeadSHA, &r.Observation, &r.Detail, &checked, &errAt, &resolved, &delivered,
		&r.DeliveryAttempts, &next, &r.DeliveryError}})
	if err != nil {
		return nil, err
	}
	r.Watch = *w
	r.SourceCheckedAt, r.ErrorAt, r.ResolvedAt = decodeTimePtr(checked), decodeTimePtr(errAt), decodeTimePtr(resolved)
	r.DeliveredAt, r.DeliveryNextAt = decodeTimePtr(delivered), decodeTimePtr(next)
	return &r, nil
}

// RunWaitStore keeps the conditions runs wait on.
type RunWaitStore interface {
	// RecordWaitingReport stores a waiting report's receipt and, when it is
	// new, its conditions in one transaction, registered: nothing reads
	// them until MarkCoordStateReportApplied arms them, and releasing the
	// report deletes them. A retry of the same report loads its receipt and
	// records nothing; the same key with other inputs fails
	// ErrCoordReportIdempotencyConflict. A run that registered
	// MaxWaitReportsPerHour reports with conditions in the last hour fails
	// ErrRunWaitLimit.
	RecordWaitingReport(ctx context.Context, report *CoordStateReport, waits []*RunWait) (inserted bool, err error)
	GetRunWait(ctx context.Context, id string) (*RunWait, error)
	// RunWaitConditions is the run's latest revision of conditions.
	RunWaitConditions(ctx context.Context, run domain.RunID) ([]*RunWait, error)
	// RunWaitConditionsOf is RunWaitConditions of each of runs, in one
	// read; a run with none has no entry.
	RunWaitConditionsOf(ctx context.Context, runs []domain.RunID) (map[domain.RunID][]*RunWait, error)
	// PurgeRegisteredRunWaits deletes the conditions of reports never
	// applied that were registered before cutoff.
	PurgeRegisteredRunWaits(ctx context.Context, cutoff time.Time) error
	// CommitRunWait writes w if w.Version is still stored, else returns
	// ErrConflict. A w that wakes its run ends the revision's other pending
	// conditions cancelled and records n with a pending run delivery, unless
	// another condition of the revision already woke it: then w ends
	// cancelled instead. ended lists the others it ended.
	CommitRunWait(ctx context.Context, w *RunWait, n *WatchNotification) (ended []*RunWait, err error)
	// EndRunWaits ends run's pending conditions, or only wait when it is
	// set, in state with cause, by the member who cancelled them if one
	// did, and drops their undelivered wakes.
	EndRunWaits(ctx context.Context, run domain.RunID, wait, state, cause string, by domain.MemberID, now time.Time) ([]*RunWait, error)
	// DueRunWaitDeliveries lists the waits whose wake is due by now.
	DueRunWaitDeliveries(ctx context.Context, now time.Time) ([]*RunWait, error)
	// SetRunWaitDelivery records one delivery attempt of w's wake: its
	// attempts, next attempt, delivered time and error, and the delivery
	// row d.
	SetRunWaitDelivery(ctx context.Context, w *RunWait, d WatchDelivery) error
}

// MaxWaitReportsPerHour bounds the waiting reports with conditions one run
// records in an hour.
const MaxWaitReportsPerHour = 30

// ErrRunWaitLimit refuses a run's waiting report with conditions past
// MaxWaitReportsPerHour.
var ErrRunWaitLimit = fmt.Errorf("store: this run recorded %d waiting reports with conditions in the last hour; wait on the conditions it has, or report waiting without them", MaxWaitReportsPerHour)

func (d *DB) RecordWaitingReport(ctx context.Context, report *CoordStateReport, waits []*RunWait) (bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: record waiting report: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err = getCoordStateReport(ctx, tx, report.RunID, report.IdempotencyKey); errors.Is(err, ErrNotFound) {
		since, encodeErr := encodeTime(report.CreatedAt.Add(-time.Hour))
		if encodeErr != nil {
			return false, fmt.Errorf("store: record waiting report: %w", encodeErr)
		}
		var recent int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM coord_state_reports
			WHERE run_id = ? AND until_conditions <> '' AND created_at > ?`, report.RunID, since).Scan(&recent); err != nil {
			return false, fmt.Errorf("store: record waiting report: %w", err)
		}
		if recent >= MaxWaitReportsPerHour {
			return false, ErrRunWaitLimit
		}
	} else if err != nil {
		return false, err
	}
	inserted, err := recordCoordStateReport(ctx, tx, report)
	if err != nil || !inserted {
		return false, err
	}
	for _, w := range waits {
		w.OwnerRunID, w.ReportKey, w.WaitState, w.State = report.RunID, report.IdempotencyKey, waitRegistered, "paused"
		if err := insertRunWait(ctx, tx, w); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: record waiting report: %w", err)
	}
	return true, nil
}

// armRunWaits makes the conditions report key registered pending as the
// run's next revision, due at once, and supersedes the run's pending ones.
// Only the new revision and the latest that woke the run are kept.
func armRunWaits(ctx context.Context, tx *sql.Tx, run domain.RunID, key string, now time.Time) error {
	var registered int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM watches WHERE owner_run_id = ? AND wait_report_key = ? AND wait_state = 'registered'`,
		run, key).Scan(&registered); err != nil {
		return fmt.Errorf("store: arm waits: %w", err)
	}
	if registered == 0 {
		return nil
	}
	if _, err := endRunWaits(ctx, tx, run, "", waitSuperseded, "replaced by the report "+key, "", now); err != nil {
		return err
	}
	at, err := encodeTime(now)
	if err != nil {
		return fmt.Errorf("store: arm waits: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE watches SET state = 'active', wait_state = 'pending', next_check_at = ?1, updated_at = ?1,
		wait_revision = (SELECT COALESCE(MAX(wait_revision), 0) + 1 FROM watches WHERE owner_run_id = ?2), version = version + 1
		WHERE owner_run_id = ?2 AND wait_report_key = ?3 AND wait_state = 'registered'`, at, run, key); err != nil {
		return fmt.Errorf("store: arm waits: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM watches WHERE owner_run_id = ?1 AND wait_state <> 'registered'
		AND wait_revision <> (SELECT MAX(wait_revision) FROM watches WHERE owner_run_id = ?1)
		AND wait_revision <> COALESCE((SELECT MAX(wait_revision) FROM watches WHERE owner_run_id = ?1
		  AND wait_state IN ('resolved', 'expired', 'error')), 0)`, run); err != nil {
		return fmt.Errorf("store: prune waits: %w", err)
	}
	return nil
}

const waitSuperseded = "superseded"

func insertRunWait(ctx context.Context, tx *sql.Tx, w *RunWait) error {
	id, err := newID("wat")
	if err != nil {
		return err
	}
	args, err := watchArgs(&w.Watch)
	if err != nil {
		return fmt.Errorf("store: record wait: %w", err)
	}
	created, err := encodeTime(w.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: record wait: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO watches (id, member_id, workspace_id, target_kind, repo, pr_number, source, created_at,
		   target_title, conditions, label, state, error, failures, observed, fired, updated_at, expires_at,
		   last_check_at, next_check_at, ended_at,
		   owner_run_id, wait_revision, wait_report_key, wait_spec, wait_kind, wait_target_run, wait_state, wait_head_sha)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		append(append([]any{id, w.MemberID, w.WorkspaceID, w.TargetKind, w.Repo, w.Number, w.Source, created}, args...),
			w.OwnerRunID, w.Revision, w.ReportKey, w.Spec, w.Kind, w.TargetRun, w.WaitState, w.HeadSHA)...); err != nil {
		return fmt.Errorf("store: record wait: %w", mapConstraint(err, ErrNotFound))
	}
	w.ID, w.Version = id, 1
	return nil
}

func (d *DB) GetRunWait(ctx context.Context, id string) (*RunWait, error) {
	w, err := scanRunWait(d.db.QueryRowContext(ctx, `SELECT `+runWaitColumns+` FROM watches WHERE id = ? AND owner_run_id IS NOT NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: wait %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get wait: %w", err)
	}
	return w, nil
}

func (d *DB) RunWaitConditions(ctx context.Context, run domain.RunID) ([]*RunWait, error) {
	byRun, err := d.RunWaitConditionsOf(ctx, []domain.RunID{run})
	return byRun[run], err
}

func (d *DB) RunWaitConditionsOf(ctx context.Context, runs []domain.RunID) (map[domain.RunID][]*RunWait, error) {
	out := map[domain.RunID][]*RunWait{}
	if len(runs) == 0 {
		return out, nil
	}
	args := make([]any, len(runs))
	for i, r := range runs {
		args[i] = r
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+runWaitColumns+` FROM watches w
		WHERE owner_run_id IN (?`+strings.Repeat(", ?", len(runs)-1)+`) AND wait_state <> 'registered'
		  AND wait_revision = (SELECT MAX(wait_revision) FROM watches l WHERE l.owner_run_id = w.owner_run_id AND l.wait_state <> 'registered')
		ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list waits: %w", err)
	}
	waits, err := collect(rows, scanRunWait)
	if err != nil {
		return nil, err
	}
	for _, wait := range waits {
		out[wait.OwnerRunID] = append(out[wait.OwnerRunID], wait)
	}
	return out, nil
}

func (d *DB) PurgeRegisteredRunWaits(ctx context.Context, cutoff time.Time) error {
	at, err := encodeTime(cutoff)
	if err != nil {
		return fmt.Errorf("store: purge registered waits: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `DELETE FROM watches WHERE wait_state = 'registered' AND created_at < ?`, at); err != nil {
		return fmt.Errorf("store: purge registered waits: %w", err)
	}
	return nil
}

func (d *DB) CommitRunWait(ctx context.Context, w *RunWait, n *WatchNotification) ([]*RunWait, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: commit wait: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var ended []*RunWait
	if w.Woke() {
		var woken int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM watches WHERE owner_run_id = ? AND wait_revision = ? AND id <> ?
			AND wait_state IN ('resolved', 'expired', 'error')`, w.OwnerRunID, w.Revision, w.ID).Scan(&woken); err != nil {
			return nil, fmt.Errorf("store: commit wait: %w", err)
		}
		if woken > 0 {
			w.WaitState, w.Cause, w.DeliveryNextAt = "cancelled", "another condition of this wait already woke the run", nil
		} else {
			if ended, err = endRunWaitsWhere(ctx, tx, `owner_run_id = ? AND wait_revision = ? AND id <> ?`,
				[]any{w.OwnerRunID, w.Revision, w.ID}, "cancelled", w.Spec+" resolved first", "", w.UpdatedAt); err != nil {
				return nil, err
			}
			if n != nil {
				if _, err := insertWatchNotification(ctx, tx, w.ID, n); err != nil {
					return nil, err
				}
			}
		}
	}
	version := w.Version
	if err := updateRunWait(ctx, tx, w); err != nil {
		w.Version = version
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		w.Version = version
		return nil, fmt.Errorf("store: commit wait: %w", err)
	}
	return ended, nil
}

func updateRunWait(ctx context.Context, db execer, w *RunWait) error {
	args, err := watchArgs(&w.Watch)
	if err != nil {
		return fmt.Errorf("store: update wait: %w", err)
	}
	times := make([]any, 0, 5)
	for _, t := range []*time.Time{w.SourceCheckedAt, w.ErrorAt, w.ResolvedAt, w.DeliveredAt, w.DeliveryNextAt} {
		v, encodeErr := encodeTimePtr(t)
		if encodeErr != nil {
			return fmt.Errorf("store: update wait: %w", encodeErr)
		}
		times = append(times, v)
	}
	res, err := db.ExecContext(ctx, `UPDATE watches SET `+watchSet+`, wait_state = ?, wait_outcome = ?, wait_summary = ?,
		wait_cause = ?, wait_head_sha = ?, wait_observation = ?, wait_detail = ?, wait_source_checked_at = ?, wait_error_at = ?, wait_resolved_at = ?, wait_delivered_at = ?,
		wait_delivery_next_at = ?, wait_delivery_attempts = ?, wait_delivery_error = ?, version = version + 1
		WHERE id = ? AND version = ?`,
		append(append(args, w.WaitState, w.Outcome, w.Summary, w.Cause, w.HeadSHA, w.Observation, w.Detail), append(times, w.DeliveryAttempts, w.DeliveryError, w.ID, w.Version)...)...)
	if err != nil {
		return fmt.Errorf("store: update wait: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: update wait: %w", err)
	} else if n == 0 {
		return fmt.Errorf("store: update wait %s at version %d: %w", w.ID, w.Version, ErrConflict)
	}
	w.Version++
	return nil
}

func (d *DB) EndRunWaits(ctx context.Context, run domain.RunID, wait, state, cause string, by domain.MemberID, now time.Time) ([]*RunWait, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: end waits: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	ended, err := endRunWaits(ctx, tx, run, wait, state, cause, by, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: end waits: %w", err)
	}
	return ended, nil
}

func endRunWaits(ctx context.Context, tx *sql.Tx, run domain.RunID, wait, state, cause string, by domain.MemberID, now time.Time) ([]*RunWait, error) {
	where, args := `owner_run_id = ?`, []any{run}
	if wait != "" {
		where, args = where+` AND id = ?`, append(args, wait)
	}
	return endRunWaitsWhere(ctx, tx, where, args, state, cause, by, now)
}

// endRunWaitsWhere ends the pending waits that match where, and drops the
// undelivered wakes of the woken ones.
func endRunWaitsWhere(ctx context.Context, tx *sql.Tx, where string, args []any, state, cause string, by domain.MemberID, now time.Time) ([]*RunWait, error) {
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: end waits: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE watch_deliveries SET status = 'cleared', detail = ?, at = ?
		WHERE channel = 'run_inbox' AND status = 'pending' AND notification_id IN (
		  SELECT id FROM watch_notifications WHERE watch_id IN (
		    SELECT id FROM watches WHERE wait_delivery_next_at IS NOT NULL AND `+where+`))`,
		append([]any{"Not delivered: " + cause, at}, args...)...); err != nil {
		return nil, fmt.Errorf("store: end waits: drop wakes: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE watches SET wait_delivery_next_at = NULL, wait_delivery_error = ?, version = version + 1
		WHERE wait_delivery_next_at IS NOT NULL AND `+where, append([]any{"Not delivered: " + cause}, args...)...); err != nil {
		return nil, fmt.Errorf("store: end waits: drop wakes: %w", err)
	}
	finalState := "done"
	if state == waitExpired {
		finalState = "expired"
	}
	rows, err := tx.QueryContext(ctx, `UPDATE watches SET state = ?, wait_state = ?, wait_cause = ?, wait_ended_by = ?, ended_at = ?, updated_at = ?,
		next_check_at = NULL, version = version + 1
		WHERE wait_state = 'pending' AND `+where+` RETURNING `+runWaitColumns,
		append([]any{finalState, state, cause, by, at, at}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("store: end waits: %w", err)
	}
	return collect(rows, scanRunWait)
}

func (d *DB) DueRunWaitDeliveries(ctx context.Context, now time.Time) ([]*RunWait, error) {
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: due wait deliveries: %w", err)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+runWaitColumns+` FROM watches
		WHERE wait_delivery_next_at <= ? ORDER BY wait_delivery_next_at, id`, at)
	if err != nil {
		return nil, fmt.Errorf("store: due wait deliveries: %w", err)
	}
	return collect(rows, scanRunWait)
}

func (d *DB) SetRunWaitDelivery(ctx context.Context, w *RunWait, del WatchDelivery) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set wait delivery: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err = updateRunWait(ctx, tx, w); err != nil {
		return err
	}
	var notification string
	err = tx.QueryRowContext(ctx, `SELECT id FROM watch_notifications WHERE watch_id = ? ORDER BY created_at DESC LIMIT 1`, w.ID).Scan(&notification)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("store: set wait delivery: %w", err)
	default:
		if err := setWatchDelivery(ctx, tx, notification, del); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set wait delivery: %w", err)
	}
	return nil
}
