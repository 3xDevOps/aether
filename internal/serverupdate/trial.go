package serverupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// maxTrialBoots is how many times the new binary may start without
// committing before the next start rolls it back.
const maxTrialBoots = 2

// trialDeadline is how long the new binary has, once it has migrated the
// store, to bind its listeners and commit.
const trialDeadline = 5 * time.Minute

// storeAttempts is how many times Commit tries to record the outcome.
const storeAttempts = 3

// Record phases. The record outlives the swap so the status call can name
// both versions of the last update.
const (
	recordSwapping   = "swapping"
	recordVerifying  = "verifying"
	recordApplied    = "applied"
	recordRolledBack = "rolled_back"
)

// swapRecord is what an update swapped, written before the restart. It is
// a file rather than a store row because the process that has to read it
// may be the old binary after a rollback, which must decide before it
// opens a database the new binary may have migrated.
type swapRecord struct {
	Phase       string `json:"phase"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	RequestedBy string `json:"requested_by,omitempty"`
	// Binaries are the replaced files, the server first; each previous
	// file sits beside it as <path>.prev.
	Binaries []string `json:"binaries"`
	// Database and Snapshot are set when the release migrates the schema:
	// Snapshot is a copy of Database at SchemaBefore, taken once the server
	// has drained, right before the exec.
	Database     string    `json:"database,omitempty"`
	Snapshot     string    `json:"snapshot,omitempty"`
	SchemaBefore int       `json:"schema_before,omitempty"`
	Boots        int       `json:"boots"`
	Deadline     time.Time `json:"deadline,omitzero"`
	Error        string    `json:"error,omitempty"`
	At           time.Time `json:"at"`
	// Recorded is set once the outcome is in the store and on the feed.
	Recorded bool `json:"recorded"`
}

func recordPath(dir string) string   { return filepath.Join(dir, "swap.json") }
func snapshotPath(dir string) string { return filepath.Join(dir, "aether.db.pre-update") }
func prevPath(path string) string    { return path + ".prev" }

func loadRecord(dir string) (*swapRecord, error) {
	data, err := os.ReadFile(recordPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("serverupdate: read %s: %w", recordPath(dir), err)
	}
	var rec swapRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("serverupdate: parse %s: %w", recordPath(dir), err)
	}
	return &rec, nil
}

func saveRecord(dir string, rec *swapRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeFileAtomic(recordPath(dir), data)
}

// writeFileAtomic replaces path so a crash leaves either the old or the
// new content, never a torn file.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("serverupdate: create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("serverupdate: write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err == nil {
		err = syncDir(filepath.Dir(path))
	}
	if err != nil {
		return fmt.Errorf("serverupdate: write %s: %w", path, err)
	}
	return nil
}

// syncDir makes a rename in dir survive a power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// keepPrevious links each binary to <path>.prev before it is replaced, so
// the rename that installs the release leaves the old file reachable.
func keepPrevious(paths []string) error {
	for _, p := range paths {
		prev := prevPath(p)
		if err := os.Remove(prev); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", prev, err)
		}
		if err := os.Link(p, prev); err != nil {
			if cerr := copyFile(p, prev); cerr != nil {
				return fmt.Errorf("keep %s: %w", prev, errors.Join(err, cerr))
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // read-only
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// restorePrevious renames each <path>.prev back over path.
func restorePrevious(paths []string) error {
	var errs []error
	for _, p := range paths {
		if err := os.Rename(prevPath(p), p); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// rollBack restores the database snapshot when the new binary already
// migrated past it, then the previous binaries, and records cause. The
// database goes first so a rollback cut short leaves the new binary on its
// own schema, and a rerun finds the schema back at SchemaBefore. The store
// must not be open in this process. An error means the rollback is
// incomplete; it is recorded, and the caller must not execute Binaries[0].
func rollBack(dir string, rec *swapRecord, cause string) error {
	restored, err := restoreSnapshot(rec)
	if err == nil {
		err = restorePrevious(rec.Binaries)
	}
	rec.Phase, rec.Error, rec.Recorded = recordRolledBack, cause, false
	if restored != "" {
		rec.Error += "; " + restored
	}
	if err != nil {
		rec.Error += "; rollback incomplete: " + err.Error()
	}
	return errors.Join(err, saveRecord(dir, rec))
}

// restoreSnapshot puts the pre-update database back when the database is
// past SchemaBefore, and says what was restored: every write made after
// the snapshot is gone. Otherwise those writes would be lost for nothing,
// so it does nothing.
func restoreSnapshot(rec *swapRecord) (string, error) {
	if rec.Snapshot == "" {
		return "", nil
	}
	// An unreadable database is restored too: the old binary cannot use it
	// either way.
	if current, err := store.ReadSchemaVersion(rec.Database); err == nil && current <= rec.SchemaBefore {
		return "", nil
	}
	info, err := os.Stat(rec.Snapshot)
	if err != nil {
		return "", fmt.Errorf("restore %s: %w", rec.Database, err)
	}
	if err := restoreDatabase(rec.Database, rec.Snapshot); err != nil {
		return "", err
	}
	return fmt.Sprintf("database restored to its snapshot of %s; changes made after it are lost",
		info.ModTime().UTC().Format(time.RFC3339)), nil
}

// restoreDatabase puts the snapshot back. The WAL and shared-memory files
// belong to the migrated database, so they go with it.
func restoreDatabase(db, snapshot string) error {
	for _, side := range []string{db + "-wal", db + "-shm"} {
		if err := os.Remove(side); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", side, err)
		}
	}
	if err := os.Rename(snapshot, db); err != nil {
		return fmt.Errorf("restore %s from %s: %w", db, snapshot, err)
	}
	return nil
}

// takeSnapshot copies the database the swap record names, if any, once
// nothing writes to it any more.
func takeSnapshot(rec *swapRecord) error {
	if rec.Snapshot == "" {
		return nil
	}
	if err := store.SnapshotFile(rec.Database, rec.Snapshot); err != nil {
		return fmt.Errorf("snapshot the database before the update: %w", err)
	}
	return nil
}

// Trial is one server start seen against the last update's swap record.
// cmd/aether-server begins it before it opens the store, starts its
// deadline once the store is migrated, commits it once the server serves,
// and fails it when the start does not get that far.
type Trial struct {
	dir  string
	host Host

	mu  sync.Mutex
	rec *swapRecord
}

// Boot reads the swap record under dir before anything else starts. A
// start of the new binary is counted, and one past maxTrialBoots or the
// deadline rolls back and re-executes the previous binary, so Boot only
// returns in that case with the error that stopped it; the caller exits
// and the service manager starts the restored binary. A start of a binary
// the record does not expect - the operator put another one in place -
// closes the trial as rolled back by hand. A swap that stopped halfway is
// undone.
func Boot(dir string, host Host) (*Trial, error) {
	t := &Trial{dir: dir, host: host}
	rec, err := loadRecord(dir)
	if err != nil {
		// Refusing to start over an unreadable record would turn a torn
		// file into an outage; the start is simply not a trial.
		slog.Error("serverupdate: ignoring the swap record", "error", err)
		return t, nil
	}
	t.rec = rec
	switch {
	case rec == nil:
		return t, nil
	case rec.Phase == recordSwapping:
		cause := fmt.Sprintf("the update to %s stopped while it replaced the binaries", rec.ToVersion)
		if version.Version == rec.FromVersion {
			return t, rollBack(dir, rec, cause)
		}
		return t, t.rollBackAndExec(cause)
	case rec.Phase != recordVerifying:
		return t, nil
	case version.Version != rec.ToVersion:
		return t, t.closeByHand()
	}
	rec.Boots++
	switch {
	case rec.Boots > maxTrialBoots:
		cause := fmt.Sprintf("%s started %d times without finishing its boot", rec.ToVersion, rec.Boots-1)
		if rec.Error != "" {
			cause += ": " + rec.Error
		}
		return t, t.rollBackAndExec(cause)
	case !rec.Deadline.IsZero() && time.Now().After(rec.Deadline):
		return t, t.rollBackAndExec(fmt.Sprintf("%s did not finish its boot within %s of migrating the database", rec.ToVersion, trialDeadline))
	}
	return t, saveRecord(dir, rec)
}

// closeByHand records a start of a binary other than the one under trial.
// When it is the previous one, the database goes back to the snapshot
// first if the new binary migrated it, since the previous binary cannot
// run on that schema; if that fails the record stays as it is, so every
// start retries rather than serving on the migrated database.
func (t *Trial) closeByHand() error {
	rec := t.rec
	detail := fmt.Sprintf("aether-server %s started instead of %s before the update finished", version.Version, rec.ToVersion)
	if version.Version == rec.FromVersion {
		restored, err := restoreSnapshot(rec)
		if err != nil {
			return fmt.Errorf("serverupdate: %s: %w", detail, err)
		}
		if restored != "" {
			detail += "; " + restored
		}
	}
	rec.Phase, rec.Error, rec.Recorded = recordRolledBack, detail, false
	return saveRecord(t.dir, rec)
}

// Migrated starts the deadline once this start has opened and migrated the
// store, so a slow migration is never cut short, and returns it; zero when
// this start is not a trial. A later start of the same trial keeps the
// first deadline.
func (t *Trial) Migrated() (time.Time, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec == nil || t.rec.Phase != recordVerifying {
		return time.Time{}, nil
	}
	if t.rec.Deadline.IsZero() {
		next := *t.rec
		next.Deadline = time.Now().UTC().Add(trialDeadline)
		if err := saveRecord(t.dir, &next); err != nil {
			return time.Time{}, err
		}
		t.rec = &next
	}
	return t.rec.Deadline, nil
}

// Expire runs at the deadline: a start that has not committed records why
// and returns the error to exit with, so the next start rolls back. It
// returns nil once the start committed.
func (t *Trial) Expire() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec == nil || t.rec.Phase != recordVerifying {
		return nil
	}
	return t.failLocked(fmt.Errorf("%s did not finish its boot within %s of migrating the database", t.rec.ToVersion, trialDeadline))
}

// Fail records why a trial start stopped before it committed, so the
// start that rolls back can report the real error, and returns cause. The
// service manager restarts the binary; a start past maxTrialBoots or the
// deadline rolls back.
func (t *Trial) Fail(cause error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failLocked(cause)
}

func (t *Trial) failLocked(cause error) error {
	if t.rec == nil || t.rec.Phase != recordVerifying {
		return cause
	}
	t.rec.Error = cause.Error()
	return errors.Join(cause, saveRecord(t.dir, t.rec))
}

func (t *Trial) rollBackAndExec(cause string) error {
	slog.Error("serverupdate: rolling back", "from", t.rec.ToVersion, "to", t.rec.FromVersion, "cause", cause)
	if err := rollBack(t.dir, t.rec, cause); err != nil {
		return fmt.Errorf("serverupdate: %s: %w", t.rec.Error, err)
	}
	if !t.host.complete() {
		return errors.New("serverupdate: rolled back, but this build cannot re-execute; start aether-server again")
	}
	self := t.rec.Binaries[0]
	err := t.host.Exec(self, append([]string{self}, os.Args[1:]...), os.Environ())
	return fmt.Errorf("serverupdate: re-exec %s after the rollback: %w", self, err)
}

// Commit finishes the start once the store is migrated and the listeners
// are bound: the new binary records the update applied, and a restored
// one records the rollback. The swap record is marked applied before
// anything else, since a start that finds it verifying rolls back; the
// pre-update snapshot is deleted with it. The outcome then goes to the
// store and the feed once; a start that could not write it tries again.
func (t *Trial) Commit(ctx context.Context, st store.Store, bus events.Bus) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec == nil || t.rec.Recorded || t.rec.Phase == recordSwapping {
		return nil
	}
	rec := *t.rec
	rec.At = time.Now().UTC()
	outcome, phase := store.ServerUpdateRolledBack, events.ServerUpdateRolledBack
	if rec.Phase != recordRolledBack {
		outcome, phase = store.ServerUpdateApplied, events.ServerUpdateApplied
	}
	if rec.Phase == recordVerifying {
		snapshot := rec.Snapshot
		rec.Phase, rec.Error, rec.Snapshot = recordApplied, "", ""
		if err := saveRecord(t.dir, &rec); err != nil {
			return err
		}
		t.rec = &rec
		if snapshot != "" {
			if err := os.Remove(snapshot); err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Warn("serverupdate: remove the pre-update database snapshot", "path", snapshot, "error", err)
			}
		}
	}
	attempt := store.ServerUpdateAttempt{Version: rec.ToVersion, Outcome: outcome, Detail: rec.Error, At: rec.At}
	var err error
	for i := range storeAttempts {
		if err = st.SetLastServerUpdate(ctx, attempt); err == nil {
			break
		}
		if i < storeAttempts-1 {
			select {
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
	if err != nil {
		return err
	}
	publish(ctx, st, bus, events.ServerUpdatePayload{
		Phase: phase, Version: rec.ToVersion, FromVersion: rec.FromVersion,
		ActorID: domain.MemberID(rec.RequestedBy), Detail: rec.Error,
	})
	rec.Recorded = true
	t.rec = &rec
	return saveRecord(t.dir, &rec)
}

// ExecAfterDrain snapshots the database when the update migrates it and
// re-executes argv once the server has shut down for an update. It
// returns only when either failed; the swap is then rolled back and the
// previous binary executed instead, and the returned error says both what
// failed and what was restored.
func ExecAfterDrain(dir string, host Host, argv []string) error {
	rec, err := loadRecord(dir)
	if err != nil || rec == nil || rec.Phase != recordVerifying {
		return errors.Join(fmt.Errorf("serverupdate: no swap to restart onto in %s", dir), err)
	}
	cause := takeSnapshot(rec)
	if cause == nil {
		err = host.Exec(argv[0], argv, os.Environ())
		if err == nil {
			err = errors.New("returned without replacing the process")
		}
		cause = fmt.Errorf("re-exec %s: %w", argv[0], err)
	}
	t := &Trial{dir: dir, host: host, rec: rec}
	return errors.Join(cause, t.rollBackAndExec(cause.Error()))
}
