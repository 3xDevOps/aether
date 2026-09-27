package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

const browserSessionCols = `id, device_id, credential, created_at, last_seen_at`

func scanBrowserSession(row interface{ Scan(...any) error }) (*domain.BrowserSession, error) {
	var (
		s         domain.BrowserSession
		createdAt int64
		lastSeen  *int64
	)
	if err := row.Scan(&s.ID, &s.Device, &s.Credential, &createdAt, &lastSeen); err != nil {
		return nil, err
	}
	s.CreatedAt = decodeTime(createdAt)
	s.LastSeenAt = decodeTimePtr(lastSeen)
	return &s, nil
}

func (d *DB) CreateBrowserSession(ctx context.Context, s *domain.BrowserSession) error {
	if s.Device == "" || s.Credential == "" {
		return errors.New("store: create browser session: device and credential are required")
	}
	id, ts, err := prepareCreate(s.CreatedAt)
	if err != nil {
		return err
	}
	createdAt, err := encodeTime(ts)
	if err != nil {
		return fmt.Errorf("store: create browser session: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO browser_sessions (id, device_id, credential, created_at) VALUES (?, ?, ?, ?)`,
		id, s.Device, s.Credential, createdAt,
	); err != nil {
		return fmt.Errorf("store: create browser session: %w", mapConstraint(err, ErrNotFound))
	}
	s.ID, s.CreatedAt, s.LastSeenAt = domain.BrowserSessionID(id), ts, nil
	return nil
}

func (d *DB) getBrowserSession(ctx context.Context, op, where string, arg any) (*domain.BrowserSession, error) {
	s, err := scanBrowserSession(d.db.QueryRowContext(ctx,
		`SELECT `+browserSessionCols+` FROM browser_sessions WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", op, err)
	}
	return s, nil
}

func (d *DB) GetBrowserSession(ctx context.Context, id domain.BrowserSessionID) (*domain.BrowserSession, error) {
	return d.getBrowserSession(ctx, "get browser session", `id = ?`, id)
}

func (d *DB) GetBrowserSessionByCredential(ctx context.Context, credential string) (*domain.BrowserSession, error) {
	return d.getBrowserSession(ctx, "get browser session by credential", `credential = ?`, credential)
}

func (d *DB) DeleteBrowserSession(ctx context.Context, id domain.BrowserSessionID) error {
	return d.execDelete(ctx, "delete browser session", `DELETE FROM browser_sessions WHERE id = ?`, id)
}

func (d *DB) TouchBrowserSession(ctx context.Context, id domain.BrowserSessionID, at time.Time) error {
	seen, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: touch browser session: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin touch browser session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = notFoundOnZeroRows(tx.ExecContext(ctx,
		`UPDATE browser_sessions SET last_seen_at = ? WHERE id = ?`, seen, id)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("store: touch browser session: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE member_devices SET last_seen_at = ?
		 WHERE id = (SELECT device_id FROM browser_sessions WHERE id = ?)`, seen, id); err != nil {
		return fmt.Errorf("store: touch browser session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit touch browser session: %w", err)
	}
	return nil
}
