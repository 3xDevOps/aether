package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// The data directory's locks. The edge and every command that may migrate
// or replace edge.db hold edge.lock exclusively. Everything with edge.db
// open holds edge.db.lock shared, and a restore holds it exclusively, so
// an operator command beside a running edge never meets a restore.
const (
	lockFile   = "edge.lock"
	dbLockFile = "edge.db.lock"
	keyFile    = "edge_key"
)

// errLocked reports a lock another process holds.
var errLocked = errors.New("another aether-edge process holds the lock")

func versionCmd(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("aether-edge version", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if !*asJSON {
		_, err := fmt.Fprintln(out, "aether-edge", version.String())
		return err
	}
	type protocol struct {
		Version    int `json:"version"`
		MinVersion int `json:"min_version"`
	}
	return json.NewEncoder(out).Encode(struct {
		Version          string   `json:"version"`
		Commit           string   `json:"commit"`
		StoreSchema      int      `json:"store_schema"`
		StoreCompatFloor int      `json:"store_compat_floor"`
		Protocol         protocol `json:"protocol"`
	}{version.Version, version.Commit, edgestore.SchemaVersion(), edgestore.CompatFloor(),
		protocol{edgeproto.Version, edgeproto.MinVersion}})
}

func schemaCmd(args []string, getenv func(string) string, out io.Writer) error {
	flags := flag.NewFlagSet("aether-edge schema", flag.ContinueOnError)
	dataDir := flags.String("data", dataDirDefault(getenv), "data directory of the edge")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	db, err := edgeDB(*dataDir)
	if err != nil {
		return err
	}
	unlock, err := lockDB(*dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := edgestore.ReadSchema(db)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(struct {
			Schema      int `json:"schema"`
			CompatFloor int `json:"compat_floor"`
			Supported   int `json:"supported"`
		}{s.Version, s.CompatFloor, edgestore.SchemaVersion()})
	}
	_, err = fmt.Fprintf(out, "%s: schema %d, compat floor %d; this aether-edge supports schema %d\n",
		db, s.Version, s.CompatFloor, edgestore.SchemaVersion())
	return err
}

func backupCmd(args []string, getenv func(string) string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("aether-edge backup", flag.ContinueOnError)
	dataDir := flags.String("data", dataDirDefault(getenv), "data directory of the edge")
	withKey := flags.Bool("with-key", false, "copy the signing key edge_key to <file>.key too")
	if err := parseFlags(flags, args, 1); err != nil {
		return err
	}
	target := flags.Arg(0)
	db, err := edgeDB(*dataDir)
	if err != nil {
		return err
	}
	// Held across reading edge_key and edge.db, so a restore cannot pair
	// one edge's key with another's database.
	unlock, err := lockDB(*dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	if target == "-" {
		if *withKey {
			return errors.New("--with-key needs a file or directory: standard output carries only the database")
		}
		return backupToStdout(db, filepath.Join(*dataDir, edgestore.BackupDir), out, errOut)
	}
	var key []byte
	var fp string
	if *withKey {
		if key, fp, err = readKey(filepath.Join(*dataDir, keyFile)); err != nil {
			return err
		}
	}
	dst, dir := target, filepath.Dir(target)
	if info, statErr := os.Stat(target); (statErr == nil && info.IsDir()) || strings.HasSuffix(target, string(os.PathSeparator)) {
		cur, readErr := edgestore.ReadSchema(db)
		if readErr != nil {
			return readErr
		}
		dst = filepath.Join(target, fmt.Sprintf("edge-v%d-%s.db", cur.Version, edgestore.Stamp()))
		dir = target
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	s, err := edgestore.Backup(db, dst)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("backed up %s (schema %d) to %s\n", db, s.Version, dst)
	if *withKey {
		if err = writeNew(dst+".key", key); err != nil {
			return err
		}
		if err = edgestore.SyncDir(dir); err != nil {
			return err
		}
		msg += fmt.Sprintf("backed up the edge key %s to %s.key\n", fp, dst)
	}
	_, err = io.WriteString(out, msg)
	return err
}

// backupToStdout copies the database through a file in dir, which is on
// the data volume, so it needs no writable root or /tmp.
func backupToStdout(db, dir string, out, errOut io.Writer) error {
	// A closed pipe then fails the write instead of killing the process
	// before it removes the file.
	signal.Ignore(syscall.SIGPIPE)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("stdout-%d-%s.db", os.Getpid(), edgestore.Stamp()))
	s, err := edgestore.Backup(db, tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) //nolint:errcheck // best effort; the name is unique
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only
	if _, err = io.Copy(out, f); err != nil {
		return fmt.Errorf("write the backup to standard output: %w", err)
	}
	_, err = fmt.Fprintf(errOut, "backed up %s (schema %d) to standard output\n", db, s.Version)
	return err
}

func restoreCmd(args []string, getenv func(string) string, out io.Writer) error {
	flags := flag.NewFlagSet("aether-edge restore", flag.ContinueOnError)
	dataDir := flags.String("data", dataDirDefault(getenv), "data directory of the edge")
	keyPath := flags.String("key", "", "restore this file as the signing key edge_key too")
	if err := parseFlags(flags, args, 1); err != nil {
		return err
	}
	src, data := flags.Arg(0), *dataDir
	if err := checkDataDir(data); err != nil {
		return err
	}
	unlock, err := lockPath(filepath.Join(data, lockFile), exclusive)
	if errors.Is(err, errLocked) {
		err = fmt.Errorf("%w; an edge is running on %s: stop it first (docker compose stop, or sudo systemctl stop aether-edge), then restore", err, data)
	}
	if err != nil {
		return err
	}
	defer unlock()
	unlockDB, err := lockPath(filepath.Join(data, dbLockFile), exclusive)
	if errors.Is(err, errLocked) {
		err = fmt.Errorf("%w; another aether-edge command has %s open: wait for it to finish, then restore", err, filepath.Join(data, "edge.db"))
	}
	if err != nil {
		return err
	}
	defer unlockDB()
	if err = removeStaleKeys(data); err != nil {
		return err
	}

	keyDst := filepath.Join(data, keyFile)
	var key []byte
	var fp string
	if *keyPath != "" {
		if key, fp, err = readKey(*keyPath); err != nil {
			return err
		}
	} else if _, err = os.Stat(keyDst); err != nil {
		return fmt.Errorf("edge key: %w; restore it too with --key <file>: the edge does not make a new key beside a database that holds accounts or servers", err)
	}
	db := filepath.Join(data, "edge.db")
	staged, s, err := edgestore.StageRestore(src, db)
	if err != nil {
		return err
	}
	defer os.Remove(staged) //nolint:errcheck // gone once in place
	var stagedKey string
	if key != nil {
		f, tmpErr := os.CreateTemp(data, keyFile+".*.tmp")
		if tmpErr != nil {
			return tmpErr
		}
		stagedKey = f.Name()
		defer os.Remove(stagedKey) //nolint:errcheck // gone once in place
		if err = writeFile(f, bytes.NewReader(key)); err != nil {
			return err
		}
	}

	backups := filepath.Join(data, edgestore.BackupDir)
	if err = os.MkdirAll(backups, 0o700); err != nil {
		return err
	}
	stamp := edgestore.Stamp()
	var saved []string
	for _, f := range []struct {
		src, dst string
		want     bool
	}{
		{db, "edge-before-restore-" + stamp + ".db", true},
		{db + "-wal", "edge-before-restore-" + stamp + ".db-wal", true},
		{keyDst, "edge_key-before-restore-" + stamp, key != nil},
	} {
		if !f.want {
			continue
		}
		dst := filepath.Join(backups, f.dst)
		if err = copyNew(f.src, dst); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("save the current %s: %w", f.src, err)
		}
		saved = append(saved, dst)
	}
	if err = edgestore.SyncDir(backups); err != nil {
		return err
	}

	if err = edgestore.FinishRestore(staged, db); err != nil {
		return fmt.Errorf("%w; %s was not replaced", err, db)
	}
	msg := fmt.Sprintf("restored %s from %s (schema %d)\n", db, src, s.Version)
	if stagedKey != "" {
		if err = os.Rename(stagedKey, keyDst); err != nil {
			return fmt.Errorf("%s is restored, but not the edge key: %w", db, err)
		}
		if err = edgestore.SyncDir(data); err != nil {
			return err
		}
		msg += fmt.Sprintf("restored the edge key %s from %s to %s\n", fp, *keyPath, keyDst)
	}
	if len(saved) > 0 {
		msg += "saved the files it replaced: " + strings.Join(saved, ", ") + "\n"
	}
	_, err = io.WriteString(out, msg)
	return err
}

// lockDB takes the data directory's edge.db.lock shared, so a restore
// cannot replace edge.db while it is open, and returns its release.
func lockDB(dataDir string) (func(), error) {
	unlock, err := lockPath(filepath.Join(dataDir, dbLockFile), shared)
	if errors.Is(err, errLocked) {
		err = fmt.Errorf("%w; a restore is replacing %s: run the command again once it finishes", err, filepath.Join(dataDir, "edge.db"))
	}
	return unlock, err
}

// removeStaleKeys removes the staged keys a restore left behind when it
// stopped before renaming one into place. The caller holds edge.lock.
func removeStaleKeys(dataDir string) error {
	stale, err := filepath.Glob(filepath.Join(dataDir, keyFile+".*.tmp"))
	if err != nil {
		return err
	}
	for _, f := range stale {
		if err = os.Remove(f); err != nil {
			return err
		}
	}
	return nil
}

// copyNew copies the file at src, byte for byte, so a database too damaged
// to read is kept too, to a new 0600 file.
func copyNew(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // read-only
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return writeFile(f, in)
}

// writeNew writes data to a new 0600 file.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return writeFile(f, bytes.NewReader(data))
}

// writeFile fills the new file f from r, syncs and closes it, and removes
// it if that fails.
func writeFile(f *os.File, r io.Reader) error {
	_, err := io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(f.Name()) //nolint:errcheck // the write error takes precedence
		return fmt.Errorf("write %s: %w", f.Name(), err)
	}
	return nil
}

// readKey reads an edge signing key and returns it with its fingerprint.
func readKey(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("edge key: %w", err)
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, "", fmt.Errorf("edge key %s: %w", path, err)
	}
	key, ok := raw.(*ed25519.PrivateKey)
	if !ok {
		return nil, "", fmt.Errorf("edge key %s is %T, want an Ed25519 key", path, raw)
	}
	return data, edgeproto.EdgeKeyFingerprint(key.Public().(ed25519.PublicKey)), nil
}

// edgeDB is the path of the data directory's existing database. It
// refuses any other user than the database's owner: SQLite creates its
// -wal and -shm files as whoever opens the database first, and a
// root-owned one would lock the edge out of its own database.
func edgeDB(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "edge.db")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("no edge database: %w; name the data directory with --data or AETHER_EDGE_DATA", err)
	}
	return path, checkOwner(path, info)
}
func parseFlags(flags *flag.FlagSet, args []string, want int) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != want {
		return fmt.Errorf("%s takes %d argument(s) after its flags, got %d", flags.Name(), want, flags.NArg())
	}
	return nil
}
