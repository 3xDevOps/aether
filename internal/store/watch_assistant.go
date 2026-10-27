package store

import (
	"context"
	"fmt"
)

// WatchAssistantStore keeps the deliveries of watch notices to their
// owner's assistant conversation.
type WatchAssistantStore interface {
	// PendingWatchAssistantNotices lists the notifications whose assistant
	// delivery is pending, oldest first, except those of paused watches.
	PendingWatchAssistantNotices(ctx context.Context) ([]*WatchNotification, error)
	// SetWatchDelivery replaces a notification's delivery row on d's
	// channel, or adds one.
	SetWatchDelivery(ctx context.Context, notification string, d WatchDelivery) error
}

func (d *DB) PendingWatchAssistantNotices(ctx context.Context) ([]*WatchNotification, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+watchNotificationColumns+` FROM watch_notifications n JOIN watches w ON w.id = n.watch_id
		 WHERE w.state <> 'paused'
		   AND n.id IN (SELECT notification_id FROM watch_deliveries WHERE channel = 'assistant' AND status = 'pending')
		 ORDER BY n.created_at, n.rowid`)
	if err != nil {
		return nil, fmt.Errorf("store: pending watch assistant notices: %w", err)
	}
	return collect(rows, scanWatchNotification)
}

func (d *DB) SetWatchDelivery(ctx context.Context, notification string, del WatchDelivery) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set watch delivery: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := setWatchDelivery(ctx, tx, notification, del); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set watch delivery: %w", err)
	}
	return nil
}
