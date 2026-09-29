package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// SignIn records a completed sign-in: it creates the account keyed by
// (provider, subject), with a new public id, or refreshes the email, login
// and name the provider just reported, which are then current as of now.
// An account keeps the public id it was created with. A GitHub login names
// one account at a time, so another account still holding it from before
// a rename loses it: only the current holder matches an invitation for
// that login. It returns the account's row id, or ErrAccountBlocked,
// recording nothing, for a blocked account.
func (s *Store) SignIn(ctx context.Context, a edgeproto.Account, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("edgestore: record sign-in: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var blocked bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM blocked_accounts WHERE provider = ? AND subject = ?)`,
		a.Provider, a.Subject).Scan(&blocked); err != nil {
		return 0, fmt.Errorf("edgestore: record sign-in: %w", err)
	}
	if blocked {
		return 0, ErrAccountBlocked
	}
	if a.Login != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET login = ''
			WHERE provider = ? AND lower(login) = lower(?) AND subject <> ?`,
			a.Provider, a.Login, a.Subject); err != nil {
			return 0, fmt.Errorf("edgestore: record sign-in: release login %s: %w", a.Login, err)
		}
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO accounts (public_id, provider, subject, email, login, name, created_at, identity_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, subject) DO UPDATE SET
			email = excluded.email, login = excluded.login, name = excluded.name, identity_at = excluded.identity_at
		RETURNING id`,
		edgeproto.NewAccountID(), a.Provider, a.Subject, a.Email, a.Login, a.Name, unix(now), unix(now)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("edgestore: record sign-in: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("edgestore: record sign-in: %w", err)
	}
	return id, nil
}

const accountCols = `a.id, a.public_id, a.provider, a.subject, a.email, a.login, a.name, a.identity_at`

// notBlocked is an SQL condition, true when the account whose id is in
// column col is not blocked.
func notBlocked(col string) string {
	return `NOT EXISTS (SELECT 1 FROM accounts ba JOIN blocked_accounts b
		ON b.provider = ba.provider AND b.subject = ba.subject WHERE ba.id = ` + col + `)`
}

func accountDest(id *int64, a *edgeproto.AccountInfo) []any {
	return []any{id, &a.ID, &a.Provider, &a.Subject, &a.Email, &a.Login, &a.Name, identityAt{&a.IdentityAt}}
}

// identityAt scans accounts.identity_at, where 0 is an account from
// before the column: never confirmed.
type identityAt struct{ t *time.Time }

func (d identityAt) Scan(v any) error {
	n, ok := v.(int64)
	if !ok {
		return fmt.Errorf("edgestore: identity_at is %T, want an integer", v)
	}
	*d.t = time.Time{}
	if n != 0 {
		*d.t = fromUnix(n)
	}
	return nil
}

// Session is a signed-in browser at the edge. AccountID is the account's
// row id; Account.ID its public id.
type Session struct {
	ID        string
	AccountID int64
	Account   edgeproto.AccountInfo
}

// CreateSession stores a session under the hash of its cookie token and
// drops expired sessions.
func (s *Store) CreateSession(ctx context.Context, id, tokenHash string, accountID int64, now, expires time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires_at <= ?`, unix(now)); err != nil {
		return fmt.Errorf("edgestore: drop expired sessions: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions (id, token_hash, account_id, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, id, tokenHash, accountID, unix(now), unix(expires)); err != nil {
		return fmt.Errorf("edgestore: create session: %w", err)
	}
	return nil
}

// UseSession returns the unexpired session of an account that is not
// blocked stored under tokenHash, and moves its expiry to expires.
func (s *Store) UseSession(ctx context.Context, tokenHash string, now, expires time.Time) (Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx, `UPDATE web_sessions SET expires_at = ?
		WHERE token_hash = ? AND expires_at > ? AND `+notBlocked("web_sessions.account_id")+` RETURNING id, account_id`,
		unix(expires), tokenHash, unix(now)).Scan(&sess.ID, &sess.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("edgestore: use session: %w", err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.id = ?`,
		sess.AccountID).Scan(accountDest(&id, &sess.Account)...); err != nil {
		return Session{}, fmt.Errorf("edgestore: use session: %w", err)
	}
	return sess, nil
}

// DeleteSession signs a session out.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("edgestore: delete session: %w", err)
	}
	return nil
}
