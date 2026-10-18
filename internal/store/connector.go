package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// ErrRefreshReused is returned for a refresh token its connector rotated
// away: the connector has been deleted.
var ErrRefreshReused = errors.New("store: refresh token reused")

// Connector is a ChatGPT or Claude connector a member authorized: an
// OAuth client whose tokens act as the member. Device is the edge device
// its pairing code was minted on, empty for one minted over a member SSH
// key or a tailnet connection. Resource is the connector URL its tokens
// are good for. The expiries are those of its current tokens.
type Connector struct {
	ID               string
	Member           domain.MemberID
	Device           domain.DeviceID
	ClientID         string
	ClientName       string
	RedirectHost     string
	Resource         string
	CreatedAt        time.Time
	LastUsedAt       *time.Time
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

// ConnectorTokens are a connector's tokens as hex SHA-256 hashes, and when
// each lapses.
type ConnectorTokens struct {
	Access           string
	AccessExpiresAt  time.Time
	Refresh          string
	RefreshExpiresAt time.Time
}

// ConnectorStore persists connectors. Removing a member or the device a
// connector was minted on removes the connector.
type ConnectorStore interface {
	// CreateConnector stores c with its first tokens and assigns c.ID. It
	// first deletes every connector whose refresh token lapsed by
	// c.CreatedAt.
	CreateConnector(ctx context.Context, c *Connector, tokens ConnectorTokens) error
	// ConnectorByAccess returns the connector whose access token hashes to
	// hash, expired or not.
	ConnectorByAccess(ctx context.Context, hash string) (*Connector, error)
	// ConnectorByRefresh returns the connector whose refresh token hashes
	// to hash, expired or not. A hash the connector rotated away, unlapsed
	// at now, deletes the connector and returns ErrRefreshReused.
	ConnectorByRefresh(ctx context.Context, hash string, now time.Time) (*Connector, error)
	// RotateConnector replaces the tokens of connector id with next while
	// its refresh token still hashes to prev, and remembers prev until it
	// would have lapsed. When prev was rotated away meanwhile it deletes
	// the connector and returns ErrRefreshReused, as ConnectorByRefresh
	// does.
	RotateConnector(ctx context.Context, id, prev string, next ConnectorTokens, now time.Time) error
	TouchConnector(ctx context.Context, id string, at time.Time) error
	GetConnector(ctx context.Context, id string) (*Connector, error)
	// ListConnectors returns member's connectors whose refresh token has
	// not lapsed at now, oldest first.
	ListConnectors(ctx context.Context, member domain.MemberID, now time.Time) ([]*Connector, error)
	DeleteConnector(ctx context.Context, id string) error
}

var _ ConnectorStore = (*DB)(nil)

const connectorCols = `id, member_id, device_id, client_id, client_name, redirect_host, resource,
	created_at, last_used_at, access_expires_at, refresh_expires_at`

func scanConnector(row interface{ Scan(...any) error }) (*Connector, error) {
	var (
		c                                        Connector
		device                                   sql.NullString
		createdAt, accessExpires, refreshExpires int64
		lastUsed                                 *int64
	)
	if err := row.Scan(&c.ID, &c.Member, &device, &c.ClientID, &c.ClientName, &c.RedirectHost, &c.Resource,
		&createdAt, &lastUsed, &accessExpires, &refreshExpires); err != nil {
		return nil, err
	}
	c.Device = domain.DeviceID(device.String)
	c.CreatedAt, c.LastUsedAt = decodeTime(createdAt), decodeTimePtr(lastUsed)
	c.AccessExpiresAt, c.RefreshExpiresAt = decodeTime(accessExpires), decodeTime(refreshExpires)
	return &c, nil
}

func (d *DB) CreateConnector(ctx context.Context, c *Connector, tokens ConnectorTokens) error {
	id, err := newID("con")
	if err != nil {
		return err
	}
	created, err := encodeTime(c.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: create connector: %w", err)
	}
	accessExpires, refreshExpires, err := encodeTokenExpiries(tokens)
	if err != nil {
		return fmt.Errorf("store: create connector: %w", err)
	}
	var device any
	if c.Device != "" {
		device = c.Device
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin create connector: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM connectors WHERE refresh_expires_at <= ?`, created); err != nil {
		return fmt.Errorf("store: delete lapsed connectors: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO connectors (id, member_id, device_id, client_id, client_name, redirect_host, resource, created_at,
		   access_hash, access_expires_at, refresh_hash, refresh_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, c.Member, device, c.ClientID, c.ClientName, c.RedirectHost, c.Resource, created,
		tokens.Access, accessExpires, tokens.Refresh, refreshExpires); err != nil {
		return fmt.Errorf("store: create connector: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit create connector: %w", err)
	}
	c.ID, c.AccessExpiresAt, c.RefreshExpiresAt = id, tokens.AccessExpiresAt, tokens.RefreshExpiresAt
	return nil
}

func encodeTokenExpiries(t ConnectorTokens) (access, refresh int64, err error) {
	if access, err = encodeTime(t.AccessExpiresAt); err != nil {
		return 0, 0, err
	}
	if refresh, err = encodeTime(t.RefreshExpiresAt); err != nil {
		return 0, 0, err
	}
	return access, refresh, nil
}

func (d *DB) ConnectorByAccess(ctx context.Context, hash string) (*Connector, error) {
	return d.getConnector(ctx, "get connector by access token", `access_hash = ?`, hash)
}

func (d *DB) ConnectorByRefresh(ctx context.Context, hash string, now time.Time) (*Connector, error) {
	c, err := d.getConnector(ctx, "get connector by refresh token", `refresh_hash = ?`, hash)
	if !errors.Is(err, ErrNotFound) {
		return c, err
	}
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: get connector by refresh token: %w", err)
	}
	return nil, revokeReused(ctx, d.db, hash, at)
}

// revokeReused deletes the connector that rotated hash away, unlapsed at
// at: ErrRefreshReused when there was one, ErrNotFound otherwise.
func revokeReused(ctx context.Context, q execer, hash string, at int64) error {
	err := notFoundOnZeroRows(q.ExecContext(ctx,
		`DELETE FROM connectors WHERE id =
		 (SELECT connector_id FROM connector_rotated_tokens WHERE refresh_hash = ? AND expires_at > ?)`, hash, at))
	switch {
	case err == nil:
		return ErrRefreshReused
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	}
	return fmt.Errorf("store: revoke connector: %w", err)
}

func (d *DB) RotateConnector(ctx context.Context, id, prev string, next ConnectorTokens, now time.Time) error {
	at, err := encodeTime(now)
	if err != nil {
		return fmt.Errorf("store: rotate connector: %w", err)
	}
	accessExpires, refreshExpires, err := encodeTokenExpiries(next)
	if err != nil {
		return fmt.Errorf("store: rotate connector: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin rotate connector: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	err = notFoundOnZeroRows(tx.ExecContext(ctx,
		`INSERT INTO connector_rotated_tokens (refresh_hash, connector_id, expires_at)
		 SELECT refresh_hash, id, refresh_expires_at FROM connectors WHERE id = ? AND refresh_hash = ?`, id, prev))
	if errors.Is(err, ErrNotFound) {
		if err = revokeReused(ctx, tx, prev, at); errors.Is(err, ErrRefreshReused) {
			if cerr := tx.Commit(); cerr != nil {
				return fmt.Errorf("store: commit revoke connector: %w", cerr)
			}
		}
		return err
	}
	if err != nil {
		return fmt.Errorf("store: rotate connector: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE connectors SET access_hash = ?, access_expires_at = ?, refresh_hash = ?, refresh_expires_at = ? WHERE id = ?`,
		next.Access, accessExpires, next.Refresh, refreshExpires, id); err != nil {
		return fmt.Errorf("store: rotate connector: %w", mapConstraint(err, ErrNotFound))
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM connector_rotated_tokens WHERE connector_id = ? AND expires_at <= ?`, id, at); err != nil {
		return fmt.Errorf("store: delete lapsed rotated tokens: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit rotate connector: %w", err)
	}
	return nil
}

func (d *DB) TouchConnector(ctx context.Context, id string, at time.Time) error {
	ts, err := encodeTime(at)
	if err != nil {
		return fmt.Errorf("store: touch connector: %w", err)
	}
	if err := notFoundOnZeroRows(d.db.ExecContext(ctx, `UPDATE connectors SET last_used_at = ? WHERE id = ?`, ts, id)); err != nil {
		return fmt.Errorf("store: touch connector %s: %w", id, err)
	}
	return nil
}

func (d *DB) GetConnector(ctx context.Context, id string) (*Connector, error) {
	return d.getConnector(ctx, "get connector", `id = ?`, id)
}

func (d *DB) getConnector(ctx context.Context, op, where string, arg any) (*Connector, error) {
	c, err := scanConnector(d.db.QueryRowContext(ctx, `SELECT `+connectorCols+` FROM connectors WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", op, err)
	}
	return c, nil
}

func (d *DB) ListConnectors(ctx context.Context, member domain.MemberID, now time.Time) ([]*Connector, error) {
	at, err := encodeTime(now)
	if err != nil {
		return nil, fmt.Errorf("store: list connectors: %w", err)
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+connectorCols+` FROM connectors WHERE member_id = ? AND refresh_expires_at > ? ORDER BY created_at, id`,
		member, at)
	if err != nil {
		return nil, fmt.Errorf("store: list connectors: %w", err)
	}
	return collect(rows, scanConnector)
}

func (d *DB) DeleteConnector(ctx context.Context, id string) error {
	err := notFoundOnZeroRows(d.db.ExecContext(ctx, `DELETE FROM connectors WHERE id = ?`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: delete connector: %w", err)
	}
	return err
}
