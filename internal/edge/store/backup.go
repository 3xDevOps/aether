package edgestore

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// BackupDir is the directory beside the database that holds the backups
// taken before migrations and restores.
const BackupDir = "backups"

// keepMigrationBackups is how many pre-migration backups are kept.
const keepMigrationBackups = 5

// StampLayout is the UTC timestamp in backup file names.
const StampLayout = "20060102T150405Z"

var migrationBackup = regexp.MustCompile(`^edge-v([0-9]+)-v([0-9]+)-([0-9]{8}T[0-9]{6}Z)\.db$`)

// backupBeforeMigrating takes a verified copy of the database at path,
// at schema version from, into the backups directory, then keeps only the
// newest keepMigrationBackups of them. Without the copy there is no
// migration.
func backupBeforeMigrating(path string, from, to int) error {
	dir := filepath.Join(filepath.Dir(path), BackupDir)
	dst := filepath.Join(dir, fmt.Sprintf("edge-v%d-v%d-%s.db", from, to, time.Now().UTC().Format(StampLayout)))
	fail := func(err error) error {
		return fmt.Errorf("edgestore: back up %s to %s before migrating schema %d to %d: %w; "+
			"the edge does not migrate without a verified backup: make %s writable by uid %d with room for a copy of the database, then start the edge again",
			path, dst, from, to, err, dir, os.Geteuid())
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(err)
	}
	stale, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		return fail(err)
	}
	for _, f := range stale {
		if err = os.Remove(f); err != nil {
			return fail(err)
		}
	}
	s, err := Backup(path, dst)
	if err != nil {
		return fail(err)
	}
	if s.Version != from {
		return fail(fmt.Errorf("the copy is at schema version %d, want %d", s.Version, from))
	}
	old, err := migrationBackups(dir)
	if err != nil {
		return fail(err)
	}
	for _, b := range old[min(len(old), keepMigrationBackups):] {
		if err := os.Remove(filepath.Join(dir, b.name)); err != nil {
			return fail(fmt.Errorf("remove an old backup: %w", err))
		}
	}
	return nil
}

type backupFile struct {
	name  string
	from  int
	stamp string
}

// migrationBackups lists the pre-migration backups in dir, newest first.
func migrationBackups(dir string) ([]backupFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []backupFile
	for _, e := range entries {
		m := migrationBackup.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		from, _ := strconv.Atoi(m[1])
		files = append(files, backupFile{name: e.Name(), from: from, stamp: m[3]})
	}
	slices.SortFunc(files, func(a, b backupFile) int {
		return -strings.Compare(a.stamp+a.name, b.stamp+b.name)
	})
	return files, nil
}

// restoreHint names the newest pre-migration backup a binary supporting
// schema version supported can run on.
func restoreHint(path string, supported, floor int) string {
	dir := filepath.Join(filepath.Dir(path), BackupDir)
	files, _ := migrationBackups(dir) // the hint is best effort; the refusal stands
	for _, f := range files {
		if f.from <= supported {
			return fmt.Sprintf("run a binary that supports schema %d or later, or stop the edge and restore the backup taken before the upgrade: aether-edge restore %s",
				floor, filepath.Join(dir, f.name))
		}
	}
	return fmt.Sprintf("run a binary that supports schema %d or later; %s holds no backup taken at schema %d or older", floor, dir, supported)
}

// ReadSchema reads the schema of the database at path through a read-only
// connection, so it never migrates or creates it.
func ReadSchema(path string) (Schema, error) {
	db, err := openRO(path)
	if err != nil {
		return Schema{}, err
	}
	defer db.Close() //nolint:errcheck // read-only
	return readSchema(db)
}

// Backup writes a consistent, verified copy of the database at src to dst,
// which must not exist, through a read-only connection, so a running edge
// keeps writing. It returns the copy's schema.
func Backup(src, dst string) (Schema, error) {
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		return Schema{}, fmt.Errorf("edgestore: %s exists; name a new file", dst)
	}
	tmp := dst + ".tmp"
	s, err := snapshot(src, tmp)
	if err != nil {
		return Schema{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp) //nolint:errcheck // the rename error takes precedence
		return Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	return s, syncDir(filepath.Dir(dst))
}

// Restore replaces the database at dst with a verified copy of src, which
// must be a database this binary can run on. Nothing may have dst open:
// its -wal and -shm files belong to the replaced database and are removed.
func Restore(src, dst string) (Schema, error) {
	s, err := ReadSchema(src)
	if err != nil {
		return Schema{}, err
	}
	if s.Version == 0 {
		return Schema{}, fmt.Errorf("edgestore: %s is not an edge database: it has no schema_migrations", src)
	}
	if s.CompatFloor > SchemaVersion() {
		return Schema{}, fmt.Errorf("edgestore: %s is at schema version %d and needs a binary that supports schema %d or later; this binary supports %d: restore it with the image that wrote it",
			src, s.Version, s.CompatFloor, SchemaVersion())
	}
	tmp := dst + ".restore.tmp"
	if err = os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	if s, err = snapshot(src, tmp); err != nil {
		return Schema{}, err
	}
	for _, side := range []string{dst + "-wal", dst + "-shm"} {
		if err := os.Remove(side); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Schema{}, fmt.Errorf("edgestore: %w", err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	return s, syncDir(filepath.Dir(dst))
}

// snapshot copies the database at src into the new file dst with VACUUM
// INTO, syncs it and checks the copy's integrity. dst is created 0600
// first, because VACUUM INTO would create it with the umask's mode.
func snapshot(src, dst string) (s Schema, err error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	if err = f.Close(); err != nil {
		return Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	defer func() {
		if err != nil {
			os.Remove(dst) //nolint:errcheck // the copy error takes precedence
		}
	}()
	db, err := openRO(src)
	if err != nil {
		return Schema{}, err
	}
	_, err = db.Exec(`VACUUM INTO ?`, dst)
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Schema{}, fmt.Errorf("edgestore: copy %s to %s: %w", src, dst, err)
	}
	if f, err = os.OpenFile(dst, os.O_RDWR, 0); err != nil {
		return Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Schema{}, fmt.Errorf("edgestore: sync %s: %w", dst, err)
	}
	return verify(dst)
}

// verify runs PRAGMA integrity_check on the database at path and returns
// its schema.
func verify(path string) (Schema, error) {
	db, err := openRO(path)
	if err != nil {
		return Schema{}, err
	}
	defer db.Close() //nolint:errcheck // read-only
	rows, err := db.Query(`PRAGMA integrity_check(20)`)
	if err != nil {
		return Schema{}, fmt.Errorf("edgestore: check %s: %w", path, err)
	}
	var problems []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close() //nolint:errcheck // the scan error takes precedence
			return Schema{}, fmt.Errorf("edgestore: check %s: %w", path, err)
		}
		problems = append(problems, p)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Schema{}, fmt.Errorf("edgestore: check %s: %w", path, err)
	}
	if len(problems) != 1 || problems[0] != "ok" {
		return Schema{}, fmt.Errorf("edgestore: %s fails PRAGMA integrity_check: %s", path, strings.Join(problems, "; "))
	}
	return readSchema(db)
}

func openRO(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("edgestore: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=temp_store(memory)")
	if err != nil {
		return nil, fmt.Errorf("edgestore: open %s: %w", path, err)
	}
	return db, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("edgestore: %w", err)
	}
	err = d.Sync()
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("edgestore: sync %s: %w", dir, err)
	}
	return nil
}
