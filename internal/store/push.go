package store

import (
	"context"
	"errors"
	"fmt"

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
