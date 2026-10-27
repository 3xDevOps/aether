package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// WatchPRSnapshot is the last read of one pull request with one member's
// GitHub credentials: the JSON of a github.Snapshot, ETags included, so a
// restarted server asks GitHub conditionally.
type WatchPRSnapshot struct {
	MemberID  domain.MemberID
	Repo      string
	Number    int
	Snapshot  []byte
	CheckedAt time.Time
}

// watchPRSnapshotRetention drops snapshots no watch has read for a day.
const watchPRSnapshotRetention = 24 * time.Hour

func (d *DB) WatchPRSnapshot(ctx context.Context, member domain.MemberID, repo string, number int) (*WatchPRSnapshot, error) {
	s := WatchPRSnapshot{MemberID: member, Repo: repo, Number: number}
	var checked int64
	err := d.db.QueryRowContext(ctx,
		`SELECT snapshot, checked_at FROM watch_pr_snapshots WHERE member_id = ? AND repo = ? AND pr_number = ?`,
		member, repo, number).Scan(&s.Snapshot, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read pull request snapshot: %w", err)
	}
	s.CheckedAt = decodeTime(checked)
	return &s, nil
}

func (d *DB) SaveWatchPRSnapshot(ctx context.Context, s *WatchPRSnapshot) error {
	checked, err := encodeTime(s.CheckedAt)
	if err != nil {
		return fmt.Errorf("store: save pull request snapshot: %w", err)
	}
	cutoff, err := encodeTime(s.CheckedAt.Add(-watchPRSnapshotRetention))
	if err != nil {
		return fmt.Errorf("store: save pull request snapshot: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: save pull request snapshot: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM watch_pr_snapshots WHERE checked_at < ?`, cutoff); err != nil {
		return fmt.Errorf("store: save pull request snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO watch_pr_snapshots (member_id, repo, pr_number, snapshot, checked_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (member_id, repo, pr_number) DO UPDATE SET snapshot = excluded.snapshot, checked_at = excluded.checked_at`,
		s.MemberID, s.Repo, s.Number, s.Snapshot, checked); err != nil {
		return fmt.Errorf("store: save pull request snapshot: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: save pull request snapshot: %w", err)
	}
	return nil
}
