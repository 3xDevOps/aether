package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
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
