package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// WatchRelay is one notice's delivery row to one session a watch relays
// to.
type WatchRelay struct {
	ID           int64
	Notification *WatchNotification
	RunID        domain.RunID
	Attempts     int
}

// WatchRelayStore keeps the deliveries of watch notices into the sessions
// watches relay to.
type WatchRelayStore interface {
	// DueWatchRelays lists the relay rows due at or before now, oldest
	// first, except those of paused watches.
	DueWatchRelays(ctx context.Context, now time.Time) ([]*WatchRelay, error)
	// ClaimWatchRelay records one more attempt on r, due again at next,
	// while r.Attempts is still stored, else returns ErrConflict. It
	// advances r.Attempts.
	ClaimWatchRelay(ctx context.Context, r *WatchRelay, detail string, at, next time.Time) error
	// SettleWatchRelay records how r ended; a nil next leaves it due never
	// again.
	SettleWatchRelay(ctx context.Context, r *WatchRelay, status, detail string, at time.Time, next *time.Time) error
}

func (d *DB) DueWatchRelays(ctx context.Context, now time.Time) ([]*WatchRelay, error) {
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: due watch relays: %w", err)
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT d.id, d.run_id, d.attempts, n.id FROM watch_deliveries d
		 JOIN watch_notifications n ON n.id = d.notification_id JOIN watches w ON w.id = n.watch_id
		 WHERE d.channel = 'relay' AND d.next_at IS NOT NULL AND d.next_at <= ? AND w.state <> 'paused'
		 ORDER BY d.next_at, d.id`, at)
	if err != nil {
		return nil, fmt.Errorf("store: due watch relays: %w", err)
	}
	type row struct {
		r            WatchRelay
		notification string
	}
	found, err := collect(rows, func(s interface{ Scan(...any) error }) (*row, error) {
		var r row
		return &r, s.Scan(&r.r.ID, &r.r.RunID, &r.r.Attempts, &r.notification)
	})
	if err != nil {
		return nil, fmt.Errorf("store: due watch relays: %w", err)
	}
	out := make([]*WatchRelay, 0, len(found))
	for _, f := range found {
		n, err := d.GetWatchNotification(ctx, f.notification)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		r := f.r
		r.Notification = n
		out = append(out, &r)
	}
	return out, nil
}

func (d *DB) ClaimWatchRelay(ctx context.Context, r *WatchRelay, detail string, at, next time.Time) error {
	a, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: claim watch relay: %w", err)
	}
	n, err := encodeTime(next)
	if err != nil {
		return fmt.Errorf("store: claim watch relay: %w", err)
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE watch_deliveries SET attempts = attempts + 1, next_at = ?, status = 'pending', detail = ?, at = ?
		 WHERE id = ? AND channel = 'relay' AND attempts = ? AND next_at IS NOT NULL`,
		n, detail, a, r.ID, r.Attempts)
	if err != nil {
		return fmt.Errorf("store: claim watch relay: %w", err)
	}
	if count, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: claim watch relay: %w", err)
	} else if count == 0 {
		return fmt.Errorf("store: watch relay %d: %w", r.ID, ErrConflict)
	}
	r.Attempts++
	return nil
}

func (d *DB) SettleWatchRelay(ctx context.Context, r *WatchRelay, status, detail string, at time.Time, next *time.Time) error {
	a, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: settle watch relay: %w", err)
	}
	n, err := encodeTimePtr(next)
	if err != nil {
		return fmt.Errorf("store: settle watch relay: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE watch_deliveries SET status = ?, detail = ?, at = ?, next_at = ? WHERE id = ? AND attempts = ?`,
		status, detail, a, n, r.ID, r.Attempts); err != nil {
		return fmt.Errorf("store: settle watch relay: %w", err)
	}
	return nil
}
