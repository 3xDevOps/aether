// Package edgestore is the edge's SQLite store: accounts, browser sessions,
// device authorizations, devices, claimed servers with their owners and
// directories, the account deletions owed to servers, the relay's monthly
// egress, and the servers and accounts the operator blocked. It stores
// bearer secrets only as edgeproto.HashToken values.
package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
// pending migrations, after a verified backup into the backups directory
// beside it. The file is created 0600 before SQLite sees it, because SQLite
// copies its mode onto the -wal and -shm files. A database a newer binary
// migrated opens unmigrated when its compat floor allows this binary.
func Open(path string) (*Store, error) {
	return open(path, migrations)
}

func open(path string, steps []migration) (*Store, error) {
	db, err := openRW(path)
	if err != nil {
		return nil, err
	}
	if err := prepare(db, path, steps); err != nil {
		db.Close() //nolint:errcheck // the migration error takes precedence
		return nil, err
	}
	return &Store{db: db}, nil
}

// OpenCurrent opens the existing database at path without migrating it,
// and refuses one this binary would migrate or cannot run on: the operator
// commands use it beside a running edge, which owns the schema.
func OpenCurrent(path string) (*Store, error) {
	db, err := openRW(path)
	if err != nil {
		return nil, err
	}
	s, err := readSchema(db)
	if err == nil && (s.Version < SchemaVersion() || s.CompatFloor > SchemaVersion()) {
		err = fmt.Errorf("edgestore: %s is at schema version %d with compat floor %d, and this binary supports %d",
			path, s.Version, s.CompatFloor, SchemaVersion())
	}
	if err != nil {
		db.Close() //nolint:errcheck // the schema error takes precedence
		return nil, err
	}
	return &Store{db: db}, nil
}

func openRW(path string) (*sql.DB, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("edgestore: create %s: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return nil, fmt.Errorf("edgestore: create %s: %w", path, err)
	}
	// temp_store(memory): the edge may run on a read-only root, where
	// SQLite could not create its temporary files outside the data
	// directory.
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=temp_store(memory)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("edgestore: open %s: %w", path, err)
	}
	return db, nil
}

// HasIdentities reports whether the database holds an account or a
// claimed server, which the edge's signing key vouches for.
func (s *Store) HasIdentities(ctx context.Context) (bool, error) {
	var has bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM accounts) OR EXISTS (SELECT 1 FROM servers)`).Scan(&has); err != nil {
		return false, fmt.Errorf("edgestore: read accounts and servers: %w", err)
	}
	return has, nil
}

// Close closes the database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("edgestore: close: %w", err)
	}
	return nil
}

// migration is one step of the schema history, applied in its own
// transaction. floor is the lowest schema version a binary must support to
// run correctly on the database after this step. A step keeps the previous
// step's floor only when it is additive (a new table, a nullable or
// defaulted column, an index) and an older binary stays correct without
// reading or maintaining what it adds; any step an older binary must know
// about, such as a new revocation or block, sets the floor to its own
// version.
type migration struct {
	floor int
	sql   string
}

// SchemaVersion is the schema version this binary migrates to.
func SchemaVersion() int { return len(migrations) }

// CompatFloor is the lowest schema version a binary must support to run on
// a database at SchemaVersion.
func CompatFloor() int { return migrations[len(migrations)-1].floor }

// migrations is the append-only schema history; entry i is version i+1.
var migrations = []migration{{floor: 1, sql: `
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
`}}

// Schema is a database's schema version and the compat floor recorded
// with it.
type Schema struct {
	Version     int
	CompatFloor int
}

func prepare(db *sql.DB, path string, steps []migration) error {
	s, err := readSchema(db)
	if err != nil {
		return err
	}
	supported := len(steps)
	switch {
	case s.Version > supported && s.CompatFloor <= supported:
		slog.Warn("edgestore: the database was migrated by a newer binary; running on it unmigrated, as its compat floor allows",
			"path", path, "schema", s.Version, "compat_floor", s.CompatFloor, "supported", supported)
		return nil
	case s.Version > supported:
		return fmt.Errorf("edgestore: database schema version %d is newer than this binary supports (%d); %s",
			s.Version, supported, restoreHint(path, supported, s.CompatFloor))
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version      INTEGER PRIMARY KEY,
		applied_at   INTEGER NOT NULL,
		compat_floor INTEGER
	)`); err != nil {
		return fmt.Errorf("edgestore: create schema_migrations: %w", err)
	}
	hasFloor, err := hasFloorColumn(db)
	if err != nil {
		return err
	}
	if s.Version > 0 && (s.Version < supported || !hasFloor) {
		if err := backupBeforeMigrating(path, s.Version, supported); err != nil {
			return err
		}
	}
	if !hasFloor {
		if _, err := db.Exec(`ALTER TABLE schema_migrations ADD COLUMN compat_floor INTEGER;
			UPDATE schema_migrations SET compat_floor = version`); err != nil {
			return fmt.Errorf("edgestore: add schema_migrations.compat_floor: %w", err)
		}
	}
	for v := s.Version + 1; v <= supported; v++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("edgestore: begin migration %d: %w", v, err)
		}
		if _, err := tx.Exec(steps[v-1].sql); err != nil {
			tx.Rollback() //nolint:errcheck // the migration error takes precedence
			return fmt.Errorf("edgestore: apply migration %d: %w", v, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at, compat_floor) VALUES (?, unixepoch(), ?)`,
			v, steps[v-1].floor); err != nil {
			tx.Rollback() //nolint:errcheck // the migration error takes precedence
			return fmt.Errorf("edgestore: record migration %d: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("edgestore: commit migration %d: %w", v, err)
		}
	}
	return nil
}

// hasFloorColumn reports whether schema_migrations records compat floors.
// In one made before they existed, each recorded step counts as its own
// version's floor.
func hasFloorColumn(db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('schema_migrations') WHERE name = 'compat_floor'`).Scan(&n); err != nil {
		return false, fmt.Errorf("edgestore: read schema_migrations columns: %w", err)
	}
	return n == 1, nil
}

// readSchema reads the schema without changing the database. A step
// recorded without a floor counts as its own version.
func readSchema(db *sql.DB) (Schema, error) {
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&tables); err != nil {
		return Schema{}, fmt.Errorf("edgestore: read schema version: %w", err)
	}
	if tables == 0 {
		return Schema{}, nil
	}
	floor := "version"
	if has, err := hasFloorColumn(db); err != nil {
		return Schema{}, err
	} else if has {
		floor = "COALESCE(compat_floor, version)"
	}
	var s Schema
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0), COALESCE(MAX(`+floor+`), 0) FROM schema_migrations`).Scan(&s.Version, &s.CompatFloor); err != nil {
		return Schema{}, fmt.Errorf("edgestore: read schema version: %w", err)
	}
	return s, nil
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
