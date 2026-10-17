package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
)

// newEdge makes a data directory with a database holding one account and
// a signing key, and returns the key's fingerprint.
func newEdge(t *testing.T, login string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := edgestore.Open(filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SignIn(context.Background(), edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "42", Login: login}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "aether edge")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, keyFile), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, edgeproto.EdgeKeyFingerprint(pub)
}

func TestBackupAndRestoreCommands(t *testing.T) {
	src, fp := newEdge(t, "octo")
	var out bytes.Buffer
	file := filepath.Join(t.TempDir(), "nested", "edge-copy.db")
	if err := backupCmd([]string{"--data", src, "--with-key", file}, envOf(nil), &out, &out); err != nil {
		t.Fatalf("backup: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "backed up the edge key "+fp) {
		t.Fatalf("backup printed:\n%s", out.String())
	}

	dst, oldFP := newEdge(t, "other")
	out.Reset()
	if err := restoreCmd([]string{"--data", dst, "--key", file + ".key", file}, envOf(nil), &out); err != nil {
		t.Fatalf("restore: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "restored the edge key "+fp) ||
		!strings.Contains(out.String(), "saved the files it replaced: "+filepath.Join(dst, "backups", "edge-before-restore-")) {
		t.Fatalf("restore printed:\n%s", out.String())
	}
	savedKeys, _ := filepath.Glob(filepath.Join(dst, "backups", "edge_key-before-restore-*"))
	if len(savedKeys) != 1 {
		t.Fatalf("saved keys: %v", savedKeys)
	}
	if info, err := os.Stat(savedKeys[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved key: %v %v", info, err)
	}
	if _, got, err := readKey(savedKeys[0]); err != nil || got != oldFP {
		t.Fatalf("saved key fingerprint %s, %v; want %s", got, err, oldFP)
	}
	if info, err := os.Stat(filepath.Join(dst, keyFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored key: %v %v", info, err)
	}
	if _, got, err := readKey(filepath.Join(dst, keyFile)); err != nil || got != fp {
		t.Fatalf("restored key fingerprint %s, %v; want %s", got, err, fp)
	}
	listed, err := run(t, dst, "accounts", "list")
	if err != nil || !strings.Contains(listed, "octo") || strings.Contains(listed, "other") {
		t.Fatalf("accounts after the restore:\n%s%v", listed, err)
	}
	saved, _ := filepath.Glob(filepath.Join(dst, "backups", "edge-before-restore-*.db"))
	if len(saved) != 1 {
		t.Fatalf("saved databases: %v", saved)
	}
	if s, err := edgestore.ReadSchema(saved[0]); err != nil || s.Version != edgestore.SchemaVersion() {
		t.Fatalf("saved database schema %+v, %v", s, err)
	}
}

// Without --key, a restore needs the key the database's servers pin.
func TestRestoreNeedsAKey(t *testing.T) {
	src, _ := newEdge(t, "octo")
	dst, _ := newEdge(t, "other")
	if err := os.Remove(filepath.Join(dst, keyFile)); err != nil {
		t.Fatal(err)
	}
	err := restoreCmd([]string{"--data", dst, filepath.Join(src, "edge.db")}, envOf(nil), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "restore it too with --key <file>") {
		t.Fatalf("restore without a key: %v", err)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// A failing output cannot cut a backup short: the key is written first.
func TestBackupWritesTheKeyBeforeReporting(t *testing.T) {
	dir, _ := newEdge(t, "octo")
	file := filepath.Join(t.TempDir(), "edge.db")
	if err := backupCmd([]string{"--data", dir, "--with-key", file}, envOf(nil), brokenWriter{}, brokenWriter{}); err == nil {
		t.Fatal("backup to a broken output succeeded")
	}
	if _, err := os.Stat(file + ".key"); err != nil {
		t.Fatalf("the key was not backed up: %v", err)
	}
}

func TestBackupToStdout(t *testing.T) {
	dir, _ := newEdge(t, "octo")
	var out, errOut bytes.Buffer
	if err := backupCmd([]string{"--data", dir, "-"}, envOf(nil), &out, &errOut); err != nil {
		t.Fatalf("backup -: %v\n%s", err, errOut.String())
	}
	copied := filepath.Join(t.TempDir(), "edge.db")
	if err := os.WriteFile(copied, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := edgestore.ReadSchema(copied); err != nil || s.Version != edgestore.SchemaVersion() {
		t.Fatalf("streamed copy: schema %+v, %v", s, err)
	}
	if left, _ := os.ReadDir(filepath.Join(dir, "backups")); len(left) != 0 {
		t.Fatalf("backup - left %v behind", left)
	}
	if err := backupCmd([]string{"--data", dir, "--with-key", "-"}, envOf(nil), &out, &errOut); err == nil {
		t.Fatal("backup --with-key - succeeded")
	}
}

// A second edge, and a restore, are refused while an edge holds the lock;
// operator commands then run only on a schema this binary can run on, and
// never during a restore.
func TestDataLock(t *testing.T) {
	dir, _ := newEdge(t, "octo")
	unlock, err := lockPath(filepath.Join(dir, lockFile), exclusive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lockPath(filepath.Join(dir, lockFile), exclusive); !errors.Is(err, errLocked) {
		t.Fatalf("second lock: %v, want errLocked", err)
	}
	err = restoreCmd([]string{"--data", dir, filepath.Join(dir, "edge.db")}, envOf(nil), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "an edge is running on "+dir+": stop it first") {
		t.Fatalf("restore beside a running edge: %v", err)
	}
	if out, listErr := run(t, dir, "accounts", "list"); listErr != nil || !strings.Contains(out, "octo") {
		t.Fatalf("accounts list beside a running edge:\n%s%v", out, listErr)
	}

	// A restore holds both locks.
	unlockDB, err := lockPath(filepath.Join(dir, dbLockFile), exclusive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = run(t, dir, "accounts", "list"); err == nil || !strings.Contains(err.Error(), "a restore is replacing") {
		t.Fatalf("accounts list during a restore: %v", err)
	}
	unlockDB()
	unlock()

	// An operator command beside a running edge holds the database's lock.
	unlockDB, err = lockPath(filepath.Join(dir, dbLockFile), shared)
	if err != nil {
		t.Fatal(err)
	}
	err = restoreCmd([]string{"--data", dir, filepath.Join(dir, "edge.db")}, envOf(nil), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "another aether-edge command has "+filepath.Join(dir, "edge.db")+" open") {
		t.Fatalf("restore beside an operator command: %v", err)
	}
	unlockDB()

	db, err := sql.Open("sqlite", filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO schema_migrations (version, applied_at, compat_floor) VALUES (99, 0, 99)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if unlock, err = lockPath(filepath.Join(dir, lockFile), exclusive); err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, err = run(t, dir, "accounts", "list")
	if err == nil || !strings.Contains(err.Error(), "is at schema version 99 with compat floor 99, and this binary supports 1; an edge is running on "+dir+
		", so run this command with the same image or binary as the running edge") {
		t.Fatalf("accounts list at another schema: %v", err)
	}
}

func TestSchemaJSON(t *testing.T) {
	dir, _ := newEdge(t, "octo")
	var out bytes.Buffer
	if err := schemaCmd([]string{"--data", dir, "--json"}, envOf(nil), &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	v := edgestore.SchemaVersion()
	if got["schema"] != v || got["compat_floor"] != edgestore.CompatFloor() || got["supported"] != v {
		t.Fatalf("schema --json = %s", out.String())
	}
}
