package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// RunAccountSegment is the stretch of a run spent on one account. Segment 0
// is the account the run launched on; each later one is a move to another.
type RunAccountSegment struct {
	Seq             int
	AccountMemberID domain.MemberID
	Reason          string
	StartedAt       time.Time
}

// RunAccountLaunchReason is segment 0's reason.
const RunAccountLaunchReason = "launch"

// SetRunAccount moves a running run to account and appends the segment that
// starts at, recording segment 0 for the account it leaves on its first
// move. Earlier segments are never rewritten. The run keeps the account it
// first asked for in requested_account_member_id, cleared when the run moves
// back to it. A run already on account is left as it is. It returns the
// run's current segment.
func (d *DB) SetRunAccount(ctx context.Context, id domain.RunID, account domain.MemberID, reason string, at time.Time) (int, error) {
	startedAt, err := encodeTime(at)
	if err != nil {
		return 0, fmt.Errorf("store: set run account: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: set run account: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var (
		current, owner, requested string
		launched                  int64
		last                      int
	)
	err = tx.QueryRowContext(ctx,
		`SELECT account_member_id, member_id, COALESCE(requested_account_member_id, ''), COALESCE(started_at, created_at)
		 FROM runs WHERE id = ?`, id).Scan(&current, &owner, &requested, &launched)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("store: set run account: %w", err)
	}
	if current == "" {
		current = owner
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), -1) FROM run_account_segments WHERE run_id = ?`, id).Scan(&last); err != nil {
		return 0, fmt.Errorf("store: set run account: %w", err)
	}
	if domain.MemberID(current) == account {
		return max(last, 0), nil
	}
	insert := `INSERT INTO run_account_segments (run_id, seq, account_member_id, reason, started_at) VALUES (?, ?, ?, ?, ?)`
	if last < 0 {
		if _, err := tx.ExecContext(ctx, insert, id, 0, current, RunAccountLaunchReason, launched); err != nil {
			return 0, fmt.Errorf("store: set run account: %w", err)
		}
		last = 0
	}
	if _, err := tx.ExecContext(ctx, insert, id, last+1, account, reason, startedAt); err != nil {
		return 0, fmt.Errorf("store: set run account: %w", mapConstraint(err, ErrNotFound))
	}
	switch domain.MemberID(requested) {
	case "":
		requested = current
	case account:
		requested = ""
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET account_member_id = ?, requested_account_member_id = NULLIF(?, '') WHERE id = ?`,
		account, requested, id); err != nil {
		return 0, fmt.Errorf("store: set run account: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: set run account: commit: %w", err)
	}
	return last + 1, nil
}

// ListRunAccountSegments returns a run's account history in order; none for
// a run that never moved.
func (d *DB) ListRunAccountSegments(ctx context.Context, id domain.RunID) ([]*RunAccountSegment, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT seq, account_member_id, reason, started_at FROM run_account_segments WHERE run_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list run account segments: %w", err)
	}
	return collect(rows, func(row interface{ Scan(...any) error }) (*RunAccountSegment, error) {
		var (
			s         RunAccountSegment
			startedAt int64
		)
		if err := row.Scan(&s.Seq, &s.AccountMemberID, &s.Reason, &startedAt); err != nil {
			return nil, err
		}
		s.StartedAt = decodeTime(startedAt)
		return &s, nil
	})
}
