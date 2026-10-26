package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const watchNotificationColumns = `n.id, n.watch_id, w.member_id, n.condition, n.dedup_key, n.title, n.body, n.link,
	n.created_at, n.push_state, n.push_attempts, n.push_next_at`

func scanWatchNotification(row interface{ Scan(...any) error }) (*WatchNotification, error) {
	var (
		n       WatchNotification
		created int64
		next    *int64
	)
	if err := row.Scan(&n.ID, &n.WatchID, &n.MemberID, &n.Condition, &n.Key, &n.Title, &n.Body, &n.Link,
		&created, &n.PushState, &n.PushAttempts, &next); err != nil {
		return nil, err
	}
	n.CreatedAt, n.PushNextAt = decodeTime(created), decodeTimePtr(next)
	return &n, nil
}

func (d *DB) ListWatchNotifications(ctx context.Context, watch string, limit int) ([]*WatchNotification, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+watchNotificationColumns+` FROM watch_notifications n JOIN watches w ON w.id = n.watch_id
		 WHERE n.watch_id = ? ORDER BY n.created_at DESC, n.rowid DESC LIMIT ?`, watch, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list watch notifications: %w", err)
	}
	out, err := collect(rows, scanWatchNotification)
	if err != nil {
		return nil, err
	}
	return out, d.loadWatchDeliveries(ctx, out)
}

func (d *DB) GetWatchNotification(ctx context.Context, id string) (*WatchNotification, error) {
	n, err := scanWatchNotification(d.db.QueryRowContext(ctx,
		`SELECT `+watchNotificationColumns+` FROM watch_notifications n JOIN watches w ON w.id = n.watch_id WHERE n.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: watch notification %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get watch notification: %w", err)
	}
	return n, d.loadWatchDeliveries(ctx, []*WatchNotification{n})
}

func (d *DB) DueWatchPushes(ctx context.Context, now time.Time) ([]*WatchNotification, error) {
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: due watch pushes: %w", err)
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+watchNotificationColumns+` FROM watch_notifications n JOIN watches w ON w.id = n.watch_id
		 WHERE n.push_state <> 'none' AND n.push_next_at <= ? ORDER BY n.created_at, n.rowid`, at)
	if err != nil {
		return nil, fmt.Errorf("store: due watch pushes: %w", err)
	}
	return collect(rows, scanWatchNotification)
}

func (d *DB) loadWatchDeliveries(ctx context.Context, notes []*WatchNotification) error {
	if len(notes) == 0 {
		return nil
	}
	byID := make(map[string]*WatchNotification, len(notes))
	args := make([]any, 0, len(notes))
	for _, n := range notes {
		byID[n.ID] = n
		args = append(args, n.ID)
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT notification_id, channel, status, detail, at FROM watch_deliveries
		 WHERE notification_id IN (?`+strings.Repeat(", ?", len(args)-1)+`) ORDER BY id`, args...)
	if err != nil {
		return fmt.Errorf("store: list watch deliveries: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	for rows.Next() {
		var (
			id  string
			del WatchDelivery
			at  int64
		)
		if err := rows.Scan(&id, &del.Channel, &del.Status, &del.Detail, &at); err != nil {
			return fmt.Errorf("store: list watch deliveries: %w", err)
		}
		del.At = decodeTime(at)
		byID[id].Deliveries = append(byID[id].Deliveries, del)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: list watch deliveries: %w", err)
	}
	return nil
}

func (d *DB) SetWatchPush(ctx context.Context, id, state string, attempts int, next *time.Time, del *WatchDelivery) error {
	at, err := encodeTimePtr(next)
	if err != nil {
		return fmt.Errorf("store: set watch push: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set watch push: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := notFoundOnZeroRows(tx.ExecContext(ctx,
		`UPDATE watch_notifications SET push_state = ?, push_attempts = ?, push_next_at = ? WHERE id = ?`,
		state, attempts, at, id)); err != nil {
		return fmt.Errorf("store: set watch push %s: %w", id, err)
	}
	if del != nil {
		if err := insertWatchDelivery(ctx, tx, id, *del); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set watch push: %w", err)
	}
	return nil
}

func (d *DB) GetNotifyPrefs(ctx context.Context, member domain.MemberID) (NotifyPrefs, error) {
	p := NotifyPrefs{Push: true}
	err := d.db.QueryRowContext(ctx,
		`SELECT push, quiet_start, quiet_end, quiet_tz FROM notify_prefs WHERE member_id = ?`, member).
		Scan(&p.Push, &p.QuietStart, &p.QuietEnd, &p.QuietTZ)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return NotifyPrefs{}, fmt.Errorf("store: get notify prefs: %w", err)
	}
	return p, nil
}

func (d *DB) PutNotifyPrefs(ctx context.Context, member domain.MemberID, p NotifyPrefs) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO notify_prefs (member_id, push, quiet_start, quiet_end, quiet_tz) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (member_id) DO UPDATE SET push = excluded.push, quiet_start = excluded.quiet_start,
		   quiet_end = excluded.quiet_end, quiet_tz = excluded.quiet_tz`,
		member, p.Push, p.QuietStart, p.QuietEnd, p.QuietTZ)
	if err != nil {
		return fmt.Errorf("store: put notify prefs: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) WatchCursor(ctx context.Context) (uint64, bool, error) {
	var seq uint64
	err := d.db.QueryRowContext(ctx, `SELECT seq FROM watch_cursor WHERE id = 1`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: read watch cursor: %w", err)
	}
	return seq, true, nil
}

func (d *DB) SetWatchCursor(ctx context.Context, seq uint64) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO watch_cursor (id, seq) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET seq = max(seq, excluded.seq)`, seq)
	if err != nil {
		return fmt.Errorf("store: save watch cursor: %w", err)
	}
	return nil
}
