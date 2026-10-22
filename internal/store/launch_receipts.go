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
	// (member, key), or ErrNotFound. The run is empty while the receipt is
	// reserved and its launch has not created a run.
	LaunchReceipt(ctx context.Context, member domain.MemberID, key string) (domain.RunID, string, error)
	// ReserveLaunchReceipt records, before the launch, that (member, key)
	// launches with fingerprint. It returns ErrConflict when the key is
	// already reserved.
	ReserveLaunchReceipt(ctx context.Context, member domain.MemberID, key, fingerprint string) error
	// CompleteLaunchReceipt records the run a reserved launch created.
	CompleteLaunchReceipt(ctx context.Context, member domain.MemberID, key string, run domain.RunID) error
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

func (d *DB) ReserveLaunchReceipt(ctx context.Context, member domain.MemberID, key, fingerprint string) error {
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO run_launch_receipts (member_id, idempotency_key, fingerprint, run_id, created_at) VALUES (?, ?, ?, '', ?)`,
		member, key, fingerprint, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("store: reserve launch receipt: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) CompleteLaunchReceipt(ctx context.Context, member domain.MemberID, key string, run domain.RunID) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE run_launch_receipts SET run_id = ? WHERE member_id = ? AND idempotency_key = ? AND run_id = ''`, run, member, key)
	if err != nil {
		return fmt.Errorf("store: complete launch receipt: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: complete launch receipt: %w", err)
	} else if n == 0 {
		return fmt.Errorf("%w: no reserved launch receipt %q", ErrNotFound, key)
	}
	return nil
}
