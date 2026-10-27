package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// CoordStateReport is the receipt of a needs-input, waiting or answered
// report. It holds no evidence and no terminal slot; it only binds the run's
// idempotency key to the report's inputs.
type CoordStateReport struct {
	RunID          domain.RunID
	IdempotencyKey string
	Outcome        string
	WaitingFor     string
	Summary        string
	// Until is a waiting report's conditions, canonical.
	Until     []string
	CreatedAt time.Time
}

// queryExecer is a *sql.DB or a *sql.Tx.
type queryExecer interface {
	execer
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// RecordCoordStateReport stores report's receipt and reports whether it is
// new. A retry with the same inputs loads the original receipt into report;
// the same key with other inputs fails ErrCoordReportIdempotencyConflict.
func (d *DB) RecordCoordStateReport(ctx context.Context, report *CoordStateReport) (bool, error) {
	return recordCoordStateReport(ctx, d.db, report)
}

func recordCoordStateReport(ctx context.Context, db queryExecer, report *CoordStateReport) (bool, error) {
	ts := report.CreatedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return false, fmt.Errorf("store: record coord state report: %w", err)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO coord_state_reports (run_id, idempotency_key, outcome, waiting_for, summary, until_conditions, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (run_id, idempotency_key) DO NOTHING`,
		report.RunID, report.IdempotencyKey, report.Outcome, report.WaitingFor, report.Summary, strings.Join(report.Until, "\n"), createdAt)
	if err != nil {
		return false, fmt.Errorf("store: record coord state report: %w", mapConstraint(err, ErrNotFound))
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: record coord state report: %w", err)
	}
	if inserted == 1 {
		report.CreatedAt = ts
		return true, nil
	}
	prior, err := getCoordStateReport(ctx, db, report.RunID, report.IdempotencyKey)
	if err != nil {
		return false, err
	}
	if prior.Outcome != report.Outcome || prior.WaitingFor != report.WaitingFor || prior.Summary != report.Summary ||
		!slices.Equal(prior.Until, report.Until) {
		return false, fmt.Errorf("%w: run %s", ErrCoordReportIdempotencyConflict, report.RunID)
	}
	*report = *prior
	return false, nil
}

// GetCoordStateReport loads the receipt a run stored under key.
func (d *DB) GetCoordStateReport(ctx context.Context, run domain.RunID, key string) (*CoordStateReport, error) {
	return getCoordStateReport(ctx, d.db, run, key)
}

func getCoordStateReport(ctx context.Context, db queryExecer, run domain.RunID, key string) (*CoordStateReport, error) {
	report := CoordStateReport{RunID: run, IdempotencyKey: key}
	var (
		createdAt int64
		until     string
	)
	err := db.QueryRowContext(ctx,
		`SELECT outcome, waiting_for, summary, until_conditions, created_at FROM coord_state_reports
		 WHERE run_id = ? AND idempotency_key = ?`, run, key,
	).Scan(&report.Outcome, &report.WaitingFor, &report.Summary, &until, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get coord state report %s: %w", key, err)
	}
	report.CreatedAt = decodeTime(createdAt)
	if until != "" {
		report.Until = strings.Split(until, "\n")
	}
	return &report, nil
}

// MarkCoordStateReportApplied records that the scheduler accepted the report
// stored under key, so its key is never released.
func (d *DB) MarkCoordStateReportApplied(ctx context.Context, run domain.RunID, key string) error {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE coord_state_reports SET applied_at = ? WHERE run_id = ? AND idempotency_key = ? AND applied_at IS NULL`,
		time.Now().UTC().UnixNano(), run, key); err != nil {
		return fmt.Errorf("store: mark coord state report %s applied: %w", key, err)
	}
	return nil
}

// ReleaseCoordStateReport frees the key of a refused report that never took
// effect, with the conditions it registered.
func (d *DB) ReleaseCoordStateReport(ctx context.Context, run domain.RunID, key string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: release coord state report %s: %w", key, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx,
		`DELETE FROM coord_state_reports WHERE run_id = ? AND idempotency_key = ? AND applied_at IS NULL`, run, key)
	if err != nil {
		return fmt.Errorf("store: release coord state report %s: %w", key, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: release coord state report %s: %w", key, err)
	} else if n > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM watches WHERE owner_run_id = ? AND wait_report_key = ?`, run, key); err != nil {
			return fmt.Errorf("store: release coord state report %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: release coord state report %s: %w", key, err)
	}
	return nil
}

// LastCoordReportAt is when run last reported an outcome of any kind, zero
// when it never did. A refused state report released its receipt and does
// not count.
func (d *DB) LastCoordReportAt(ctx context.Context, run domain.RunID) (time.Time, error) {
	var last sql.NullInt64
	err := d.db.QueryRowContext(ctx,
		`SELECT MAX(at) FROM (
		   SELECT MAX(created_at) AS at FROM coord_reports WHERE run_id = ?
		   UNION ALL
		   SELECT MAX(created_at) FROM coord_state_reports WHERE run_id = ?)`, run, run).Scan(&last)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: last coord report of %s: %w", run, err)
	}
	if !last.Valid {
		return time.Time{}, nil
	}
	return decodeTime(last.Int64), nil
}
