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

// Runs
// RunWorktreeStore exposes the narrow compare-and-clear used by checkout GC.
// It cannot overwrite ownership or another lifecycle update with a stale
// full run snapshot.
type RunWorktreeStore interface {
	ClearRunWorktree(context.Context, domain.RunID, string, domain.RunStatus) error
}

// ReservedRunStore creates a run with an ID reserved by durable orchestration
// state. The reserved ID closes the reservation/CreateRun crash gap; ordinary
// launches continue to use CreateRun's generated IDs.
type ReservedRunStore interface {
	CreateRunWithID(context.Context, *domain.Run) error
}

func validateRun(r *domain.Run, op string) error {
	if !r.Status.Valid() {
		return fmt.Errorf("store: %s run: invalid status %q", op, r.Status)
	}
	if !r.Mode.Valid() {
		return fmt.Errorf("store: %s run: invalid launch mode %q", op, r.Mode)
	}
	return nil
}
func (d *DB) CreateRun(ctx context.Context, r *domain.Run) error {
	return d.createRun(ctx, r, false)
}

// CreateRunWithID is the reserved-ID variant used by mission attempts. It
// accepts only a caller-supplied ID and never silently substitutes another
// identity.
func (d *DB) CreateRunWithID(ctx context.Context, r *domain.Run) error {
	if r == nil || r.ID == "" {
		return errors.New("store: create reserved run: id is required")
	}
	return d.createRun(ctx, r, true)
}

func (d *DB) createRun(ctx context.Context, r *domain.Run, reserved bool) error {
	if err := validateRun(r, "create"); err != nil {
		return err
	}
	r.AccountMemberID = r.AccountMember()
	var id string
	id = string(r.ID)
	var ts time.Time
	var err error
	if reserved {
		ts = r.CreatedAt
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
	} else {
		id, ts, err = prepareCreate(r.CreatedAt)
	}
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create run: %w", err)
	}
	startedAt, err := encodeTimePtr(r.StartedAt)
	if err != nil {
		return fmt.Errorf("store: create run: started at: %w", err)
	}
	finishedAt, err := encodeTimePtr(r.FinishedAt)
	if err != nil {
		return fmt.Errorf("store: create run: finished at: %w", err)
	}
	lastCommitAt, err := encodeOptionalTime(r.LastCommitAt)
	if err != nil {
		return fmt.Errorf("store: create run: %w", err)
	}
	baseCheckedAt, err := encodeOptionalTime(r.BaseCheckedAt)
	if err != nil {
		return fmt.Errorf("store: create run: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO runs (id, workspace_id, member_id, account_member_id, task, harness, mode, status,
		                   reason, branch, worktree, protected, created_at, started_at,
		                   finished_at, profile_snapshot_id, title, last_commit, last_commit_at,
		                   harness_session_id, base_commit, base_branch, base_source, base_checked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.WorkspaceID, r.MemberID, r.AccountMemberID, r.Task, r.Harness, r.Mode, r.Status,
		r.Reason, r.Branch, r.Worktree, r.Protected, createdAt, startedAt, finishedAt,
		r.ProfileSnapshotID, r.Title, r.LastCommit, lastCommitAt, r.HarnessSessionID,
		r.BaseCommit, r.BaseBranch, r.BaseSource, baseCheckedAt,
	); err != nil {
		return fmt.Errorf("store: create run: %w", mapConstraint(err, ErrNotFound))
	}
	r.ID, r.CreatedAt = domain.RunID(id), ts
	return nil
}

func scanRun(row interface{ Scan(...any) error }) (*domain.Run, error) {
	var (
		r                     domain.Run
		createdAt             int64
		startedAt, finishedAt *int64
		lastCommitAt          *int64
		baseCheckedAt         *int64
		archivedAt            *int64
	)
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.MemberID, &r.AccountMemberID, &r.Task, &r.Harness,
		&r.Mode, &r.Status, &r.Reason, &r.Branch, &r.Worktree, &r.Protected,
		&createdAt, &startedAt, &finishedAt, &r.ProfileSnapshotID, &r.Title,
		&r.LastCommit, &lastCommitAt, &r.HarnessSessionID, &r.BaseCommit, &r.BaseBranch,
		&r.BaseSource, &baseCheckedAt, &archivedAt, &r.UnansweredQuestions,
		&r.MissionID, &r.MissionRole, &r.IntegratorRunID); err != nil {
		return nil, err
	}
	r.CreatedAt = decodeTime(createdAt)
	r.StartedAt = decodeTimePtr(startedAt)
	r.FinishedAt = decodeTimePtr(finishedAt)
	if lastCommitAt != nil {
		r.LastCommitAt = decodeTime(*lastCommitAt)
	}
	if baseCheckedAt != nil {
		r.BaseCheckedAt = decodeTime(*baseCheckedAt)
	}
	r.ArchivedAt = decodeTimePtr(archivedAt)
	return &r, nil
}

const runCols = `runs.id, runs.workspace_id, runs.member_id, runs.account_member_id, runs.task, runs.harness, runs.mode, runs.status,
	runs.reason, runs.branch, runs.worktree, runs.protected, runs.created_at, runs.started_at, runs.finished_at, runs.profile_snapshot_id,
	runs.title, runs.last_commit, runs.last_commit_at, runs.harness_session_id, runs.base_commit, runs.base_branch, runs.base_source,
	runs.base_checked_at, runs.archived_at`

// runSnapshotQuery returns one grouped query for a run snapshot. Questions
// with a denied/cancelled state are not actionable, and a correlated reply
// removes its question from the count. Keeping this in the run read means
// list and single-run snapshots cannot disagree, without walking room history
// once per run.
func runSnapshotQuery(where string) string {
	return `SELECT ` + runCols + `, COUNT(question.id),
		COALESCE(integrator.id, worker_mission.id, ''),
		CASE WHEN integrator.id IS NOT NULL THEN 'integrator'
		     WHEN worker_mission.id IS NOT NULL THEN 'worker' ELSE '' END,
		COALESCE(integrator.current_integrator_run_id, worker_mission.current_integrator_run_id, '')
		FROM runs
		LEFT JOIN missions integrator ON integrator.current_integrator_run_id = runs.id
		LEFT JOIN mission_attempts attempt ON attempt.run_id = runs.id
		LEFT JOIN missions worker_mission ON worker_mission.id = attempt.mission_id
		LEFT JOIN room_messages question
			ON question.run_id = runs.id
			AND question.kind = 'question'
			AND question.state NOT IN ('denied', 'cancelled')
			AND NOT EXISTS (
				SELECT 1 FROM room_messages reply
				WHERE reply.run_id = question.run_id
				  AND reply.kind = 'reply'
				  AND reply.correlation_id = question.id
			)
		WHERE ` + where + `
		GROUP BY runs.id`
}

func (d *DB) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	r, err := scanRun(d.db.QueryRowContext(ctx,
		runSnapshotQuery(`runs.id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get run: %w", err)
	}
	return r, nil
}

func (d *DB) ListRunsByWorkspace(ctx context.Context, id domain.WorkspaceID) ([]*domain.Run, error) {
	rows, err := d.db.QueryContext(ctx,
		runSnapshotQuery(`runs.workspace_id = ?`)+` ORDER BY runs.id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list runs by workspace: %w", err)
	}
	return collect(rows, scanRun)
}

func (d *DB) ListRunsByMember(ctx context.Context, id domain.MemberID) ([]*domain.Run, error) {
	rows, err := d.db.QueryContext(ctx,
		runSnapshotQuery(`runs.member_id = ?`)+` ORDER BY runs.id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list runs by member: %w", err)
	}
	return collect(rows, scanRun)
}

// ListActiveRuns returns runs whose status is non-terminal. The status set
// is derived from domain.AllRunStatuses so it cannot drift from the enum.
func (d *DB) ListActiveRuns(ctx context.Context) ([]*domain.Run, error) {
	var (
		placeholders []string
		args         []any
	)
	for _, s := range domain.AllRunStatuses {
		if !s.Terminal() {
			placeholders = append(placeholders, "?")
			args = append(args, s)
		}
	}
	rows, err := d.db.QueryContext(ctx,
		runSnapshotQuery(`runs.status IN (`+
			strings.Join(placeholders, ", ")+
			`)`)+` ORDER BY runs.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list active runs: %w", err)
	}
	return collect(rows, scanRun)
}

// ListRunsArchivedBefore returns runs whose archived_at is set and at or
// before cutoff, for the retention sweep. It does not filter by status: a
// run restored or otherwise no longer eligible is caught by the sweep's
// own re-read. One query, no index: the sweep runs hourly and the table
// is small.
func (d *DB) ListRunsArchivedBefore(ctx context.Context, cutoff time.Time) ([]*domain.Run, error) {
	ts, err := encodeTime(cutoff)
	if err != nil {
		return nil, fmt.Errorf("store: list runs archived before: %w", err)
	}
	rows, err := d.db.QueryContext(ctx,
		runSnapshotQuery(`runs.archived_at IS NOT NULL AND runs.archived_at <= ?`)+` ORDER BY runs.id`, ts)
	if err != nil {
		return nil, fmt.Errorf("store: list runs archived before: %w", err)
	}
	return collect(rows, scanRun)
}

func (d *DB) UpdateRun(ctx context.Context, r *domain.Run) error {
	if err := validateRun(r, "update"); err != nil {
		return err
	}
	r.AccountMemberID = r.AccountMember()
	startedAt, err := encodeTimePtr(r.StartedAt)
	if err != nil {
		return fmt.Errorf("store: update run: started at: %w", err)
	}
	finishedAt, err := encodeTimePtr(r.FinishedAt)
	if err != nil {
		return fmt.Errorf("store: update run: finished at: %w", err)
	}
	lastCommitAt, err := encodeOptionalTime(r.LastCommitAt)
	if err != nil {
		return fmt.Errorf("store: update run: last commit at: %w", err)
	}
	baseCheckedAt, err := encodeOptionalTime(r.BaseCheckedAt)
	if err != nil {
		return fmt.Errorf("store: update run: base checked at: %w", err)
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET workspace_id = ?, member_id = ?, account_member_id = ?, task = ?, harness = ?,
		     mode = ?, status = ?, reason = ?, branch = ?, worktree = ?,
		     protected = ?, started_at = ?, finished_at = ?,
		     profile_snapshot_id = ?, title = ?, last_commit = ?, last_commit_at = ?,
		     harness_session_id = ?, base_commit = ?, base_branch = ?, base_source = ?,
		     base_checked_at = ?
		 WHERE id = ?`,
		r.WorkspaceID, r.MemberID, r.AccountMemberID, r.Task, r.Harness, r.Mode, r.Status,
		r.Reason, r.Branch, r.Worktree, r.Protected, startedAt, finishedAt,
		r.ProfileSnapshotID, r.Title, r.LastCommit, lastCommitAt, r.HarnessSessionID,
		r.BaseCommit, r.BaseBranch, r.BaseSource, baseCheckedAt, r.ID,
	))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update run: %w", mapConstraint(err, ErrNotFound))
	}
	return err
}

// ClearRunWorktree clears only a checkout path that still has the expected
// terminal status and value. A concurrent ownership transfer therefore
// survives even when GC started from an older run snapshot.
func (d *DB) ClearRunWorktree(ctx context.Context, id domain.RunID, expected string, status domain.RunStatus) error {
	if !status.Valid() {
		return fmt.Errorf("store: clear run worktree: invalid status %q", status)
	}
	result, err := d.db.ExecContext(ctx,
		`UPDATE runs SET worktree = ? WHERE id = ? AND worktree = ? AND status = ?`,
		"", id, expected, status)
	if err != nil {
		return fmt.Errorf("store: clear run worktree: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: clear run worktree: %w", err)
	}
	if affected > 0 {
		return nil
	}
	current, err := d.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if current.Worktree == "" {
		return nil
	}
	return ErrConflict
}

// UpdateRunCommit updates only the metadata for the latest published branch
// commit, so concurrent lifecycle writes cannot clobber it.
func (d *DB) UpdateRunCommit(ctx context.Context, id domain.RunID, commit string, at time.Time) error {
	lastCommitAt, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: update run commit: %w", err)
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET last_commit = ?, last_commit_at = ? WHERE id = ?`,
		commit, lastCommitAt, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: update run commit: %w", err)
	}
	return err
}

// SetRunTitle updates only the run's title, leaving all other columns
// untouched.
func (d *DB) SetRunTitle(ctx context.Context, id domain.RunID, title string) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET title = ? WHERE id = ?`, title, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: set run title: %w", err)
	}
	return err
}

func (d *DB) UpdateRunStatus(ctx context.Context, id domain.RunID, status domain.RunStatus, reason string, startedAt, finishedAt *time.Time) error {
	if !status.Valid() {
		return fmt.Errorf("store: update run status: invalid status %q", status)
	}
	started, err := encodeTimePtr(startedAt)
	if err != nil {
		return fmt.Errorf("store: update run status: started at: %w", err)
	}
	finished, err := encodeTimePtr(finishedAt)
	if err != nil {
		return fmt.Errorf("store: update run status: finished at: %w", err)
	}
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, reason = ?,
		     started_at = COALESCE(?, started_at),
		     finished_at = COALESCE(?, finished_at)
		 WHERE id = ?`,
		status, reason, started, finished, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update run status: %w", err)
	}
	return err
}

// TransferRun reassigns a run's owner. Existence of the new owner is
// enforced by the runs.member_id REFERENCES members(id) foreign key
// (foreign_keys pragma is on): a bogus member surfaces as ErrConflict
// via mapConstraint rather than being silently accepted.
func (d *DB) TransferRun(ctx context.Context, id domain.RunID, to domain.MemberID) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET member_id = ? WHERE id = ?`, to, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: transfer run: %w", mapConstraint(err, ErrNotFound))
	}
	return err
}

func (d *DB) SetRunProtected(ctx context.Context, id domain.RunID, protected bool) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET protected = ? WHERE id = ?`, protected, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: set run protected: %w", err)
	}
	return err
}

// SetRunArchived is the narrow, conditional archive/restore mutator.
// Archiving (at != nil) takes effect only on a Final, unarchived run;
// restoring (at == nil) takes effect only on an archived run. The
// reported bool is whether this call changed the column; false with a
// nil error also covers a run that does not exist.
func (d *DB) SetRunArchived(ctx context.Context, id domain.RunID, at *time.Time) (bool, error) {
	var (
		result sql.Result
		err    error
	)
	if at != nil {
		ts, encErr := encodeTime(*at)
		if encErr != nil {
			return false, fmt.Errorf("store: set run archived: %w", encErr)
		}
		var placeholders []string
		args := []any{ts, id}
		for _, s := range domain.AllRunStatuses {
			if s.Final() {
				placeholders = append(placeholders, "?")
				args = append(args, s)
			}
		}
		query := `UPDATE runs SET archived_at = ? WHERE id = ? AND archived_at IS NULL AND status IN (` +
			strings.Join(placeholders, ", ") + `)`
		result, err = d.db.ExecContext(ctx, query, args...)
	} else {
		result, err = d.db.ExecContext(ctx,
			`UPDATE runs SET archived_at = NULL WHERE id = ? AND archived_at IS NOT NULL`, id)
	}
	if err != nil {
		return false, fmt.Errorf("store: set run archived: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: set run archived: %w", err)
	}
	return affected > 0, nil
}

func (d *DB) DeleteRun(ctx context.Context, id domain.RunID) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete run: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	deletes := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM approvals WHERE run_id = ?`, []any{id}},
		// Fold the run's cost into its workspace/member accumulator before
		// the row it came from is gone; see foldDeletedRunCostSQL.
		{foldDeletedRunCostSQL, []any{id}},
		{`DELETE FROM run_costs WHERE run_id = ?`, []any{id}},
		// Published audit rows are reconciliation cache and may be removed
		// with retired mailbox rows. Pending and quarantined rows retain their
		// immutable event projection after the run is gone.
		{`DELETE FROM coord_audit_publications
			WHERE publication_state = ?
			  AND message_id IN (
				SELECT id FROM run_messages WHERE from_run = ? OR to_run = ?
			  )`, []any{CoordAuditPublicationPublished, id, id}},
		{`DELETE FROM run_messages WHERE from_run = ? OR to_run = ?`, []any{id, id}},
		{`DELETE FROM run_steerers WHERE run_id = ?`, []any{id}},
	}
	for _, deletion := range deletes {
		if _, execErr := tx.ExecContext(ctx, deletion.query, deletion.args...); execErr != nil {
			return fmt.Errorf("store: delete run: %w", mapConstraint(execErr, ErrInUse))
		}
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete run: %w", mapConstraint(err, ErrInUse))
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete run: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("store: delete run: %w", ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete run: commit: %w", err)
	}
	return nil
}

// Run steerers

// AddRunSteerer records that a member other than the run's owner steered
// it - injected a message or typed into its terminal. The set is what the
// run's commits credit as co-authors. Returns whether this call was the
// one that added the member, so a caller can stamp the timeline and
// refresh the container's list exactly once.
func (d *DB) AddRunSteerer(ctx context.Context, run domain.RunID, member domain.MemberID) (bool, error) {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO run_steerers (run_id, member_id) VALUES (?, ?)
		 ON CONFLICT (run_id, member_id) DO NOTHING`, run, member)
	if err != nil {
		return false, fmt.Errorf("store: add run steerer: %w", mapConstraint(err, ErrNotFound))
	}
	added, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: add run steerer: %w", err)
	}
	return added > 0, nil
}

// ListRunSteerers returns the members who steered the run. It is a set -
// when they joined is on the timeline - so the order is by member ID,
// which keeps the trailers it produces stable across calls.
func (d *DB) ListRunSteerers(ctx context.Context, run domain.RunID) ([]*domain.Member, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+memberCols+` FROM members
		 WHERE id IN (SELECT member_id FROM run_steerers WHERE run_id = ?)
		 ORDER BY id`, run)
	if err != nil {
		return nil, fmt.Errorf("store: list run steerers: %w", err)
	}
	return collect(rows, scanMember)
}
