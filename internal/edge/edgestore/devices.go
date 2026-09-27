package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// Device authorization states.
const (
	AuthPending  = "pending"
	AuthApproved = "approved"
	AuthDenied   = "denied"
)

// DeviceAuth is one device authorization request: a client install waiting
// for a person to confirm its user code.
type DeviceAuth struct {
	CodeHash     string
	UserCodeHash string
	Label        string
	Key          string
	Status       string
	AccountID    int64
	CreatedAt    time.Time
	ExpiresAt    time.Time
	LastPollAt   time.Time
}

// CreateDeviceAuth stores a pending authorization and drops expired ones.
func (s *Store) CreateDeviceAuth(ctx context.Context, a DeviceAuth) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM device_authorizations WHERE expires_at <= ?`,
		unix(a.CreatedAt)); err != nil {
		return fmt.Errorf("edgestore: drop expired device authorizations: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO device_authorizations
		(code_hash, user_code_hash, label, public_key, status, created_at, expires_at, last_poll_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
		a.CodeHash, a.UserCodeHash, a.Label, a.Key, AuthPending, unix(a.CreatedAt), unix(a.ExpiresAt)); err != nil {
		return fmt.Errorf("edgestore: create device authorization: %w", conflict(err))
	}
	return nil
}

const deviceAuthCols = `code_hash, user_code_hash, label, public_key, status, COALESCE(account_id, 0),
	created_at, expires_at, last_poll_at`

func scanDeviceAuth(row *sql.Row) (DeviceAuth, error) {
	var a DeviceAuth
	var created, expires, polled int64
	err := row.Scan(&a.CodeHash, &a.UserCodeHash, &a.Label, &a.Key, &a.Status, &a.AccountID,
		&created, &expires, &polled)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceAuth{}, ErrNotFound
	}
	if err != nil {
		return DeviceAuth{}, err
	}
	a.CreatedAt, a.ExpiresAt = fromUnix(created), fromUnix(expires)
	if polled != 0 {
		a.LastPollAt = fromUnix(polled)
	}
	return a, nil
}

// PendingDeviceAuth returns the pending, unexpired authorization whose user
// code hashes to userCodeHash.
func (s *Store) PendingDeviceAuth(ctx context.Context, userCodeHash string, now time.Time) (DeviceAuth, error) {
	a, err := scanDeviceAuth(s.db.QueryRowContext(ctx, `SELECT `+deviceAuthCols+`
		FROM device_authorizations WHERE user_code_hash = ? AND status = ? AND expires_at > ?`,
		userCodeHash, AuthPending, unix(now)))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: find device authorization: %w", err)
	}
	return a, err
}

// DecideDeviceAuth approves (for accountID) or denies the pending,
// unexpired authorization whose user code hashes to userCodeHash.
func (s *Store) DecideDeviceAuth(ctx context.Context, userCodeHash string, accountID int64, approve bool, now time.Time) error {
	status, account := AuthDenied, sql.NullInt64{}
	if approve {
		status, account = AuthApproved, sql.NullInt64{Int64: accountID, Valid: true}
	}
	err := oneRow(s.db.ExecContext(ctx, `UPDATE device_authorizations SET status = ?, account_id = ?
		WHERE user_code_hash = ? AND status = ? AND expires_at > ?`,
		status, account, userCodeHash, AuthPending, unix(now)))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: decide device authorization: %w", err)
	}
	return err
}

// PollDeviceAuth records a poll at now and returns the authorization as it
// was before, so LastPollAt is the previous poll.
func (s *Store) PollDeviceAuth(ctx context.Context, codeHash string, now time.Time) (DeviceAuth, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("edgestore: poll device authorization: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	a, err := scanDeviceAuth(tx.QueryRowContext(ctx, `SELECT `+deviceAuthCols+`
		FROM device_authorizations WHERE code_hash = ?`, codeHash))
	if errors.Is(err, ErrNotFound) {
		return DeviceAuth{}, err
	}
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("edgestore: poll device authorization: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE device_authorizations SET last_poll_at = ? WHERE code_hash = ?`,
		unix(now), codeHash); err != nil {
		return DeviceAuth{}, fmt.Errorf("edgestore: poll device authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DeviceAuth{}, fmt.Errorf("edgestore: poll device authorization: %w", err)
	}
	return a, nil
}

// DeleteDeviceAuth drops an authorization that ended denied or expired.
func (s *Store) DeleteDeviceAuth(ctx context.Context, codeHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM device_authorizations WHERE code_hash = ?`, codeHash); err != nil {
		return fmt.Errorf("edgestore: delete device authorization: %w", err)
	}
	return nil
}

// Device is a client install holding a device token.
type Device struct {
	ID         string
	TokenHash  string
	AccountID  int64
	Account    edgeproto.Account
	Key        string
	Label      string
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// RedeemDeviceAuth consumes the approved, unexpired authorization under
// codeHash and creates d from it, in one transaction, so an authorization
// yields at most one device. d's account, key and label come from the
// authorization.
func (s *Store) RedeemDeviceAuth(ctx context.Context, codeHash string, d Device, now time.Time) (Device, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, fmt.Errorf("edgestore: redeem device authorization: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	err = tx.QueryRowContext(ctx, `DELETE FROM device_authorizations
		WHERE code_hash = ? AND status = ? AND expires_at > ?
		RETURNING account_id, public_key, label`, codeHash, AuthApproved, unix(now)).
		Scan(&d.AccountID, &d.Key, &d.Label)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("edgestore: redeem device authorization: %w", err)
	}
	d.CreatedAt, d.LastUsedAt = now, now
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, d.ID, d.TokenHash, d.AccountID, d.Key, d.Label, unix(now), unix(now)); err != nil {
		return Device{}, fmt.Errorf("edgestore: create device: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.id = ?`,
		d.AccountID).Scan(accountDest(&id, &d.Account)...); err != nil {
		return Device{}, fmt.Errorf("edgestore: redeem device authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Device{}, fmt.Errorf("edgestore: redeem device authorization: %w", err)
	}
	return d, nil
}

const deviceCols = `d.id, d.token_hash, d.public_key, d.label, d.created_at, d.last_used_at, ` + accountCols

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var created, used int64
	dest := append([]any{&d.ID, &d.TokenHash, &d.Key, &d.Label, &created, &used}, accountDest(&d.AccountID, &d.Account)...)
	if err := row.Scan(dest...); err != nil {
		return Device{}, err
	}
	d.CreatedAt, d.LastUsedAt = fromUnix(created), fromUnix(used)
	return d, nil
}

// UseDevice returns the device holding the token that hashes to tokenHash
// and records its use at now.
func (s *Store) UseDevice(ctx context.Context, tokenHash string, now time.Time) (Device, error) {
	err := oneRow(s.db.ExecContext(ctx, `UPDATE devices SET last_used_at = ? WHERE token_hash = ?`, unix(now), tokenHash))
	if errors.Is(err, ErrNotFound) {
		return Device{}, err
	}
	if err != nil {
		return Device{}, fmt.Errorf("edgestore: use device: %w", err)
	}
	d, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+`
		FROM devices d JOIN accounts a ON a.id = d.account_id WHERE d.token_hash = ?`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("edgestore: use device: %w", err)
	}
	return d, nil
}

// Devices lists an account's devices, newest first.
func (s *Store) Devices(ctx context.Context, accountID int64) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+`
		FROM devices d JOIN accounts a ON a.id = d.account_id
		WHERE d.account_id = ? ORDER BY d.created_at DESC, d.id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("edgestore: list devices: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("edgestore: list devices: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: list devices: %w", err)
	}
	return out, nil
}

// DeleteDevice revokes one of accountID's devices by deleting it, token
// hash included.
func (s *Store) DeleteDevice(ctx context.Context, accountID int64, deviceID string) error {
	err := oneRow(s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ? AND account_id = ?`, deviceID, accountID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: delete device: %w", err)
	}
	return err
}
