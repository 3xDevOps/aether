package store

import (
	"context"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// CloseDependents is what closing one run would affect beyond the run
// itself.
type CloseDependents struct {
	// Messages counts the mail from other runs that asks it something and
	// it has not acknowledged, and any mail since the recent cutoff from
	// runs other than its parent and subsessions.
	Messages int
	// Awaiting lists the live runs with a pending wait on it finishing.
	Awaiting []domain.RunID
	// Children lists its attached subsessions that close with it: those
	// not final and not already accepted.
	Children []domain.RunID
}

// CloseCheckStore reads what a close would affect.
type CloseCheckStore interface {
	CloseDependents(ctx context.Context, run domain.RunID, recent time.Time) (CloseDependents, error)
}

func (d *DB) CloseDependents(ctx context.Context, run domain.RunID, recent time.Time) (CloseDependents, error) {
	var out CloseDependents
	since, err := encodeTime(recent)
	if err != nil {
		return out, fmt.Errorf("store: close dependents: %w", err)
	}
	if err = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_messages
		WHERE to_run = ?1 AND from_run <> ?1 AND ((acked_at IS NULL AND retired_at IS NULL AND kind <> 'reply')
			OR (created_at > ?2 AND from_run NOT IN (SELECT parent_run_id FROM run_children WHERE run_id = ?1
				UNION SELECT run_id FROM run_children WHERE parent_run_id = ?1)))`,
		run, since).Scan(&out.Messages); err != nil {
		return out, fmt.Errorf("store: close dependents: messages: %w", err)
	}
	if out.Awaiting, err = d.runIDs(ctx, `SELECT DISTINCT w.owner_run_id FROM watches w JOIN runs r ON r.id = w.owner_run_id
		WHERE w.wait_target_run = ?1 AND w.wait_state = 'pending' AND w.owner_run_id <> ?1 AND r.status NOT IN (`+terminalStatuses+`)
		ORDER BY w.owner_run_id`, run); err != nil {
		return out, fmt.Errorf("store: close dependents: awaiting: %w", err)
	}
	if out.Children, err = d.runIDs(ctx, `SELECT rc.run_id FROM run_children rc LEFT JOIN runs r ON r.id = rc.run_id
		WHERE rc.parent_run_id = ?1 AND rc.detached = 0 AND (rc.state IN ('queued', 'launching')
			OR (rc.state = 'started' AND r.status NOT IN ('merged', 'abandoned', 'failed', 'interrupted') AND NOT (r.status = 'completed' AND r.review = 'accepted')))
		ORDER BY rc.created_at, rc.run_id`, run); err != nil {
		return out, fmt.Errorf("store: close dependents: subsessions: %w", err)
	}
	return out, nil
}

func (d *DB) runIDs(ctx context.Context, query string, args ...any) ([]domain.RunID, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []domain.RunID
	for rows.Next() {
		var id domain.RunID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
