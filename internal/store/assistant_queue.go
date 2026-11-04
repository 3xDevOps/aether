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

// AssistantQueueKept is how many ended entries a member keeps past
// assistantQueueHeld.
const AssistantQueueKept = 50

// assistantQueueHeld is how long an ended entry is kept whatever the
// count: as long as a card its turn raised stays open, and a day more.
const assistantQueueHeld = 48 * time.Hour

// ErrAssistantQueueFull means as many entries as allowed already wait.
var ErrAssistantQueueFull = errors.New("store: the assistant queue is full")

// AssistantQueueEntry is one prompt in a member's assistant queue.
type AssistantQueueEntry struct {
	Seq        int64
	ID         string
	MemberID   domain.MemberID
	Generation int64
	Key        string
	Text       string
	// View and Origin are the prompt's view context and origin, encoded by
	// the assistant.
	View      string
	Origin    string
	State     string
	Error     string
	Reply     string
	MessageID string
	// Cards are the ids of the cards the entry's turn raised.
	Cards []string
	// Sent is set once the agent may have had the prompt.
	Sent      bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AssistantQueueMove is an entry's next state and what it records.
type AssistantQueueMove struct {
	State string
	// Error, Reply and MessageID replace the entry's when set, and Sent
	// marks it sent.
	Error     string
	Reply     string
	MessageID string
	Sent      bool
}

// AssistantQueueStore keeps the assistants' prompt queues. Removing a member
// removes theirs.
type AssistantQueueStore interface {
	// EnqueueAssistantPrompt adds e in state queued and returns it, unless
	// e's member has an entry with e's key in e's generation that did not
	// fail unsent: that one is returned, with created false. It fails with ErrAssistantQueueFull
	// when max entries of the generation are queued.
	EnqueueAssistantPrompt(ctx context.Context, e *AssistantQueueEntry, max int) (entry AssistantQueueEntry, created bool, err error)
	// AssistantQueueEntry returns ErrNotFound when member has no entry id.
	AssistantQueueEntry(ctx context.Context, member domain.MemberID, id string) (*AssistantQueueEntry, error)
	// AssistantQueue lists member's entries in states, in queue order; an
	// empty member lists every member's.
	AssistantQueue(ctx context.Context, member domain.MemberID, states ...string) ([]AssistantQueueEntry, error)
	// MoveAssistantQueueEntry applies move if the entry is in one of the
	// states from, and reports whether it did.
	MoveAssistantQueueEntry(ctx context.Context, member domain.MemberID, id string, from []string, move AssistantQueueMove) (bool, error)
	// AddAssistantQueueCard records that entry id's turn raised card.
	AddAssistantQueueCard(ctx context.Context, member domain.MemberID, id, card string) error
	// DeleteAssistantQueue deletes all of member's entries.
	DeleteAssistantQueue(ctx context.Context, member domain.MemberID) error
}

func (d *DB) DeleteAssistantQueue(ctx context.Context, member domain.MemberID) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM assistant_queue WHERE member_id = ?`, member); err != nil {
		return fmt.Errorf("store: delete the assistant queue: %w", err)
	}
	return nil
}

const assistantQueueColumns = `seq, id, member_id, generation, key, text, view, origin, state, error, reply, message_id, cards, sent, created_at, updated_at`

func scanAssistantQueueEntry(row interface{ Scan(...any) error }) (AssistantQueueEntry, error) {
	var e AssistantQueueEntry
	var cards string
	var created, updated int64
	err := row.Scan(&e.Seq, &e.ID, &e.MemberID, &e.Generation, &e.Key, &e.Text, &e.View, &e.Origin, &e.State, &e.Error, &e.Reply, &e.MessageID,
		&cards, &e.Sent, &created, &updated)
	e.Cards = strings.Fields(cards)
	e.CreatedAt, e.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return e, err
}

func (d *DB) EnqueueAssistantPrompt(ctx context.Context, e *AssistantQueueEntry, max int) (AssistantQueueEntry, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantQueueEntry{}, false, fmt.Errorf("store: enqueue assistant prompt: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	held, err := scanAssistantQueueEntry(tx.QueryRowContext(ctx,
		`SELECT `+assistantQueueColumns+` FROM assistant_queue
		 WHERE member_id = ? AND generation = ? AND key = ? AND (state <> 'failed' OR sent = 1)`, e.MemberID, e.Generation, e.Key))
	switch {
	case err == nil:
		return held, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return AssistantQueueEntry{}, false, fmt.Errorf("store: enqueue assistant prompt: %w", err)
	}
	var queued int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assistant_queue WHERE member_id = ? AND generation = ? AND state = 'queued'`,
		e.MemberID, e.Generation).Scan(&queued); err != nil {
		return AssistantQueueEntry{}, false, fmt.Errorf("store: enqueue assistant prompt: %w", err)
	}
	if queued >= max {
		return AssistantQueueEntry{}, false, ErrAssistantQueueFull
	}
	now := time.Now().UnixMilli()
	if err = tx.QueryRowContext(ctx,
		`INSERT INTO assistant_queue (id, member_id, generation, key, text, view, origin, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?) RETURNING seq`,
		e.ID, e.MemberID, e.Generation, e.Key, e.Text, e.View, e.Origin, now, now).Scan(&e.Seq); err != nil {
		return AssistantQueueEntry{}, false, fmt.Errorf("store: enqueue assistant prompt: %w", mapConstraint(err, ErrNotFound))
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM assistant_queue WHERE member_id = ? AND state NOT IN ('queued', 'running') AND updated_at < ? AND seq NOT IN
		   (SELECT seq FROM assistant_queue WHERE member_id = ? AND state NOT IN ('queued', 'running') ORDER BY seq DESC LIMIT ?)`,
		e.MemberID, time.UnixMilli(now).Add(-assistantQueueHeld).UnixMilli(), e.MemberID, AssistantQueueKept); err != nil {
		return AssistantQueueEntry{}, false, fmt.Errorf("store: prune the assistant queue: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return AssistantQueueEntry{}, false, fmt.Errorf("store: enqueue assistant prompt: %w", err)
	}
	e.State, e.Error, e.Reply, e.MessageID, e.Cards = "queued", "", "", "", nil
	e.CreatedAt, e.UpdatedAt = time.UnixMilli(now).UTC(), time.UnixMilli(now).UTC()
	return *e, true, nil
}

func (d *DB) AssistantQueueEntry(ctx context.Context, member domain.MemberID, id string) (*AssistantQueueEntry, error) {
	e, err := scanAssistantQueueEntry(d.db.QueryRowContext(ctx,
		`SELECT `+assistantQueueColumns+` FROM assistant_queue WHERE member_id = ? AND id = ?`, member, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get assistant queue entry: %w", err)
	}
	return &e, nil
}

func (d *DB) AssistantQueue(ctx context.Context, member domain.MemberID, states ...string) ([]AssistantQueueEntry, error) {
	query := `SELECT ` + assistantQueueColumns + ` FROM assistant_queue WHERE (? = '' OR member_id = ?)`
	args := []any{member, member}
	if len(states) > 0 {
		query += ` AND state IN (?` + strings.Repeat(", ?", len(states)-1) + `)`
		for _, s := range states {
			args = append(args, s)
		}
	}
	rows, err := d.db.QueryContext(ctx, query+` ORDER BY seq`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list the assistant queue: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []AssistantQueueEntry
	for rows.Next() {
		e, scanErr := scanAssistantQueueEntry(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: list the assistant queue: %w", scanErr)
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list the assistant queue: %w", err)
	}
	return out, nil
}

func (d *DB) MoveAssistantQueueEntry(ctx context.Context, member domain.MemberID, id string, from []string, move AssistantQueueMove) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("store: move assistant queue entry: no state to move from")
	}
	args := []any{move.State, move.Error, move.Reply, move.MessageID, move.Sent, time.Now().UnixMilli(), member, id}
	for _, s := range from {
		args = append(args, s)
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE assistant_queue SET state = ?, error = COALESCE(NULLIF(?, ''), error), reply = COALESCE(NULLIF(?, ''), reply),
		   message_id = COALESCE(NULLIF(?, ''), message_id), sent = MAX(sent, ?), updated_at = ?
		 WHERE member_id = ? AND id = ? AND state IN (?`+strings.Repeat(", ?", len(from)-1)+`)`, args...)
	if err != nil {
		return false, fmt.Errorf("store: move assistant queue entry: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: move assistant queue entry: %w", err)
	}
	return n == 1, nil
}

func (d *DB) AddAssistantQueueCard(ctx context.Context, member domain.MemberID, id, card string) error {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE assistant_queue SET cards = TRIM(cards || ' ' || ?), updated_at = ? WHERE member_id = ? AND id = ?`,
		card, time.Now().UnixMilli(), member, id); err != nil {
		return fmt.Errorf("store: add the assistant queue entry's card: %w", err)
	}
	return nil
}
