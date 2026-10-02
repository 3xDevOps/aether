package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// Server is a claimed server: one that an account claimed at this edge.
// Owner is nil while it has none: its owner's account was deleted or it
// reported itself ownerless.
type Server struct {
	ID           string
	Name         string
	Kind         edgeproto.ServerKind
	AccessPolicy edgeproto.AccessPolicy
	Owner        *edgeproto.AccountInfo
	ClaimedAt    time.Time
}

// Server returns the claimed server id.
func (s *Store) Server(ctx context.Context, id string) (Server, error) {
	var srv Server
	var claimed int64
	var ownerID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, kind, access_policy, claimed_at, owner_account_id
		FROM servers WHERE id = ?`, id).Scan(&srv.ID, &srv.Name, &srv.Kind, &srv.AccessPolicy, &claimed, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	if err != nil {
		return Server{}, fmt.Errorf("edgestore: get server: %w", err)
	}
	srv.ClaimedAt = fromUnix(claimed)
	if ownerID.Valid {
		srv.Owner = new(edgeproto.AccountInfo)
		var rowID int64
		if err := s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.id = ?`, ownerID.Int64).
			Scan(accountDest(&rowID, srv.Owner)...); err != nil {
			return Server{}, fmt.Errorf("edgestore: get server: owner: %w", err)
		}
	}
	return srv, nil
}

// EnrollServer records the name and access policy a claimed server
// announced when it enrolled, and reports whether it is claimed. An
// unclaimed server records nothing.
func (s *Store) EnrollServer(ctx context.Context, id, name string, policy edgeproto.AccessPolicy) (claimed bool, err error) {
	err = oneRow(s.db.ExecContext(ctx, `UPDATE servers SET name = ?, access_policy = ? WHERE id = ?`, name, policy, id))
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("edgestore: enroll server: %w", err)
	}
	return true, nil
}

// ownerAccount returns the row id of the existing account a principal
// names, ErrNoAccount when it has none, or ErrAccountBlocked.
func ownerAccount(ctx context.Context, tx *sql.Tx, owner edgeproto.Principal) (int64, error) {
	if err := owner.Validate(); err != nil {
		return 0, err
	}
	var id int64
	var blocked bool
	err := tx.QueryRowContext(ctx, `SELECT a.id,
			EXISTS (SELECT 1 FROM blocked_accounts b WHERE b.provider = a.provider AND b.subject = a.subject)
		FROM accounts a WHERE a.provider = ? AND a.subject = ?`, owner.Provider, owner.Subject).Scan(&id, &blocked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, ErrNoAccount
	case err != nil:
		return 0, err
	case blocked:
		return 0, ErrAccountBlocked
	}
	return id, nil
}

// RecordClaim records owner, an existing account, as the owner of server
// id, which has none: a server never claimed here, created with name and
// policy as a self-hosted server, or a claimed server that is ownerless.
// It fails, recording nothing, with ErrServerBlocked, ErrNoAccount,
// ErrAccountBlocked, or ErrHasOwner.
func (s *Store) RecordClaim(ctx context.Context, id, name string, policy edgeproto.AccessPolicy,
	owner edgeproto.Principal, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("edgestore: record claim: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var blocked bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM blocked_servers WHERE id = ?)`, id).
		Scan(&blocked); err != nil {
		return fmt.Errorf("edgestore: record claim: %w", err)
	}
	if blocked {
		return ErrServerBlocked
	}
	ownerID, err := ownerAccount(ctx, tx, owner)
	if err != nil {
		return fmt.Errorf("edgestore: record claim: %w", err)
	}
	err = oneRow(tx.ExecContext(ctx, `INSERT INTO servers
			(id, name, kind, access_policy, owner_type, owner_account_id, claimed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = excluded.name, access_policy = excluded.access_policy, owner_type = excluded.owner_type,
			owner_account_id = excluded.owner_account_id, claimed_at = excluded.claimed_at
		WHERE servers.owner_account_id IS NULL`,
		id, name, edgeproto.ServerSelfHosted, policy, owner.Type, ownerID, unix(now)))
	if errors.Is(err, ErrNotFound) {
		return ErrHasOwner
	}
	if err != nil {
		return fmt.Errorf("edgestore: record claim: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("edgestore: record claim: %w", err)
	}
	return nil
}

// TransferOwner records owner, an existing account, as the owner of the
// claimed server id. It fails with ErrNotFound for a server that is not
// claimed, ErrNoAccount or ErrAccountBlocked.
func (s *Store) TransferOwner(ctx context.Context, id string, owner edgeproto.Principal) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("edgestore: transfer owner: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	ownerID, err := ownerAccount(ctx, tx, owner)
	if err != nil {
		return fmt.Errorf("edgestore: transfer owner: %w", err)
	}
	err = oneRow(tx.ExecContext(ctx, `UPDATE servers SET owner_type = ?, owner_account_id = ? WHERE id = ?`,
		owner.Type, ownerID, id))
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("edgestore: transfer owner: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("edgestore: transfer owner: %w", err)
	}
	return nil
}

// DropOwner records that the claimed server id has no owner, or returns
// ErrNotFound for a server that is not claimed.
func (s *Store) DropOwner(ctx context.Context, id string) error {
	err := oneRow(s.db.ExecContext(ctx, `UPDATE servers SET owner_type = NULL, owner_account_id = NULL WHERE id = ?`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: drop owner: %w", err)
	}
	return err
}

// ServerOwned reports whether server id has an owner. A server that is
// not claimed has none.
func (s *Store) ServerOwned(ctx context.Context, id string) (bool, error) {
	var owned bool
	err := s.db.QueryRowContext(ctx, `SELECT owner_account_id IS NOT NULL FROM servers WHERE id = ?`, id).Scan(&owned)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("edgestore: read owner of server %s: %w", id, err)
	}
	return owned, nil
}

// DeleteServer forgets a server with its directory.
func (s *Store) DeleteServer(ctx context.Context, id string) error {
	err := oneRow(s.db.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: delete server: %w", err)
	}
	return err
}

// ReplaceDirectory replaces a claimed server's directory with entries in
// one transaction. The caller validates the entries.
func (s *Store) ReplaceDirectory(ctx context.Context, serverID string, entries []edgeproto.DirectoryEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("edgestore: replace directory: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE id = ?`, serverID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("edgestore: replace directory: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM directory_entries WHERE server_id = ?`, serverID); err != nil {
		return fmt.Errorf("edgestore: replace directory: %w", err)
	}
	// One prepared insert shortens the time this holds the database's
	// write lock, which every sign-in and connection also needs.
	insert, err := tx.PrepareContext(ctx, `INSERT INTO directory_entries
		(server_id, kind, provider, subject, login, email, role, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("edgestore: replace directory: %w", err)
	}
	defer insert.Close() //nolint:errcheck // closed with the transaction
	for _, e := range entries {
		var expires int64
		if !e.ExpiresAt.IsZero() {
			expires = unix(e.ExpiresAt)
		}
		if _, err := insert.ExecContext(ctx, serverID, e.Kind, e.Provider, e.Subject, e.Login, e.Email, e.Role, expires); err != nil {
			return fmt.Errorf("edgestore: replace directory: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("edgestore: replace directory: %w", err)
	}
	return nil
}

// Access is what the store holds about one account and one claimed server:
// the server's kind and announced policy, whether the account owns it, and
// the directory entries that may name the account. Entries are
// candidates: the caller decides with edgeproto.DirectoryEntry.Matches.
type Access struct {
	ServerID     string
	Name         string
	Kind         edgeproto.ServerKind
	AccessPolicy edgeproto.AccessPolicy
	Owner        bool
	Entries      []edgeproto.DirectoryEntry
}

// ServerAccess returns a's access to the claimed server id.
func (s *Store) ServerAccess(ctx context.Context, id string, a edgeproto.Account) (Access, error) {
	out, err := s.access(ctx, a, `s.id = ?`, id)
	if err != nil {
		return Access{}, err
	}
	if len(out) == 0 {
		return Access{}, ErrNotFound
	}
	return out[0], nil
}

// AccountAccess returns every claimed server a owns or has a candidate
// directory entry on, ordered by server name.
func (s *Store) AccountAccess(ctx context.Context, a edgeproto.Account) ([]Access, error) {
	return s.access(ctx, a, accountServers, a.Provider, a.Subject, a.Provider, a.Subject, a.Login, a.Email)
}

// accountServers picks AccountAccess's servers through the owner and
// directory indexes. Every request that lists servers runs it, so its cost
// must follow the account's own servers, not every directory at the edge.
const accountServers = `s.id IN (
	SELECT id FROM servers WHERE owner_account_id = (SELECT id FROM accounts WHERE provider = ? AND subject = ?)
	UNION SELECT server_id FROM directory_entries WHERE kind = 'member' AND provider = ? AND subject = ?
	UNION SELECT server_id FROM directory_entries WHERE kind = 'invitation' AND login <> '' AND lower(login) = lower(?)
	UNION SELECT server_id FROM directory_entries WHERE kind = 'invitation' AND email <> '' AND lower(email) = lower(?))`

// accessQuery selects candidates in SQL; lower() folds ASCII only, like
// Matches.
func accessQuery(where string) string {
	return `SELECT s.id, s.name, s.kind, s.access_policy,
			COALESCE(o.provider = ? AND o.subject = ?, 0),
			e.kind, e.provider, e.subject, e.login, e.email, e.role, e.expires_at
		FROM servers s
		LEFT JOIN accounts o ON o.id = s.owner_account_id
		LEFT JOIN directory_entries e ON e.server_id = s.id AND (
			(e.kind = 'member' AND e.provider = ? AND e.subject = ?)
			OR (e.kind = 'invitation' AND (
				(e.login <> '' AND lower(e.login) = lower(?)) OR (e.email <> '' AND lower(e.email) = lower(?)))))
		WHERE ` + where + `
		ORDER BY s.name, s.id`
}

func (s *Store) access(ctx context.Context, a edgeproto.Account, where string, whereArgs ...any) ([]Access, error) {
	args := append([]any{a.Provider, a.Subject, a.Provider, a.Subject, a.Login, a.Email}, whereArgs...)
	rows, err := s.db.QueryContext(ctx, accessQuery(where), args...)
	if err != nil {
		return nil, fmt.Errorf("edgestore: read access: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []Access
	for rows.Next() {
		var acc Access
		var kind, provider, subject, login, email, role sql.NullString
		var expires sql.NullInt64
		if err := rows.Scan(&acc.ServerID, &acc.Name, &acc.Kind, &acc.AccessPolicy, &acc.Owner,
			&kind, &provider, &subject, &login, &email, &role, &expires); err != nil {
			return nil, fmt.Errorf("edgestore: read access: %w", err)
		}
		if len(out) == 0 || out[len(out)-1].ServerID != acc.ServerID {
			out = append(out, acc)
		}
		if !kind.Valid {
			continue
		}
		e := edgeproto.DirectoryEntry{Kind: kind.String, Provider: provider.String, Subject: subject.String,
			Login: login.String, Email: email.String, Role: role.String}
		if expires.Int64 != 0 {
			e.ExpiresAt = fromUnix(expires.Int64)
		}
		last := &out[len(out)-1]
		last.Entries = append(last.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: read access: %w", err)
	}
	return out, nil
}
