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

// MaxPushSubscriptions bounds one member's subscriptions: every one is an
// outbound request per notification.
const MaxPushSubscriptions = 16

// PushSubscription is one browser a member turned notifications on in.
// Endpoint is the push service's URL for that browser; P256DH and Auth are
// the browser's unpadded base64url keys a message to it is encrypted with.
type PushSubscription struct {
	Endpoint string
	MemberID domain.MemberID
	P256DH   string
	Auth     string
}

// PushStore persists Web Push subscriptions. Removing a member removes
// theirs.
type PushStore interface {
	// PutPushSubscription stores sub, replacing whatever its endpoint held.
	// A member who already has MaxPushSubscriptions others gets ErrLimit.
	PutPushSubscription(ctx context.Context, sub *PushSubscription) error
	ListPushSubscriptions(ctx context.Context, member domain.MemberID) ([]*PushSubscription, error)
	// DeletePushSubscription removes member's subscription with endpoint,
	// or returns ErrNotFound.
	DeletePushSubscription(ctx context.Context, member domain.MemberID, endpoint string) error

	GetNotifyPrefs(ctx context.Context, member domain.MemberID) (NotifyPrefs, error)
	// AddDigestItem queues item for its member's next digest. One already
	// queued under the same source and key keeps its place and becomes
	// overnight if either is.
	AddDigestItem(ctx context.Context, item *DigestItem) error
	// ListDigestItems is member's queued items, oldest first.
	ListDigestItems(ctx context.Context, member domain.MemberID) ([]*DigestItem, error)
	// DigestMembers lists the members with a queued item.
	DigestMembers(ctx context.Context) ([]domain.MemberID, error)
	// DeleteDigestItems removes member's items with the given (source, key).
	DeleteDigestItems(ctx context.Context, member domain.MemberID, items []*DigestItem) error
}

// DigestItem is a notification a member gets in their next digest rather
// than at once. Source names what produced it, such as a session or a
// watch notice, and Key identifies it there.
type DigestItem struct {
	MemberID  domain.MemberID
	Source    string
	Key       string
	Kind      string
	Workspace string
	Title     string
	Body      string
	URL       string
	RunID     domain.RunID
	Overnight bool
	CreatedAt time.Time
}

// The bound is part of the insert, so concurrent subscribes cannot each
// pass a count taken before the other's write.
func (d *DB) PutPushSubscription(ctx context.Context, sub *PushSubscription) error {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO push_subscriptions (endpoint, member_id, p256dh, auth)
		 SELECT ?1, ?2, ?3, ?4
		 WHERE (SELECT COUNT(*) FROM push_subscriptions WHERE member_id = ?2 AND endpoint <> ?1) < ?5
		 ON CONFLICT(endpoint) DO UPDATE SET
		   member_id = excluded.member_id, p256dh = excluded.p256dh, auth = excluded.auth`,
		sub.Endpoint, sub.MemberID, sub.P256DH, sub.Auth, MaxPushSubscriptions)
	if err != nil {
		return fmt.Errorf("store: put push subscription: %w", mapConstraint(err, ErrNotFound))
	}
	stored, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: put push subscription: %w", err)
	}
	if stored == 0 {
		return fmt.Errorf("store: put push subscription: %w", ErrLimit)
	}
	return nil
}

func (d *DB) ListPushSubscriptions(ctx context.Context, member domain.MemberID) ([]*PushSubscription, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT endpoint, member_id, p256dh, auth FROM push_subscriptions WHERE member_id = ? ORDER BY rowid`, member)
	if err != nil {
		return nil, fmt.Errorf("store: list push subscriptions: %w", err)
	}
	return collect(rows, func(row interface{ Scan(...any) error }) (*PushSubscription, error) {
		var sub PushSubscription
		return &sub, row.Scan(&sub.Endpoint, &sub.MemberID, &sub.P256DH, &sub.Auth)
	})
}

func (d *DB) DeletePushSubscription(ctx context.Context, member domain.MemberID, endpoint string) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx,
		`DELETE FROM push_subscriptions WHERE member_id = ? AND endpoint = ?`, member, endpoint))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: delete push subscription: %w", err)
	}
	return err
}

func (d *DB) AddDigestItem(ctx context.Context, item *DigestItem) error {
	return addDigestItem(ctx, d.db, item)
}

func addDigestItem(ctx context.Context, db interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, item *DigestItem) error {
	at, err := encodeTime(item.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: add digest item: %w", err)
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO push_digest (member_id, source, key, kind, workspace, title, body, url, run_id, overnight, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (member_id, source, key) DO UPDATE SET overnight = max(overnight, excluded.overnight)`,
		item.MemberID, item.Source, item.Key, item.Kind, item.Workspace, item.Title, item.Body, item.URL, item.RunID, item.Overnight, at)
	if err != nil {
		return fmt.Errorf("store: add digest item: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) ListDigestItems(ctx context.Context, member domain.MemberID) ([]*DigestItem, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT member_id, source, key, kind, workspace, title, body, url, run_id, overnight, created_at
		 FROM push_digest WHERE member_id = ? ORDER BY created_at, rowid`, member)
	if err != nil {
		return nil, fmt.Errorf("store: list digest items: %w", err)
	}
	return collect(rows, func(row interface{ Scan(...any) error }) (*DigestItem, error) {
		var (
			item    DigestItem
			created int64
		)
		if err := row.Scan(&item.MemberID, &item.Source, &item.Key, &item.Kind, &item.Workspace, &item.Title, &item.Body,
			&item.URL, &item.RunID, &item.Overnight, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = decodeTime(created)
		return &item, nil
	})
}

func (d *DB) DigestMembers(ctx context.Context) ([]domain.MemberID, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT member_id FROM push_digest ORDER BY member_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list digest members: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []domain.MemberID
	for rows.Next() {
		var id domain.MemberID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: list digest members: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list digest members: %w", err)
	}
	return out, nil
}

func (d *DB) DeleteDigestItems(ctx context.Context, member domain.MemberID, items []*DigestItem) error {
	if len(items) == 0 {
		return nil
	}
	args := []any{member}
	for _, item := range items {
		args = append(args, item.Source, item.Key)
	}
	_, err := d.db.ExecContext(ctx,
		`DELETE FROM push_digest WHERE member_id = ? AND (source, key) IN (VALUES (?, ?)`+strings.Repeat(", (?, ?)", len(items)-1)+`)`, args...)
	if err != nil {
		return fmt.Errorf("store: delete digest items: %w", err)
	}
	return nil
}
