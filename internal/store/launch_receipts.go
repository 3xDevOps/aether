package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// LaunchReceiptStore remembers which run a member's idempotent launch
// started, so a retry returns it instead of starting another.
type LaunchReceiptStore interface {
	// LaunchReceipt returns the run and params fingerprint recorded under
	// (member, key), or ErrNotFound.
	LaunchReceipt(ctx context.Context, member domain.MemberID, key string) (domain.RunID, string, error)
	// RecordLaunchReceipt records that the launch with fingerprint started
	// run under (member, key).
	RecordLaunchReceipt(ctx context.Context, member domain.MemberID, key, fingerprint string, run *domain.Run) error
}

func (d *DB) LaunchReceipt(ctx context.Context, member domain.MemberID, key string) (domain.RunID, string, error) {
	var run, fingerprint string
	err := d.db.QueryRowContext(ctx, `SELECT run_id, fingerprint FROM run_launch_receipts WHERE member_id = ? AND idempotency_key = ?`, member, key).Scan(&run, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: launch receipt %q", ErrNotFound, key)
	}
	if err != nil {
		return "", "", fmt.Errorf("store: read launch receipt: %w", err)
	}
	return domain.RunID(run), fingerprint, nil
}

func (d *DB) RecordLaunchReceipt(ctx context.Context, member domain.MemberID, key, fingerprint string, run *domain.Run) error {
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO run_launch_receipts (member_id, idempotency_key, fingerprint, run_id, workspace_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		member, key, fingerprint, run.ID, run.WorkspaceID, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("store: record launch receipt: %w", err)
	}
	return nil
}
