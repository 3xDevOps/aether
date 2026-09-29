// Package edgestore is the edge's SQLite store: accounts, browser sessions,
// device authorizations, devices, claimed servers with their owners and
// directories, the account deletions owed to servers, the relay's monthly
// egress, and the servers and accounts the operator blocked. It stores
// bearer secrets only as edgeproto.HashToken values.
package edgestore

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	sqlite "modernc.org/sqlite" // pure-Go sqlite driver
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrNotFound reports a missing, expired or already used row.
var ErrNotFound = errors.New("edgestore: not found")

// ErrConflict reports a row whose unique key is taken.
var ErrConflict = errors.New("edgestore: already exists")

// ErrServerBlocked and ErrAccountBlocked report a server id or an account
// the edge's operator blocked.
var (
	ErrServerBlocked  = errors.New("edgestore: server is blocked")
	ErrAccountBlocked = errors.New("edgestore: account is blocked")
)

// ErrNoAccount reports an owner that has no account at this edge, and
// ErrHasOwner a claim of a server that has an owner.
var (
	ErrNoAccount = errors.New("edgestore: no account at this edge")
	ErrHasOwner  = errors.New("edgestore: server has an owner")
)

// Store is the edge database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the database at path and applies
// pending migrations. The file is created 0600 before SQLite sees it,
// because SQLite copies its mode onto the -wal and -shm files.
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("edgestore: create %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return nil, fmt.Errorf("edgestore: create %s: %w", path, err)
	}
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("edgestore: open %s: %w", path, err)
	}
	if err := migrate(db, migrations); err != nil {
		db.Close() //nolint:errcheck // the migration error takes precedence
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migration is one step of the schema history, applied in its own
// transaction.
type migration func(tx *sql.Tx) error

func stmt(q string) migration {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(q)
		return err
	}
}

// migrations is the append-only schema history; entry i is version i+1.
var migrations = []migration{stmt(`
CREATE TABLE accounts (
	id          INTEGER PRIMARY KEY,
	public_id   TEXT NOT NULL UNIQUE,
	provider    TEXT NOT NULL,
	subject     TEXT NOT NULL,
	email       TEXT NOT NULL,
	login       TEXT NOT NULL,
	name        TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	identity_at INTEGER NOT NULL,
	UNIQUE (provider, subject)
);

CREATE TABLE web_sessions (
	id         TEXT PRIMARY KEY,
	token_hash TEXT NOT NULL UNIQUE,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX web_sessions_expiry ON web_sessions(expires_at);

CREATE TABLE device_authorizations (
	code_hash      TEXT PRIMARY KEY,
	user_code_hash TEXT NOT NULL UNIQUE,
	label          TEXT NOT NULL,
	public_key     TEXT NOT NULL,
	client_addr    TEXT NOT NULL,
	status         TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied')),
	account_id     INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
	created_at     INTEGER NOT NULL,
	expires_at     INTEGER NOT NULL,
	last_poll_at   INTEGER NOT NULL
);
CREATE INDEX device_authorizations_expiry ON device_authorizations(expires_at);

CREATE TABLE devices (
	id           TEXT PRIMARY KEY,
	token_hash   TEXT NOT NULL UNIQUE,
	account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	public_key   TEXT NOT NULL,
	label        TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER NOT NULL
);
CREATE INDEX devices_account ON devices(account_id);

CREATE TABLE servers (
	id               TEXT PRIMARY KEY,
	name             TEXT NOT NULL,
	kind             TEXT NOT NULL CHECK (kind IN ('self-hosted', 'hosted')),
	access_policy    TEXT NOT NULL CHECK (access_policy IN ('account', 'approved-devices')),
	owner_type       TEXT CHECK (owner_type IN ('account')),
	owner_account_id INTEGER REFERENCES accounts(id),
	claimed_at       INTEGER NOT NULL,
	CHECK ((owner_type IS NULL) = (owner_account_id IS NULL))
);
CREATE INDEX servers_owner ON servers(owner_account_id);

CREATE TABLE directory_entries (
	server_id  TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL,
	provider   TEXT NOT NULL,
	subject    TEXT NOT NULL,
	login      TEXT NOT NULL,
	email      TEXT NOT NULL,
	role       TEXT NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX directory_server ON directory_entries(server_id);
CREATE INDEX directory_member ON directory_entries(provider, subject) WHERE kind = 'member';
CREATE INDEX directory_login ON directory_entries(lower(login)) WHERE login <> '';
CREATE INDEX directory_email ON directory_entries(lower(email)) WHERE email <> '';

CREATE TABLE egress (
	month TEXT PRIMARY KEY,
	bytes INTEGER NOT NULL
);

CREATE TABLE blocked_servers (
	id         TEXT PRIMARY KEY,
	blocked_at INTEGER NOT NULL
);

CREATE TABLE blocked_accounts (
	provider   TEXT NOT NULL,
	subject    TEXT NOT NULL,
	blocked_at INTEGER NOT NULL,
	PRIMARY KEY (provider, subject)
);

CREATE TABLE pending_account_deletions (
	server_id  TEXT NOT NULL,
	provider   TEXT NOT NULL,
	subject    TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (server_id, provider, subject)
);

CREATE TABLE account_reach (
	provider  TEXT NOT NULL,
	subject   TEXT NOT NULL,
	server_id TEXT NOT NULL,
	PRIMARY KEY (provider, subject, server_id)
) WITHOUT ROWID;
`)}

func migrate(db *sql.DB, steps []migration) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("edgestore: create schema_migrations: %w", err)
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("edgestore: read schema version: %w", err)
	}
	if current > len(steps) {
		return fmt.Errorf("edgestore: database schema version %d is newer than this binary supports (%d)",
			current, len(steps))
	}
	for v := current + 1; v <= len(steps); v++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("edgestore: begin migration %d: %w", v, err)
		}
		if err := steps[v-1](tx); err != nil {
			tx.Rollback() //nolint:errcheck // the migration error takes precedence
			return fmt.Errorf("edgestore: apply migration %d: %w", v, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, unixepoch())`, v); err != nil {
			tx.Rollback() //nolint:errcheck // the migration error takes precedence
			return fmt.Errorf("edgestore: record migration %d: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("edgestore: commit migration %d: %w", v, err)
		}
	}
	return nil
}

func unix(t time.Time) int64 { return t.UnixNano() }

func fromUnix(n int64) time.Time { return time.Unix(0, n).UTC() }

// oneRow maps an exec result that touched no row to ErrNotFound.
func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// conflict maps a UNIQUE or PRIMARY KEY violation to ErrConflict.
func conflict(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && (se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}
