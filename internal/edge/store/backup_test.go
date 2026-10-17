package edgestore

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var testSteps = []migration{
	{floor: 1, sql: `CREATE TABLE notes (body TEXT NOT NULL)`},
	{floor: 1, sql: `CREATE TABLE tags (name TEXT NOT NULL)`},
}

func openSteps(t *testing.T, path string, steps []migration) *Store {
	t.Helper()
	s, err := open(path, steps)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func backupsIn(t *testing.T, dir string) []string {
	t.Helper()
	files, err := migrationBackups(filepath.Join(dir, BackupDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.name)
	}
	return names
}

func TestBackupBeforeMigrating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	s := openSteps(t, path, testSteps[:1])
	if _, err := s.db.Exec(`INSERT INTO notes VALUES ('kept')`); err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	if names := backupsIn(t, dir); len(names) != 0 {
		t.Fatalf("a new database was backed up: %v", names)
	}

	closeStore(t, openSteps(t, path, testSteps))
	names := backupsIn(t, dir)
	if len(names) != 1 || !regexp.MustCompile(`^edge-v1-v2-\d{8}T\d{6}Z\.db$`).MatchString(names[0]) {
		t.Fatalf("backups = %v, want one edge-v1-v2-<stamp>.db", names)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, BackupDir)); err != nil || len(entries) != 1 {
		t.Fatalf("backups directory holds %v, %v; want only the backup", entries, err)
	}
	backup := filepath.Join(dir, BackupDir, names[0])
	for p, want := range map[string]os.FileMode{filepath.Join(dir, BackupDir): 0o700, backup: 0o600} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s: %v %v, want mode %04o", p, info.Mode(), err, want)
		}
	}
	if s, err := ReadSchema(backup); err != nil || s != (Schema{Version: 1, CompatFloor: 1}) {
		t.Fatalf("backup schema = %+v, %v", s, err)
	}
	b := openSteps(t, backup, testSteps[:1])
	defer closeStore(t, b)
	var body string
	if err := b.db.QueryRow(`SELECT body FROM notes`).Scan(&body); err != nil || body != "kept" {
		t.Fatalf("backup row = %q, %v", body, err)
	}
}

func TestNoMigrationWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	closeStore(t, openSteps(t, path, testSteps[:1]))
	// A file where the directory belongs fails as root too.
	if err := os.WriteFile(filepath.Join(dir, BackupDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := open(path, testSteps)
	if err == nil || !strings.Contains(err.Error(), "before migrating schema 1 to 2") ||
		!strings.Contains(err.Error(), "the edge does not migrate without a verified backup") {
		t.Fatalf("open = %v, want the backup failure", err)
	}
	if s, err := ReadSchema(path); err != nil || s.Version != 1 {
		t.Fatalf("schema after the refusal = %+v, %v; want version 1", s, err)
	}
}

func TestMigrationBackupRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	closeStore(t, openSteps(t, path, testSteps[:1]))
	backups := filepath.Join(dir, BackupDir)
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	old := []string{
		"edge-v1-v2-20200101T000000Z.db", "edge-v1-v2-20200102T000000Z.db", "edge-v1-v2-20200103T000000Z.db",
		"edge-v1-v2-20200104T000000Z.db", "edge-v1-v2-20200105T000000Z.db", "stale.tmp", "edge-before-restore-20200101T000000Z.db",
	}
	for _, name := range old {
		if err := os.WriteFile(filepath.Join(backups, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	closeStore(t, openSteps(t, path, testSteps))
	names := backupsIn(t, dir)
	if len(names) != keepMigrationBackups || names[keepMigrationBackups-1] != old[1] || !strings.HasPrefix(names[0], "edge-v1-v2-20") || names[0] == old[4] {
		t.Fatalf("backups = %v, want the new one and the four newest old ones", names)
	}
	for _, name := range []string{"stale.tmp", old[0]} {
		if _, err := os.Stat(filepath.Join(backups, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(backups, old[6])); err != nil {
		t.Fatalf("the restore backup was removed: %v", err)
	}
}

func TestCompatFloor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	closeStore(t, openSteps(t, path, testSteps[:1]))
	closeStore(t, openSteps(t, path, testSteps))
	if s, err := ReadSchema(path); err != nil || s != (Schema{Version: 2, CompatFloor: 1}) {
		t.Fatalf("schema = %+v, %v", s, err)
	}
	// The older binary runs on the additive step, and leaves it be.
	closeStore(t, openSteps(t, path, testSteps[:1]))
	if s, err := ReadSchema(path); err != nil || s.Version != 2 {
		t.Fatalf("schema after the older binary = %+v, %v", s, err)
	}

	breaking := []migration{testSteps[0], {floor: 2, sql: `DROP TABLE notes`}}
	dir = t.TempDir()
	path = filepath.Join(dir, "edge.db")
	closeStore(t, openSteps(t, path, breaking[:1]))
	closeStore(t, openSteps(t, path, breaking))
	_, err := open(path, breaking[:1])
	want := "edgestore: database schema version 2 is newer than this binary supports (1); run a binary that supports schema 2 or later, " +
		"or stop the edge and restore the backup taken before the upgrade: aether-edge restore " + filepath.Join(dir, BackupDir, backupsIn(t, dir)[0])
	if err == nil || err.Error() != want {
		t.Fatalf("open = %v\nwant %s", err, want)
	}
}

// A database a binary without compat floors migrated gets the column, and
// each recorded step its own version as its floor.
func TestFloorColumnAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.db")
	s := openSteps(t, path, testSteps)
	if _, err := s.db.Exec(`ALTER TABLE schema_migrations DROP COLUMN compat_floor`); err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	if s, err := ReadSchema(path); err != nil || s != (Schema{Version: 2, CompatFloor: 2}) {
		t.Fatalf("schema without the column = %+v, %v", s, err)
	}
	closeStore(t, openSteps(t, path, testSteps))
	if s, err := ReadSchema(path); err != nil || s != (Schema{Version: 2, CompatFloor: 2}) {
		t.Fatalf("schema with the column = %+v, %v", s, err)
	}
}

var additiveStatement = regexp.MustCompile(`(?is)^(CREATE TABLE|CREATE INDEX|ALTER TABLE \w+ ADD COLUMN)\s`)

// additive reports whether a statement keeps a database usable by a binary
// that does not know it: a new table or index, or a column that is
// nullable or has a default.
func additive(q string) bool {
	q = strings.TrimSpace(q)
	if !additiveStatement.MatchString(q) {
		return false
	}
	upper := strings.ToUpper(q)
	return !strings.HasPrefix(upper, "ALTER") || !strings.Contains(upper, "NOT NULL") || strings.Contains(upper, "DEFAULT")
}

// A step that keeps an older floor holds only additive statements.
func TestFloorKeptOnlyByAdditiveSteps(t *testing.T) {
	for i, m := range migrations {
		v := i + 1
		if m.floor < 1 || m.floor > v || (i > 0 && m.floor < migrations[i-1].floor) {
			t.Fatalf("migration %d has floor %d", v, m.floor)
		}
		if m.floor == v {
			continue
		}
		for q := range strings.SplitSeq(m.sql, ";") {
			if strings.TrimSpace(q) != "" && !additive(q) {
				t.Errorf("migration %d keeps floor %d but is not additive: %s", v, m.floor, q)
			}
		}
	}
	for q, want := range map[string]bool{
		"CREATE TABLE t (a TEXT)":                             true,
		"CREATE INDEX i ON t(a)":                              true,
		"ALTER TABLE t ADD COLUMN b TEXT":                     true,
		"ALTER TABLE t ADD COLUMN b TEXT NOT NULL DEFAULT ''": true,
		"ALTER TABLE t ADD COLUMN b TEXT NOT NULL":            false,
		"CREATE UNIQUE INDEX i ON t(a)":                       false,
		"DROP TABLE t":                                        false,
		"UPDATE t SET a = ''":                                 false,
	} {
		if additive(q) != want {
			t.Errorf("additive(%q) = %v, want %v", q, !want, want)
		}
	}
}

func TestBackupAndRestore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edge.db")
	live := openSteps(t, path, testSteps)
	defer closeStore(t, live)
	if _, err := live.db.Exec(`INSERT INTO notes VALUES ('one')`); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "copy.db")
	if got, err := Backup(path, backup); err != nil || got.Version != 2 {
		t.Fatalf("Backup = %+v, %v", got, err)
	}
	if _, err := Backup(path, backup); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("Backup over an existing file = %v", err)
	}

	target := filepath.Join(t.TempDir(), "edge.db")
	old := openSteps(t, target, testSteps[:1])
	if _, err := old.db.Exec(`INSERT INTO notes VALUES ('old')`); err != nil {
		t.Fatal(err)
	}
	closeStore(t, old)
	if err := os.WriteFile(target+"-wal", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Restore(backup, target); err != nil || got.Version != 2 {
		t.Fatalf("Restore = %+v, %v", got, err)
	}
	if _, err := os.Stat(target + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("the old WAL survived the restore: %v", err)
	}
	restored := openSteps(t, target, testSteps)
	defer closeStore(t, restored)
	var body string
	if err := restored.db.QueryRow(`SELECT group_concat(body) FROM notes`).Scan(&body); err != nil || body != "one" {
		t.Fatalf("restored rows = %q, %v", body, err)
	}
}

func TestRestoreRefusesTooNewFloor(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new.db")
	steps := append(append([]migration{}, migrations...), migration{floor: len(migrations) + 1, sql: `CREATE TABLE later (a TEXT)`})
	closeStore(t, openSteps(t, src, steps))
	dst := filepath.Join(dir, "edge.db")
	_, err := Restore(src, dst)
	want := fmt.Sprintf("needs a binary that supports schema %d or later; this binary supports %d", SchemaVersion()+1, SchemaVersion())
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Restore = %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("the refused restore wrote %s: %v", dst, err)
	}
}
