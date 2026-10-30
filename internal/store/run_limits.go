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
	// UpdateServerRunLimits changes the fields that are not nil in one
	// statement, so concurrent updates of different fields both stick, and
	// returns the result.
	UpdateServerRunLimits(ctx context.Context, cpus *float64, memory, pids *int64) (domain.RunLimits, error)
}

func (d *DB) ServerRunLimits(ctx context.Context) (domain.RunLimits, error) {
	var l domain.RunLimits
	err := d.db.QueryRowContext(ctx, `SELECT cpus, memory_bytes, pids FROM server_run_limits WHERE id = 1`).
		Scan(&l.CPUs, &l.MemoryBytes, &l.Pids)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.RunLimits{}, fmt.Errorf("store: server session limits: %w", err)
	}
	return l, nil
}

func (d *DB) UpdateServerRunLimits(ctx context.Context, cpus *float64, memory, pids *int64) (domain.RunLimits, error) {
	var l domain.RunLimits
	err := d.db.QueryRowContext(ctx,
		`INSERT INTO server_run_limits (id, cpus, memory_bytes, pids) VALUES (1, COALESCE(?1, 0), COALESCE(?2, 0), COALESCE(?3, 0))
		 ON CONFLICT(id) DO UPDATE SET cpus = COALESCE(?1, cpus), memory_bytes = COALESCE(?2, memory_bytes), pids = COALESCE(?3, pids)
		 RETURNING cpus, memory_bytes, pids`,
		cpus, memory, pids).Scan(&l.CPUs, &l.MemoryBytes, &l.Pids)
	if err != nil {
		return domain.RunLimits{}, fmt.Errorf("store: update server session limits: %w", err)
	}
	return l, nil
}
