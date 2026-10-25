package store

import (
	"context"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ProvisionalReportPrefix starts the correlation ID of a provisional report:
// the last message of a swarm worker whose turn ended without a report.
const ProvisionalReportPrefix = "provisional:"

// CoordReportedSince reports whether run reserved a report at or after since;
// a zero since matches any report.
func (d *DB) CoordReportedSince(ctx context.Context, run domain.RunID, since time.Time) (bool, error) {
	var after int64
	if !since.IsZero() {
		var err error
		if after, err = encodeTime(since); err != nil {
			return false, fmt.Errorf("store: coord reported since: %w", err)
		}
	}
	var reported bool
	if err := d.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM coord_reports WHERE run_id = ? AND created_at >= ?)`, run, after,
	).Scan(&reported); err != nil {
		return false, fmt.Errorf("store: coord reported since: %w", err)
	}
	return reported, nil
}

// RetireProvisionalReports retires the undelivered provisional reports from
// sent to to before cutoff.
func (d *DB) RetireProvisionalReports(ctx context.Context, from, to domain.RunID, cutoff time.Time) error {
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store: retire provisional reports: %w", err)
	}
	before, err := encodeTime(cutoff)
	if err != nil {
		return fmt.Errorf("store: retire provisional reports: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE run_messages SET retired_at = ?
		 WHERE from_run = ? AND to_run = ? AND correlation_id LIKE ? || '%' AND created_at < ?
		   AND delivery_token = '' AND `+unreadMessage,
		now, from, to, ProvisionalReportPrefix, before,
	); err != nil {
		return fmt.Errorf("store: retire provisional reports: %w", err)
	}
	return nil
}
