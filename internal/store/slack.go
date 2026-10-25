package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// SlackLink is the Slack account a member linked to the server's bot with
// a code from `aether slack link`. Device is the edge device the code was
// minted on, empty for one minted over a member SSH key or a tailnet
// connection. Channel is the bot's direct message with the account.
type SlackLink struct {
	Member    domain.MemberID
	Device    domain.DeviceID
	TeamID    string
	UserID    string
	Channel   string
	CreatedAt time.Time
}

// SlackMessage is the direct message the bot keeps for a run, at TS in
// Channel. NeedKey is the need it shows, empty once the run needs nothing.
type SlackMessage struct {
	RunID   domain.RunID
	Channel string
	TS      string
	NeedKey string
}

// SlackStore persists Slack links and the bot's messages. Removing a
// member or the device a link was minted on removes the link; deleting a
// run removes its message.
type SlackStore interface {
	// PutSlackLink stores l as its member's link, replacing theirs and
	// any other member's link to the same Slack account.
	PutSlackLink(ctx context.Context, l *SlackLink) error
	GetSlackLink(ctx context.Context, member domain.MemberID) (*SlackLink, error)
	SlackLinkByUser(ctx context.Context, team, user string) (*SlackLink, error)
	// DeleteSlackLink removes member's link and returns it, or returns
	// ErrNotFound.
	DeleteSlackLink(ctx context.Context, member domain.MemberID) (*SlackLink, error)
	// PutSlackMessage stores m as its run's message, replacing the last.
	PutSlackMessage(ctx context.Context, m *SlackMessage) error
	GetSlackMessage(ctx context.Context, run domain.RunID) (*SlackMessage, error)
	SlackMessageAt(ctx context.Context, channel, ts string) (*SlackMessage, error)
	DeleteSlackMessage(ctx context.Context, run domain.RunID) error
}

const slackLinkCols = `member_id, device_id, team_id, user_id, channel_id, created_at`

func scanSlackLink(row interface{ Scan(...any) error }) (*SlackLink, error) {
	var (
		l       SlackLink
		device  sql.NullString
		created int64
	)
	if err := row.Scan(&l.Member, &device, &l.TeamID, &l.UserID, &l.Channel, &created); err != nil {
		return nil, err
	}
	l.Device, l.CreatedAt = domain.DeviceID(device.String), decodeTime(created)
	return &l, nil
}

func (d *DB) PutSlackLink(ctx context.Context, l *SlackLink) error {
	created, err := encodeTime(l.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: put slack link: %w", err)
	}
	var device any
	if l.Device != "" {
		device = l.Device
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin put slack link: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM slack_links WHERE team_id = ? AND user_id = ? AND member_id <> ?`,
		l.TeamID, l.UserID, l.Member); err != nil {
		return fmt.Errorf("store: put slack link: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO slack_links (`+slackLinkCols+`) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(member_id) DO UPDATE SET device_id = excluded.device_id, team_id = excluded.team_id,
		   user_id = excluded.user_id, channel_id = excluded.channel_id, created_at = excluded.created_at`,
		l.Member, device, l.TeamID, l.UserID, l.Channel, created); err != nil {
		return fmt.Errorf("store: put slack link: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit put slack link: %w", err)
	}
	return nil
}

func (d *DB) GetSlackLink(ctx context.Context, member domain.MemberID) (*SlackLink, error) {
	return d.slackLink(ctx, "get slack link", `SELECT `+slackLinkCols+` FROM slack_links WHERE member_id = ?`, member)
}

func (d *DB) SlackLinkByUser(ctx context.Context, team, user string) (*SlackLink, error) {
	return d.slackLink(ctx, "get slack link", `SELECT `+slackLinkCols+` FROM slack_links WHERE team_id = ? AND user_id = ?`, team, user)
}

func (d *DB) DeleteSlackLink(ctx context.Context, member domain.MemberID) (*SlackLink, error) {
	return d.slackLink(ctx, "delete slack link", `DELETE FROM slack_links WHERE member_id = ? RETURNING `+slackLinkCols, member)
}

func (d *DB) slackLink(ctx context.Context, op, query string, args ...any) (*SlackLink, error) {
	l, err := scanSlackLink(d.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", op, err)
	}
	return l, nil
}

func (d *DB) PutSlackMessage(ctx context.Context, m *SlackMessage) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO slack_messages (run_id, channel_id, ts, need_key) VALUES (?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET channel_id = excluded.channel_id, ts = excluded.ts, need_key = excluded.need_key`,
		m.RunID, m.Channel, m.TS, m.NeedKey)
	if err != nil {
		return fmt.Errorf("store: put slack message: %w", mapConstraint(err, ErrNotFound))
	}
	return nil
}

func (d *DB) GetSlackMessage(ctx context.Context, run domain.RunID) (*SlackMessage, error) {
	return d.slackMessage(ctx, `run_id = ?`, run)
}

func (d *DB) SlackMessageAt(ctx context.Context, channel, ts string) (*SlackMessage, error) {
	return d.slackMessage(ctx, `channel_id = ? AND ts = ?`, channel, ts)
}

func (d *DB) DeleteSlackMessage(ctx context.Context, run domain.RunID) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM slack_messages WHERE run_id = ?`, run); err != nil {
		return fmt.Errorf("store: delete slack message: %w", err)
	}
	return nil
}

func (d *DB) slackMessage(ctx context.Context, where string, args ...any) (*SlackMessage, error) {
	var m SlackMessage
	err := d.db.QueryRowContext(ctx, `SELECT run_id, channel_id, ts, need_key FROM slack_messages WHERE `+where, args...).
		Scan(&m.RunID, &m.Channel, &m.TS, &m.NeedKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get slack message: %w", err)
	}
	return &m, nil
}
