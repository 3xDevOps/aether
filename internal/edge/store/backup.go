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
	"syscall"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// BackupDir is the directory beside the database that holds the backups
// taken before migrations and restores.
const BackupDir = "backups"

// keepMigrationBackups is how many pre-migration backups are kept, besides
// the newest from each schema version.
const keepMigrationBackups = 5

// StampLayout is the UTC timestamp in backup file names. Microseconds keep
// two backups taken in one second, as a restart loop does, apart.
const StampLayout = "20060102T150405.000000Z"

// Stamp is the timestamp of a backup taken now.
func Stamp() string { return time.Now().UTC().Format(StampLayout) }

var migrationBackup = regexp.MustCompile(`^edge-v([0-9]+)-v([0-9]+)-([0-9]{8}T[0-9]{6}\.[0-9]{6}Z)\.db$`)

// backupBeforeMigrating takes a verified copy of the database at path,
// at schema version from, into the backups directory, then prunes the
// pre-migration backups. Without the copy there is no migration.
func backupBeforeMigrating(path string, from, to int) error {
	dir := filepath.Join(filepath.Dir(path), BackupDir)
	dst := filepath.Join(dir, fmt.Sprintf("edge-v%d-v%d-%s.db", from, to, Stamp()))
	fail := func(err error) error {
		return fmt.Errorf("edgestore: back up %s to %s before migrating schema %d to %d: %w; "+
			"the edge does not migrate without a verified backup: make %s writable by uid %d with room for a copy of the database, then start the edge again",
			path, dst, from, to, err, dir, os.Geteuid())
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(err)
	}
	// Only a migration writes these, under the data directory's lock, so
	// any found now is abandoned.
	stale, err := filepath.Glob(filepath.Join(dir, "edge-v*-v*-*.db.*.tmp"))
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
	if err = pruneMigrationBackups(dir); err != nil {
		return fail(err)
	}
	return nil
}

// pruneMigrationBackups keeps the newest keepMigrationBackups pre-migration
// backups and the newest from each schema version, so a migration that
// fails on every restart cannot prune the only copy an older binary runs on.
func pruneMigrationBackups(dir string) error {
	files, err := migrationBackups(dir)
	if err != nil {
		return err
	}
	seen := map[int]bool{}
	for i, f := range files {
		if i < keepMigrationBackups || !seen[f.from] {
			seen[f.from] = true
			continue
		}
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil {
			return fmt.Errorf("remove an old backup: %w", err)
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
			return fmt.Sprintf("run a binary that supports schema %d or later, or stop the edge and restore the backup taken before the upgrade: aether-edge restore --data %s %s",
				floor, filepath.Dir(path), filepath.Join(dir, f.name))
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

// Backup writes a consistent, verified copy of the database at src to dst
// through a read-only connection, so a running edge keeps writing. It
// never replaces an existing dst. It returns the copy's schema.
func Backup(src, dst string) (Schema, error) {
	tmp, s, err := snapshot(src, dst)
	if err != nil {
		return Schema{}, err
	}
	defer os.Remove(tmp) //nolint:errcheck // once published, the copy lives on as dst
	if err := publish(tmp, dst); errors.Is(err, fs.ErrExist) {
		return Schema{}, fmt.Errorf("edgestore: %s exists; name a new file or a directory", dst)
	} else if err != nil {
		return Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	return s, SyncDir(filepath.Dir(dst))
}

// link is os.Link; tests replace it to stand in for a filesystem without
// hard links.
var link = os.Link

// publish puts tmp in place as dst, failing on an existing dst as a link
// does. Where the filesystem has no hard links, such as SMB, an O_EXCL
// reservation of dst comes before the rename.
func publish(tmp, dst string) error {
	err := link(tmp, dst)
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.EXDEV) {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err = f.Close(); err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(dst) //nolint:errcheck // the rename error takes precedence
	}
	return err
}

// StageRestore writes a verified copy of src beside dst, for
// FinishRestore to put in place, and returns its path and schema. src must
// be a database this binary can run on.
func StageRestore(src, dst string) (string, Schema, error) {
	s, err := ReadSchema(src)
	if err != nil {
		return "", Schema{}, err
	}
	if s.Version == 0 {
		return "", Schema{}, fmt.Errorf("edgestore: %s is not an edge database: it has no schema_migrations", src)
	}
	if s.CompatFloor > SchemaVersion() {
		return "", Schema{}, fmt.Errorf("edgestore: %s is at schema version %d and needs a binary that supports schema %d or later; this binary supports %d: restore it with the image that wrote it",
			src, s.Version, s.CompatFloor, SchemaVersion())
	}
	return snapshot(src, dst)
}

// FinishRestore replaces the database at dst with staged. Nothing else may
// have dst open. dst's WAL is checkpointed first, so its committed writes
// stay in dst should the replacement fail; then its -wal and -shm files,
// which belong to it, are removed. Only a dst in use stops the restore: one
// too damaged to checkpoint is what a restore replaces, and the caller has
// saved it and its WAL.
func FinishRestore(staged, dst string) error {
	_, err := os.Stat(dst)
	switch {
	case err == nil:
		if err = checkpoint(dst); errors.Is(err, errInUse) {
			return err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("edgestore: %w", err)
	}
	for _, side := range []string{dst + "-wal", dst + "-shm"} {
		if err := os.Remove(side); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("edgestore: %w", err)
		}
	}
	if err := os.Rename(staged, dst); err != nil {
		return fmt.Errorf("edgestore: %w", err)
	}
	return SyncDir(filepath.Dir(dst))
}

var errInUse = errors.New("the database is in use")

func checkpoint(path string) error {
	db, err := openRW(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // the checkpoint result is what counts
	var busy, frames, done int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &done); err != nil {
		var se *sqlite.Error
		if errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY {
			err = fmt.Errorf("%w: %v", errInUse, err)
		}
		return fmt.Errorf("edgestore: checkpoint %s: %w", path, err)
	}
	if busy != 0 {
		return fmt.Errorf("edgestore: checkpoint %s: %w", path, errInUse)
	}
	return nil
}

// snapshot copies the database at src into a new file beside dst with
// VACUUM INTO, syncs it, checks the copy's integrity, and returns its path
// and schema. The file is created 0600 first, because VACUUM INTO would
// create it with the umask's mode.
func snapshot(src, dst string) (_ string, s Schema, err error) {
	f, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.tmp")
	if err != nil {
		return "", Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp) //nolint:errcheck // the copy error takes precedence
		}
	}()
	if err = f.Close(); err != nil {
		return "", Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	db, err := openRO(src)
	if err != nil {
		return "", Schema{}, err
	}
	_, err = db.Exec(`VACUUM INTO ?`, tmp)
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", Schema{}, fmt.Errorf("edgestore: copy %s to %s: %w", src, tmp, err)
	}
	if f, err = os.OpenFile(tmp, os.O_RDWR, 0); err != nil {
		return "", Schema{}, fmt.Errorf("edgestore: %w", err)
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", Schema{}, fmt.Errorf("edgestore: sync %s: %w", tmp, err)
	}
	if s, err = verify(tmp); err != nil {
		return "", Schema{}, err
	}
	return tmp, s, nil
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

// SyncDir makes the renames and links in dir durable.
func SyncDir(dir string) error {
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
