package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// ServerBlocked reports whether the operator blocked server id.
func (s *Store) ServerBlocked(ctx context.Context, id string) (bool, error) {
	var blocked bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM blocked_servers WHERE id = ?)`, id).
		Scan(&blocked); err != nil {
		return false, fmt.Errorf("edgestore: read block of server %s: %w", id, err)
	}
	return blocked, nil
}

// BlockServer blocks server id and forgets its claim and directory, in one
// transaction. Blocking a blocked id succeeds.
func (s *Store) BlockServer(ctx context.Context, id string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("edgestore: block server: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err = tx.ExecContext(ctx, `INSERT INTO blocked_servers (id, blocked_at) VALUES (?, ?)
		ON CONFLICT (id) DO NOTHING`, id, unix(now)); err != nil {
		return fmt.Errorf("edgestore: block server: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("edgestore: block server: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("edgestore: block server: %w", err)
	}
	return nil
}

// UnblockServer lifts the block on server id, or returns ErrNotFound when
// it is not blocked.
func (s *Store) UnblockServer(ctx context.Context, id string) error {
	err := oneRow(s.db.ExecContext(ctx, `DELETE FROM blocked_servers WHERE id = ?`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: unblock server: %w", err)
	}
	return err
}

// ServerRow is one line of ListServers: a claimed server, with its owner
// or a zero Owner while it has none, or a blocked server id, which has no
// name or owner.
type ServerRow struct {
	ID        string
	Name      string
	Owner     edgeproto.Account
	ClaimedAt time.Time
	BlockedAt time.Time
}

// ListServers returns the claimed servers, then the blocked server ids,
// each ordered by id.
func (s *Store) ListServers(ctx context.Context) ([]ServerRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT 0, s.id, s.name, s.claimed_at,
			COALESCE(a.provider, ''), COALESCE(a.subject, ''), COALESCE(a.email, ''), COALESCE(a.login, '')
		FROM servers s LEFT JOIN accounts a ON a.id = s.owner_account_id
		UNION ALL
		SELECT 1, id, '', blocked_at, '', '', '', '' FROM blocked_servers
		ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("edgestore: list servers: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []ServerRow
	for rows.Next() {
		var r ServerRow
		var blocked bool
		var at int64
		o := &r.Owner
		if err := rows.Scan(&blocked, &r.ID, &r.Name, &at, &o.Provider, &o.Subject, &o.Email, &o.Login); err != nil {
			return nil, fmt.Errorf("edgestore: list servers: %w", err)
		}
		if blocked {
			r.BlockedAt = fromUnix(at)
		} else {
			r.ClaimedAt = fromUnix(at)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: list servers: %w", err)
	}
	return out, nil
}

// BlockAccount blocks the account keyed by provider and subject, whether
// or not it exists yet, and deletes its sessions, devices and pending
// device authorizations, in one transaction. Blocking a blocked account
// succeeds.
func (s *Store) BlockAccount(ctx context.Context, provider, subject string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("edgestore: block account: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err = tx.ExecContext(ctx, `INSERT INTO blocked_accounts (provider, subject, blocked_at) VALUES (?, ?, ?)
		ON CONFLICT (provider, subject) DO NOTHING`, provider, subject, unix(now)); err != nil {
		return fmt.Errorf("edgestore: block account: %w", err)
	}
	for _, table := range []string{"web_sessions", "devices", "device_authorizations"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE account_id =
			(SELECT id FROM accounts WHERE provider = ? AND subject = ?)`, provider, subject); err != nil {
			return fmt.Errorf("edgestore: block account: delete %s: %w", table, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("edgestore: block account: %w", err)
	}
	return nil
}

// UnblockAccount lifts the block on the account keyed by provider and
// subject, or returns ErrNotFound when it is not blocked.
func (s *Store) UnblockAccount(ctx context.Context, provider, subject string) error {
	err := oneRow(s.db.ExecContext(ctx, `DELETE FROM blocked_accounts WHERE provider = ? AND subject = ?`,
		provider, subject))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: unblock account: %w", err)
	}
	return err
}

// AccountRow is one line of ListAccounts. A blocked account that never
// signed in, or was deleted, has only Provider and Subject.
type AccountRow struct {
	Account   edgeproto.Account
	CreatedAt time.Time
	Devices   int
	Servers   int
	Blocked   bool
}

// ListAccounts returns every account, then every blocked account without
// an account row, each ordered by provider and subject.
func (s *Store) ListAccounts(ctx context.Context) ([]AccountRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.provider, a.subject, a.email, a.login, a.name, a.created_at,
			(SELECT COUNT(*) FROM devices d WHERE d.account_id = a.id),
			(SELECT COUNT(*) FROM servers s WHERE s.owner_account_id = a.id),
			EXISTS (SELECT 1 FROM blocked_accounts b WHERE b.provider = a.provider AND b.subject = a.subject),
			0
		FROM accounts a
		UNION ALL
		SELECT b.provider, b.subject, '', '', '', 0, 0, 0, 1, 1 FROM blocked_accounts b
		WHERE NOT EXISTS (SELECT 1 FROM accounts a WHERE a.provider = b.provider AND a.subject = b.subject)
		ORDER BY 10, 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("edgestore: list accounts: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []AccountRow
	for rows.Next() {
		var r AccountRow
		var created int64
		var orphan bool
		a := &r.Account
		if err := rows.Scan(&a.Provider, &a.Subject, &a.Email, &a.Login, &a.Name, &created,
			&r.Devices, &r.Servers, &r.Blocked, &orphan); err != nil {
			return nil, fmt.Errorf("edgestore: list accounts: %w", err)
		}
		if !orphan {
			r.CreatedAt = fromUnix(created)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: list accounts: %w", err)
	}
	return out, nil
}

// DeletedAccount is what DeleteAccount did besides deleting the account
// row with its sessions, devices and device authorizations.
type DeletedAccount struct {
	Account edgeproto.AccountInfo
	Devices int
	// Owned are the ids of the servers the account owned, now ownerless.
	Owned []string
	// Member are the ids of the servers whose directory named the account
	// as a member. Those entries are deleted.
	Member []string
	// Reached are the ids of the servers the relay admitted the account
	// to (RecordReach). A server learns an account's identity only from
	// such a connection, so this covers an identity the server created
	// after it last pushed its directory, which the relay stores at most
	// every few seconds.
	Reached []string
	// Notify are the servers, owned, member or reached, now owed an
	// edgeproto.AccountDeleted.
	Notify []string
}

// DeleteAccount deletes the account keyed by provider and subject in one
// transaction, or returns ErrNotFound: its sessions, devices and device
// authorizations go with it, the servers it owns become ownerless, the
// member entries naming it leave every directory, and each of those
// servers, and each server the relay admitted it to, is owed an
// edgeproto.AccountDeleted (PendingDeletions). A block
// on the account stays. Invitations to its login or email stay: they name
// whoever holds that login or email, and the server that sent them
// decides.
func (s *Store) DeleteAccount(ctx context.Context, provider, subject string, now time.Time) (DeletedAccount, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeletedAccount{}, fmt.Errorf("edgestore: delete account: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var out DeletedAccount
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.provider = ? AND a.subject = ?`,
		provider, subject).Scan(accountDest(&id, &out.Account)...)
	if errors.Is(err, sql.ErrNoRows) {
		return DeletedAccount{}, ErrNotFound
	}
	if err != nil {
		return DeletedAccount{}, fmt.Errorf("edgestore: delete account: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE account_id = ?`, id).Scan(&out.Devices); err != nil {
		return DeletedAccount{}, fmt.Errorf("edgestore: delete account: count devices: %w", err)
	}
	if out.Owned, err = serverIDs(ctx, tx, `UPDATE servers SET owner_type = NULL, owner_account_id = NULL
		WHERE owner_account_id = ? RETURNING id`, id); err != nil {
		return DeletedAccount{}, fmt.Errorf("edgestore: delete account: owned servers: %w", err)
	}
	if out.Member, err = serverIDs(ctx, tx, `DELETE FROM directory_entries
		WHERE kind = 'member' AND provider = ? AND subject = ? RETURNING server_id`, provider, subject); err != nil {
		return DeletedAccount{}, fmt.Errorf("edgestore: delete account: memberships: %w", err)
	}
	if out.Reached, err = serverIDs(ctx, tx, `DELETE FROM account_reach
		WHERE provider = ? AND subject = ? RETURNING server_id`, provider, subject); err != nil {
		return DeletedAccount{}, fmt.Errorf("edgestore: delete account: servers reached: %w", err)
	}
	out.Notify = append(append(slices.Clone(out.Owned), out.Member...), out.Reached...)
	slices.Sort(out.Notify)
	out.Notify = slices.Compact(out.Notify)
	for _, sid := range out.Notify {
		if err = oweDeletion(ctx, tx, sid, provider, subject, now); err != nil {
			return DeletedAccount{}, fmt.Errorf("edgestore: delete account: owe server %s: %w", sid, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id); err != nil {
		return DeletedAccount{}, fmt.Errorf("edgestore: delete account: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return DeletedAccount{}, fmt.Errorf("edgestore: delete account: %w", err)
	}
	return out, nil
}

// serverIDs runs query, which returns server ids, and returns them sorted
// without repeats.
func serverIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close() //nolint:errcheck // the scan error takes precedence
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
