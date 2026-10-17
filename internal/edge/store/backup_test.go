package edgestore

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	if len(names) != 1 || !regexp.MustCompile(`^edge-v1-v2-\d{8}T\d{6}\.\d{6}Z\.db$`).MatchString(names[0]) {
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
		"edge-v1-v2-20200101T000000.000000Z.db", "edge-v1-v2-20200102T000000.000000Z.db", "edge-v1-v2-20200103T000000.000000Z.db",
		"edge-v1-v2-20200104T000000.000000Z.db", "edge-v1-v2-20200105T000000.000000Z.db",
		// The only backup from schema 9 is kept however old.
		"edge-v9-v10-20190101T000000.000000Z.db",
		"edge-before-restore-20200101T000000.000000Z.db", "stdout-1-20200101T000000.000000Z.db.42.tmp",
		"edge-v1-v2-20200106T000000.000000Z.db.42.tmp",
	}
	for _, name := range old {
		if err := os.WriteFile(filepath.Join(backups, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	closeStore(t, openSteps(t, path, testSteps))
	names := backupsIn(t, dir)
	if len(names) != keepMigrationBackups+1 || names[keepMigrationBackups-1] != old[1] || names[keepMigrationBackups] != old[5] ||
		names[0] == old[4] {
		t.Fatalf("backups = %v, want the new one, the four newest old ones and the one from schema 9", names)
	}
	for name, want := range map[string]bool{old[0]: false, old[6]: true, old[7]: true, old[8]: false} {
		if _, err := os.Stat(filepath.Join(backups, name)); (err == nil) != want {
			t.Errorf("%s: %v, want kept = %v", name, err, want)
		}
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
	if names := backupsIn(t, filepath.Dir(path)); len(names) != 1 || !strings.HasPrefix(names[0], "edge-v2-v2-") {
		t.Fatalf("backups = %v, want one taken before adding the column", names)
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
	staged, got, err := StageRestore(backup, target)
	if err != nil || got.Version != 2 {
		t.Fatalf("StageRestore = %+v, %v", got, err)
	}
	if err = FinishRestore(staged, target); err != nil {
		t.Fatal(err)
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
	_, _, err := StageRestore(src, dst)
	want := fmt.Sprintf("needs a binary that supports schema %d or later; this binary supports %d", SchemaVersion()+1, SchemaVersion())
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("StageRestore = %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("the refused restore wrote %s: %v", dst, err)
	}
}

// A restore that fails keeps the writes only the old database's WAL held.
func TestFailedRestoreKeepsWAL(t *testing.T) {
	live := filepath.Join(t.TempDir(), "edge.db")
	s := openSteps(t, live, testSteps)
	defer closeStore(t, s)
	if _, err := s.db.Exec(`PRAGMA wal_autocheckpoint = 0; INSERT INTO notes VALUES ('in the wal')`); err != nil {
		t.Fatal(err)
	}
	// A copy taken while the store is open is what an unclean stop leaves.
	crashed := filepath.Join(t.TempDir(), "edge.db")
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(live + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(crashed+suffix, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := FinishRestore(filepath.Join(t.TempDir(), "missing.db"), crashed); err == nil {
		t.Fatal("FinishRestore of a missing file succeeded")
	}
	db, err := openRO(crashed)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // read-only
	var body string
	if err := db.QueryRow(`SELECT body FROM notes`).Scan(&body); err != nil || body != "in the wal" {
		t.Fatalf("row after the failed restore = %q, %v", body, err)
	}
}

// Beside a running edge, a database opens unmigrated when this binary is
// its schema, or a newer one whose floor allows it.
func TestOpenCurrent(t *testing.T) {
	v := SchemaVersion()
	for _, tt := range []struct {
		steps []migration
		ok    bool
	}{
		{migrations, true},
		{append(slices.Clone(migrations), migration{floor: CompatFloor(), sql: `CREATE TABLE later (a TEXT)`}), true},
		{append(slices.Clone(migrations), migration{floor: v + 1, sql: `CREATE TABLE later (a TEXT)`}), false},
	} {
		path := filepath.Join(t.TempDir(), "edge.db")
		closeStore(t, openSteps(t, path, tt.steps))
		s, err := OpenCurrent(path)
		if (err == nil) != tt.ok {
			t.Errorf("schema %d, floor %d: OpenCurrent = %v, want ok = %v", len(tt.steps), tt.steps[len(tt.steps)-1].floor, err, tt.ok)
		}
		if err == nil {
			closeStore(t, s)
		}
	}
}
