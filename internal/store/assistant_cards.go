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

// assistantCardRetention is how long a decided card is kept after its last
// change.
const assistantCardRetention = 30 * 24 * time.Hour

// AssistantCard is an approval card of a member's assistant: the exact call
// it approves and what became of it.
type AssistantCard struct {
	ID       string
	MemberID domain.MemberID
	Method   string
	Params   string
	// Card and Origin are the card as the member saw it and the origin of
	// the turn that raised it, encoded by the assistant.
	Card   string
	Origin string
	State  string
	// ActionID names the card's action records in the conversation.
	ActionID string
	Result   string
	Error    string
	// ShownAt is when the row the card shows was last updated, to the
	// nanosecond, for a call that must act on that version only.
	ShownAt   time.Time
	CreatedAt time.Time
	ExpiresAt time.Time
	UpdatedAt time.Time
}

// AssistantCardMove is a card's next state and what it records.
type AssistantCardMove struct {
	State string
	// ActionID, Result and Error replace the card's when set.
	ActionID string
	Result   string
	Error    string
}

// AssistantCardStore keeps the assistants' approval cards. Removing a member
// removes theirs.
type AssistantCardStore interface {
	AddAssistantCard(ctx context.Context, card *AssistantCard) error
	// AssistantCard returns ErrNotFound when member has no card id.
	AssistantCard(ctx context.Context, member domain.MemberID, id string) (*AssistantCard, error)
	// AssistantCards lists member's cards in states, oldest first; an empty
	// member lists every member's.
	AssistantCards(ctx context.Context, member domain.MemberID, states ...string) ([]AssistantCard, error)
	// MoveAssistantCard applies move if the card is in one of the states
	// from, and reports whether it did. Two moves from the same state race;
	// one wins.
	MoveAssistantCard(ctx context.Context, member domain.MemberID, id string, from []string, move AssistantCardMove) (bool, error)
}

func (d *DB) AddAssistantCard(ctx context.Context, c *AssistantCard) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: add assistant card: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	now := time.Now()
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO assistant_cards (id, member_id, method, params, card, origin, state, action_id, result, error, shown_at, created_at, expires_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.MemberID, c.Method, c.Params, c.Card, c.Origin, c.State, c.ActionID, c.Result, c.Error,
		nanos(c.ShownAt), c.CreatedAt.UnixMilli(), c.ExpiresAt.UnixMilli(), now.UnixMilli()); err != nil {
		return fmt.Errorf("store: add assistant card: %w", mapConstraint(err, ErrNotFound))
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM assistant_cards WHERE member_id = ? AND state NOT IN ('pending', 'approved', 'running') AND updated_at < ?`,
		c.MemberID, now.Add(-assistantCardRetention).UnixMilli()); err != nil {
		return fmt.Errorf("store: prune assistant cards: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: add assistant card: %w", err)
	}
	c.UpdatedAt = time.UnixMilli(now.UnixMilli()).UTC()
	return nil
}

const assistantCardColumns = `id, member_id, method, params, card, origin, state, action_id, result, error, shown_at, created_at, expires_at, updated_at`

func scanAssistantCard(row interface{ Scan(...any) error }) (AssistantCard, error) {
	var c AssistantCard
	var shown, created, expires, updated int64
	err := row.Scan(&c.ID, &c.MemberID, &c.Method, &c.Params, &c.Card, &c.Origin, &c.State, &c.ActionID, &c.Result, &c.Error,
		&shown, &created, &expires, &updated)
	if shown != 0 {
		c.ShownAt = time.Unix(0, shown).UTC()
	}
	c.CreatedAt, c.ExpiresAt, c.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC(), time.UnixMilli(updated).UTC()
	return c, err
}

func (d *DB) AssistantCard(ctx context.Context, member domain.MemberID, id string) (*AssistantCard, error) {
	c, err := scanAssistantCard(d.db.QueryRowContext(ctx,
		`SELECT `+assistantCardColumns+` FROM assistant_cards WHERE member_id = ? AND id = ?`, member, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get assistant card: %w", err)
	}
	return &c, nil
}

func (d *DB) AssistantCards(ctx context.Context, member domain.MemberID, states ...string) ([]AssistantCard, error) {
	query := `SELECT ` + assistantCardColumns + ` FROM assistant_cards WHERE (? = '' OR member_id = ?)`
	args := []any{member, member}
	if len(states) > 0 {
		query += ` AND state IN (?` + strings.Repeat(", ?", len(states)-1) + `)`
		for _, s := range states {
			args = append(args, s)
		}
	}
	rows, err := d.db.QueryContext(ctx, query+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list assistant cards: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []AssistantCard
	for rows.Next() {
		c, scanErr := scanAssistantCard(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: list assistant cards: %w", scanErr)
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list assistant cards: %w", err)
	}
	return out, nil
}

func (d *DB) MoveAssistantCard(ctx context.Context, member domain.MemberID, id string, from []string, move AssistantCardMove) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("store: move assistant card: no state to move from")
	}
	args := []any{move.State, move.ActionID, move.Result, move.Error, time.Now().UnixMilli(), member, id}
	for _, s := range from {
		args = append(args, s)
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE assistant_cards SET state = ?, action_id = COALESCE(NULLIF(?, ''), action_id),
		   result = COALESCE(NULLIF(?, ''), result), error = COALESCE(NULLIF(?, ''), error), updated_at = ?
		 WHERE member_id = ? AND id = ? AND state IN (?`+strings.Repeat(", ?", len(from)-1)+`)`, args...)
	if err != nil {
		return false, fmt.Errorf("store: move assistant card: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: move assistant card: %w", err)
	}
	return n == 1, nil
}

func nanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
