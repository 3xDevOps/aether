package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// RunLimitStore persists the server-wide run limits an admin sets from the
// dashboard.
type RunLimitStore interface {
	// ServerRunLimits returns the limits, zero in every field when none are set.
	ServerRunLimits(ctx context.Context) (domain.RunLimits, error)
	PutServerRunLimits(ctx context.Context, limits domain.RunLimits) error
}

func (d *DB) ServerRunLimits(ctx context.Context) (domain.RunLimits, error) {
	var l domain.RunLimits
	err := d.db.QueryRowContext(ctx, `SELECT cpus, memory_bytes, pids FROM server_run_limits WHERE id = 1`).
		Scan(&l.CPUs, &l.MemoryBytes, &l.Pids)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.RunLimits{}, fmt.Errorf("store: server run limits: %w", err)
	}
	return l, nil
}

func (d *DB) PutServerRunLimits(ctx context.Context, l domain.RunLimits) error {
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO server_run_limits (id, cpus, memory_bytes, pids) VALUES (1, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET cpus = excluded.cpus, memory_bytes = excluded.memory_bytes, pids = excluded.pids`,
		l.CPUs, l.MemoryBytes, l.Pids); err != nil {
		return fmt.Errorf("store: put server run limits: %w", err)
	}
	return nil
}
