package store

import (
	"context"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// AssistantFollow is a run Aether watches for a member whose assistant
// launched or messaged it.
type AssistantFollow struct {
	MemberID domain.MemberID
	RunID    domain.RunID
	// NotifiedKey identifies the last state announced to the member.
	NotifiedKey string
	CreatedAt   time.Time
}

// AssistantFollowStore persists the runs followed for members. Removing a
// member or a run removes its follows.
type AssistantFollowStore interface {
	// PutAssistantFollow records the follow; one that exists is kept as is.
	PutAssistantFollow(ctx context.Context, member domain.MemberID, run domain.RunID) error
	ListAssistantFollows(ctx context.Context, run domain.RunID) ([]*AssistantFollow, error)
	SetAssistantFollowNotified(ctx context.Context, member domain.MemberID, run domain.RunID, key string) error
	DeleteAssistantFollow(ctx context.Context, member domain.MemberID, run domain.RunID) error
}

func (d *DB) PutAssistantFollow(ctx context.Context, member domain.MemberID, run domain.RunID) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assistant_follows (member_id, run_id, created_at) VALUES (?, ?, unixepoch())
		 ON CONFLICT(member_id, run_id) DO NOTHING`, member, run)
	if err != nil {
		return fmt.Errorf("store: put assistant follow: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) ListAssistantFollows(ctx context.Context, run domain.RunID) ([]*AssistantFollow, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT member_id, run_id, notified_key, created_at FROM assistant_follows WHERE run_id = ? ORDER BY created_at, member_id`, run)
	if err != nil {
		return nil, fmt.Errorf("store: list assistant follows: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []*AssistantFollow
	for rows.Next() {
		var f AssistantFollow
		var created int64
		if err := rows.Scan(&f.MemberID, &f.RunID, &f.NotifiedKey, &created); err != nil {
			return nil, fmt.Errorf("store: list assistant follows: %w", err)
		}
		f.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, &f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list assistant follows: %w", err)
	}
	return out, nil
}

func (d *DB) SetAssistantFollowNotified(ctx context.Context, member domain.MemberID, run domain.RunID, key string) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE assistant_follows SET notified_key = ? WHERE member_id = ? AND run_id = ?`, key, member, run)
	if err != nil {
		return fmt.Errorf("store: set assistant follow notified: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: set assistant follow notified: %w", ErrNotFound)
	}
	return nil
}

func (d *DB) DeleteAssistantFollow(ctx context.Context, member domain.MemberID, run domain.RunID) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM assistant_follows WHERE member_id = ? AND run_id = ?`, member, run)
	if err != nil {
		return fmt.Errorf("store: delete assistant follow: %w", err)
	}
	return nil
}
