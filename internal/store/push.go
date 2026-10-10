package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

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
	PutPushSubscription(ctx context.Context, sub *PushSubscription) error
	ListPushSubscriptions(ctx context.Context, member domain.MemberID) ([]*PushSubscription, error)
	// DeletePushSubscription removes member's subscription with endpoint,
	// or returns ErrNotFound.
	DeletePushSubscription(ctx context.Context, member domain.MemberID, endpoint string) error
}

func (d *DB) PutPushSubscription(ctx context.Context, sub *PushSubscription) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO push_subscriptions (endpoint, member_id, p256dh, auth) VALUES (?, ?, ?, ?)
		 ON CONFLICT(endpoint) DO UPDATE SET
		   member_id = excluded.member_id, p256dh = excluded.p256dh, auth = excluded.auth`,
		sub.Endpoint, sub.MemberID, sub.P256DH, sub.Auth)
	if err != nil {
		return fmt.Errorf("store: put push subscription: %w", mapConstraint(err, ErrNotFound))
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
