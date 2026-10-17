package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
	"github.com/3xDevOps/Aether/internal/version"
)

const (
	lockFile = "edge.lock"
	keyFile  = "edge_key"
)

// errLocked reports a data directory whose edge.lock another process
// holds: an edge serving it, or an operator command.
var errLocked = errors.New("another aether-edge process holds the data directory's lock")

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
		dst = filepath.Join(target, fmt.Sprintf("edge-v%d-%s.db", cur.Version, time.Now().UTC().Format(edgestore.StampLayout)))
		dir = target
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	s, err := edgestore.Backup(db, dst)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(out, "backed up %s (schema %d) to %s\n", db, s.Version, dst); err != nil {
		return err
	}
	if !*withKey {
		return nil
	}
	if err = writeNew(dst+".key", key); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "backed up the edge key %s to %s.key\n", fp, dst)
	return err
}

// backupToStdout copies the database through a file in dir, which is on
// the data volume, so it needs no writable root or /tmp.
func backupToStdout(db, dir string, out, errOut io.Writer) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("stdout-%d.tmp", os.Getpid()))
	defer os.Remove(tmp) //nolint:errcheck // a leftover is removed with the next migration backup
	s, err := edgestore.Backup(db, tmp)
	if err != nil {
		return err
	}
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
	src := flags.Arg(0)
	if err := checkDataDir(*dataDir); err != nil {
		return err
	}
	unlock, err := lockData(*dataDir)
	if errors.Is(err, errLocked) {
		return fmt.Errorf("%w; an edge is running on %s: stop it first (docker compose stop, or sudo systemctl stop aether-edge), then restore",
			err, *dataDir)
	}
	if err != nil {
		return err
	}
	defer unlock()
	var key []byte
	var fp string
	if *keyPath != "" {
		if key, fp, err = readKey(*keyPath); err != nil {
			return err
		}
	}
	db := filepath.Join(*dataDir, "edge.db")
	saved := filepath.Join(*dataDir, edgestore.BackupDir, "edge-before-restore-"+time.Now().UTC().Format(edgestore.StampLayout)+".db")
	_, err = os.Stat(db)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		saved = ""
	case err != nil:
		return err
	default:
		if err = saveDatabase(db, saved); err != nil {
			return err
		}
	}
	s, err := edgestore.Restore(src, db)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("restored %s from %s (schema %d)", db, src, s.Version)
	if saved != "" {
		msg += "; the database it replaced is " + saved
	}
	if _, err = fmt.Fprintln(out, msg); err != nil {
		return err
	}
	if key == nil {
		return nil
	}
	dst := filepath.Join(*dataDir, keyFile)
	tmp := dst + ".tmp"
	if err = os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err = writeNew(tmp, key); err != nil {
		return err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "restored the edge key %s from %s to %s\n", fp, *keyPath, dst)
	return err
}

// saveDatabase copies the database and its WAL, byte for byte, so a
// database too damaged to read is kept too.
func saveDatabase(db, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", ""} {
		data, err := os.ReadFile(db + suffix)
		if errors.Is(err, fs.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			return fmt.Errorf("save the current database: %w", err)
		}
		if err := writeNew(dst+suffix, data); err != nil {
			return fmt.Errorf("save the current database: %w", err)
		}
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

// writeNew writes data to a new 0600 file and syncs it.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path) //nolint:errcheck // the write error takes precedence
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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
