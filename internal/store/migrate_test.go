package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/url"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
)

func TestAppliedMigrationSurvivesCompetingWriter(t *testing.T) {
	path := templateDBPath(t)
	opening, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opening.Close() }()
	opening.db.SetMaxOpenConns(1)
	if _, pragmaErr := opening.db.Exec(`PRAGMA busy_timeout=1`); pragmaErr != nil {
		t.Fatal(pragmaErr)
	}
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	tx, err := writer.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, version := range []int{1, 45} {
		current, err := applyMigration(opening.db, version)
		if err != nil {
			t.Errorf("already committed migration %d behind another writer: %v", version, err)
		} else if current != len(migrations) {
			t.Errorf("resume after migration %d at %d, want latest committed version %d", version, current, len(migrations))
		}
		var foreignKeys int
		if err := opening.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
			t.Fatalf("foreign key enforcement after migration %d: %d, %v", version, foreignKeys, err)
		}
	}
}

func TestMigrationDoesNotTrustUncommittedProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	writer := openLegacy(t, path, 0)
	defer func() { _ = writer.Close() }()
	opening, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opening.Close() }()
	if pingErr := opening.Ping(); pingErr != nil {
		t.Fatal(pingErr)
	}
	tx, err := writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigration(opening, 1); !isBusy(err) {
		t.Fatalf("uncommitted version must remain pending: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := migrate(opening); err != nil {
		t.Fatalf("migrate after competing rollback: %v", err)
	}
	var versions int
	if err := opening.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil || versions != len(migrations) {
		t.Fatalf("committed migration count = %d, %v; want %d", versions, err, len(migrations))
	}
}

func TestMigrationStartupResumesCommittedPeerProgress(t *testing.T) {
	for _, newer := range []bool{false, true} {
		name := "supported schema"
		if newer {
			name = "newer schema"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "aether.db")
			legacy := openLegacy(t, path, 0)
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			base, err := sqlite.NewConnector("file:" + url.PathEscape(path) +
				"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(1)&_txlock=immediate")
			if err != nil {
				t.Fatal(err)
			}
			peerStarted := false
			opening := sql.OpenDB(migrationSnapshotConnector{
				Connector: base,
				afterRead: func() error {
					peer, openErr := Open(path)
					if openErr != nil {
						return openErr
					}
					t.Cleanup(func() { _ = peer.Close() })
					if newer {
						if _, insertErr := peer.db.Exec(
							`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`,
							len(migrations)+1,
						); insertErr != nil {
							return insertErr
						}
					}
					tx, beginErr := peer.db.BeginTx(context.Background(), nil)
					if beginErr != nil {
						return beginErr
					}
					t.Cleanup(func() { _ = tx.Rollback() })
					peerStarted = true
					return nil
				},
			})
			t.Cleanup(func() { _ = opening.Close() })
			opening.SetMaxOpenConns(1)
			err = migrateOnce(opening)
			if !peerStarted {
				t.Fatalf("competing writer was not established: %v", err)
			}
			if newer {
				if err == nil || isBusy(err) {
					t.Fatalf("startup must reject the peer's newer schema, not return a lock error: %v", err)
				}
			} else if err != nil {
				t.Fatalf("startup did not resume the peer's committed schema: %v", err)
			}
			var current int
			if readErr := opening.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&current); readErr != nil {
				t.Fatal(readErr)
			}
			want := len(migrations)
			if newer {
				want++
			}
			if current != want {
				t.Fatalf("committed peer schema changed: got %d, want %d", current, want)
			}
		})
	}
}

// Release SQLite's real read snapshot before the peer commits; the caller
// still receives the original version. No query result or lock is faked.
type migrationSnapshotConnector struct {
	driver.Connector
	afterRead func() error
}

func (c migrationSnapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &migrationSnapshotConn{Conn: conn, afterRead: c.afterRead}, nil
}

type migrationSnapshotConn struct {
	driver.Conn
	afterRead func() error
}

func (c *migrationSnapshotConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *migrationSnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || c.afterRead == nil {
		return rows, err
	}
	afterRead := c.afterRead
	c.afterRead = nil
	return &migrationSnapshotRows{Rows: rows, afterRead: afterRead}, nil
}

type migrationSnapshotRows struct {
	driver.Rows
	afterRead func() error
}

func (r *migrationSnapshotRows) Close() error {
	if err := r.Rows.Close(); err != nil {
		return err
	}
	return r.afterRead()
}
