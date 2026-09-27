package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Egress returns the bytes the relay sent in month, "2006-01" in UTC.
func (s *Store) Egress(ctx context.Context, month string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT bytes FROM egress WHERE month = ?`, month).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("edgestore: read egress for %s: %w", month, err)
	}
	return n, nil
}

// AddEgress adds n bytes to month's egress.
func (s *Store) AddEgress(ctx context.Context, month string, n int64) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO egress (month, bytes) VALUES (?, ?)
		ON CONFLICT (month) DO UPDATE SET bytes = bytes + excluded.bytes`, month, n); err != nil {
		return fmt.Errorf("edgestore: add egress for %s: %w", month, err)
	}
	return nil
}
