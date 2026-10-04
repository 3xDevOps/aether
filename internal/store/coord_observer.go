package store

import (
	"context"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
)

// UnackedRunMessageIDsStore is an optional, read-only mailbox observation seam.
// Observation never creates delivery tokens or acknowledges a batch.
type UnackedRunMessageIDsStore interface {
	ListUnackedRunMessageIDs(context.Context, domain.RunID, int) ([]string, error)
}

var _ UnackedRunMessageIDsStore = (*DB)(nil)

func (d *DB) ListUnackedRunMessageIDs(ctx context.Context, to domain.RunID, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, errors.New("store: observe run messages: limit must be positive")
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM run_messages WHERE to_run=? AND acked_at IS NULL ORDER BY created_at, rowid LIMIT ?`, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only rows
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
