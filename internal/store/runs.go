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
	if err := validateRun(r, "create"); err != nil {
		return err
	}
	id, ts, err := prepareCreate(r.CreatedAt)
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
		return fmt.Errorf("store: create run: last commit at: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status,
		                   reason, branch, worktree, protected, created_at, started_at,
		                   finished_at, profile_snapshot_id, tool_snapshot_id,
		                   last_commit, last_commit_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.WorkspaceID, r.MemberID, r.Task, r.Harness, r.Mode, r.Status,
		r.Reason, r.Branch, r.Worktree, r.Protected, createdAt, startedAt, finishedAt,
		r.ProfileSnapshotID, r.ToolSnapshotID, r.LastCommit, lastCommitAt,
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
	)
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.MemberID, &r.Task, &r.Harness,
		&r.Mode, &r.Status, &r.Reason, &r.Branch, &r.Worktree, &r.Protected,
		&createdAt, &startedAt, &finishedAt, &r.ProfileSnapshotID, &r.ToolSnapshotID,
		&r.LastCommit, &lastCommitAt); err != nil {
		return nil, err
	}
	r.CreatedAt = decodeTime(createdAt)
	r.StartedAt = decodeTimePtr(startedAt)
	r.FinishedAt = decodeTimePtr(finishedAt)
	if lastCommitAt != nil {
		r.LastCommitAt = decodeTime(*lastCommitAt)
	}
	return &r, nil
}

const runCols = `id, workspace_id, member_id, task, harness, mode, status,
	reason, branch, worktree, protected, created_at, started_at, finished_at, profile_snapshot_id, tool_snapshot_id,
	last_commit, last_commit_at`

func (d *DB) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	r, err := scanRun(d.db.QueryRowContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE id = ?`, id))
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
		`SELECT `+runCols+` FROM runs WHERE workspace_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list runs by workspace: %w", err)
	}
	return collect(rows, scanRun)
}

func (d *DB) ListRunsByMember(ctx context.Context, id domain.MemberID) ([]*domain.Run, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE member_id = ? ORDER BY id`, id)
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
		`SELECT `+runCols+` FROM runs WHERE status IN (`+
			strings.Join(placeholders, ", ")+
			`) ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list active runs: %w", err)
	}
	return collect(rows, scanRun)
}

func (d *DB) UpdateRun(ctx context.Context, r *domain.Run) error {
	if err := validateRun(r, "update"); err != nil {
		return err
	}
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
	err = notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET workspace_id = ?, member_id = ?, task = ?, harness = ?,
		     mode = ?, status = ?, reason = ?, branch = ?, worktree = ?,
		     protected = ?, started_at = ?, finished_at = ?,
		     profile_snapshot_id = ?, tool_snapshot_id = ?,
		     last_commit = ?, last_commit_at = ?
		 WHERE id = ?`,
		r.WorkspaceID, r.MemberID, r.Task, r.Harness, r.Mode, r.Status,
		r.Reason, r.Branch, r.Worktree, r.Protected, startedAt, finishedAt,
		r.ProfileSnapshotID, r.ToolSnapshotID, r.LastCommit, lastCommitAt, r.ID,
	))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: update run: %w", mapConstraint(err, ErrNotFound))
	}
	return err
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

// SetRunToolSnapshot updates only the run's tool snapshot pin. A non-empty
// snapshot must belong to the run's current member and workspace, and the
// ownership check is part of the update so a concurrent handoff cannot be
// overwritten by a stale run object.
func (d *DB) SetRunToolSnapshot(ctx context.Context, id domain.RunID, snapshot domain.ToolSnapshotID) error {
	var (
		res sql.Result
		err error
	)
	if snapshot == "" {
		res, err = d.db.ExecContext(ctx,
			`UPDATE runs SET tool_snapshot_id = '' WHERE id = ?`, id)
	} else {
		res, err = d.db.ExecContext(ctx, `
			UPDATE runs
			SET tool_snapshot_id = ?
			WHERE id = ?
			  AND EXISTS (
				SELECT 1
				FROM tool_snapshots ts
				WHERE ts.id = ?
				  AND ts.member_id = runs.member_id
				  AND ts.workspace_id = runs.workspace_id
			  )`, snapshot, id, snapshot)
	}
	if err != nil {
		return fmt.Errorf("store: set run tool snapshot: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set run tool snapshot: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) SetRunProtected(ctx context.Context, id domain.RunID, protected bool) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`UPDATE runs SET protected = ? WHERE id = ?`, protected, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: set run protected: %w", err)
	}
	return err
}

func (d *DB) DeleteRun(ctx context.Context, id domain.RunID) error {
	return d.execDelete(ctx, "delete run", `DELETE FROM runs WHERE id = ?`, id)
}
