package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// Server is a claimed server.
type Server struct {
	ID        string
	Name      string
	Owner     edgeproto.Account
	ClaimedAt time.Time
}

// Server returns the claimed server id.
func (s *Store) Server(ctx context.Context, id string) (Server, error) {
	var srv Server
	var ownerID, claimed int64
	err := s.db.QueryRowContext(ctx, `SELECT s.id, s.name, s.claimed_at, `+accountCols+`
		FROM servers s JOIN accounts a ON a.id = s.owner_id WHERE s.id = ?`, id).
		Scan(append([]any{&srv.ID, &srv.Name, &claimed}, accountDest(&ownerID, &srv.Owner)...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	if err != nil {
		return Server{}, fmt.Errorf("edgestore: get server: %w", err)
	}
	srv.ClaimedAt = fromUnix(claimed)
	return srv, nil
}

// ClaimServer records ownerID as the owner of server id, keeping the
// original claim time when the owner is unchanged.
func (s *Store) ClaimServer(ctx context.Context, id, name string, ownerID int64, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO servers (id, name, owner_id, claimed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = excluded.name,
			claimed_at = CASE WHEN owner_id = excluded.owner_id THEN claimed_at ELSE excluded.claimed_at END,
			owner_id = excluded.owner_id`,
		id, name, ownerID, unix(now)); err != nil {
		return fmt.Errorf("edgestore: claim server: %w", err)
	}
	return nil
}

// RenameServer records the name a claimed server announced.
func (s *Store) RenameServer(ctx context.Context, id, name string) error {
	err := oneRow(s.db.ExecContext(ctx, `UPDATE servers SET name = ? WHERE id = ?`, name, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: rename server: %w", err)
	}
	return err
}

// DeleteServer forgets a server with its directory and sign-in codes.
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM directory_entries WHERE server_id = ?`, serverID); err != nil {
		return fmt.Errorf("edgestore: replace directory: %w", err)
	}
	for _, e := range entries {
		var expires int64
		if !e.ExpiresAt.IsZero() {
			expires = unix(e.ExpiresAt)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO directory_entries
			(server_id, kind, provider, subject, login, email, role, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			serverID, e.Kind, e.Provider, e.Subject, e.Login, e.Email, e.Role, expires); err != nil {
			return fmt.Errorf("edgestore: replace directory: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("edgestore: replace directory: %w", err)
	}
	return nil
}

// Access is what the store holds about one account and one claimed server:
// whether the account owns it, and the directory entries that may name the
// account. Entries are candidates: the caller decides with
// edgeproto.DirectoryEntry.Matches.
type Access struct {
	ServerID string
	Name     string
	Owner    bool
	Entries  []edgeproto.DirectoryEntry
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
	return s.access(ctx, a, `(o.provider = ? AND o.subject = ?) OR e.server_id IS NOT NULL`, a.Provider, a.Subject)
}

// access selects candidates in SQL; lower() folds ASCII only, like Matches.
func (s *Store) access(ctx context.Context, a edgeproto.Account, where string, whereArgs ...any) ([]Access, error) {
	args := append([]any{a.Provider, a.Subject, a.Provider, a.Subject, a.Login, a.Email}, whereArgs...)
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.name, o.provider = ? AND o.subject = ?,
			e.kind, e.provider, e.subject, e.login, e.email, e.role, e.expires_at
		FROM servers s
		JOIN accounts o ON o.id = s.owner_id
		LEFT JOIN directory_entries e ON e.server_id = s.id AND (
			(e.kind = 'member' AND e.provider = ? AND e.subject = ?)
			OR (e.kind = 'invitation' AND (
				(e.login <> '' AND lower(e.login) = lower(?)) OR (e.email <> '' AND lower(e.email) = lower(?)))))
		WHERE `+where+`
		ORDER BY s.name, s.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("edgestore: read access: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []Access
	for rows.Next() {
		var acc Access
		var kind, provider, subject, login, email, role sql.NullString
		var expires sql.NullInt64
		if err := rows.Scan(&acc.ServerID, &acc.Name, &acc.Owner,
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

// WebCode is a one-time web sign-in code, bound to a server, the account
// and browser session that asked for it, and the server's PKCE challenge.
type WebCode struct {
	CodeHash  string
	ServerID  string
	AccountID int64
	Account   edgeproto.Account
	SessionID string
	Challenge string
	ExpiresAt time.Time
}

// CreateWebCode stores a code and drops expired ones.
func (s *Store) CreateWebCode(ctx context.Context, c WebCode, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM web_codes WHERE expires_at <= ?`, unix(now)); err != nil {
		return fmt.Errorf("edgestore: drop expired web codes: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO web_codes
		(code_hash, server_id, account_id, session_id, challenge, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.CodeHash, c.ServerID, c.AccountID, c.SessionID, c.Challenge, unix(c.ExpiresAt)); err != nil {
		return fmt.Errorf("edgestore: create web code: %w", err)
	}
	return nil
}

// TakeWebCode deletes and returns the code under codeHash, expired or not:
// whoever presents a code uses it up.
func (s *Store) TakeWebCode(ctx context.Context, codeHash string) (WebCode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WebCode{}, fmt.Errorf("edgestore: take web code: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	c := WebCode{CodeHash: codeHash}
	var expires int64
	err = tx.QueryRowContext(ctx, `DELETE FROM web_codes WHERE code_hash = ?
		RETURNING server_id, account_id, session_id, challenge, expires_at`, codeHash).
		Scan(&c.ServerID, &c.AccountID, &c.SessionID, &c.Challenge, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return WebCode{}, ErrNotFound
	}
	if err != nil {
		return WebCode{}, fmt.Errorf("edgestore: take web code: %w", err)
	}
	c.ExpiresAt = fromUnix(expires)
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.id = ?`,
		c.AccountID).Scan(accountDest(&id, &c.Account)...); err != nil {
		return WebCode{}, fmt.Errorf("edgestore: take web code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return WebCode{}, fmt.Errorf("edgestore: take web code: %w", err)
	}
	return c, nil
}
