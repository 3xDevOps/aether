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

// trialDeadline is how long the new binary has, from its first start, to
// open and migrate the store and bind its listeners.
const trialDeadline = 5 * time.Minute

// Record phases. The record outlives the swap so the status call can name
// both versions of the last update.
const (
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
	// Snapshot is a copy of Database taken at SchemaBefore.
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
	if err != nil {
		return fmt.Errorf("serverupdate: write %s: %w", path, err)
	}
	return nil
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

// rollBack restores the previous binaries, and the database snapshot when
// the new binary already migrated past it, then records cause. The store
// must not be open in this process.
func rollBack(dir string, rec *swapRecord, cause string) error {
	errs := []error{restorePrevious(rec.Binaries)}
	if rec.Snapshot != "" {
		current, err := store.ReadSchemaVersion(rec.Database)
		// An unreadable database is restored too: the old binary cannot
		// use it either way.
		if err != nil || current > rec.SchemaBefore {
			errs = append(errs, restoreDatabase(rec.Database, rec.Snapshot))
		}
	}
	rec.Phase, rec.Error, rec.Recorded = recordRolledBack, cause, false
	if err := errors.Join(errs...); err != nil {
		rec.Error = cause + "; rollback incomplete: " + err.Error()
	}
	return saveRecord(dir, rec)
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

// Trial is one server start seen against the last update's swap record.
// cmd/aether-server begins it before it opens the store, commits it once
// the server serves, and fails it when the start does not get that far.
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
// and the service manager starts the restored binary. A start of a binary the record
// does not expect - the operator put another one in place - closes the
// trial as rolled back by hand.
func Boot(dir string, host Host) (*Trial, error) {
	t := &Trial{dir: dir, host: host}
	rec, err := loadRecord(dir)
	if err != nil {
		// Refusing to start over an unreadable record would turn a torn
		// file into an outage; the start is simply not a trial.
		slog.Error("serverupdate: ignoring the swap record", "error", err)
		return t, nil
	}
	if rec == nil || rec.Phase != recordVerifying {
		t.rec = rec
		return t, nil
	}
	if version.Version != rec.ToVersion {
		rec.Phase, rec.Recorded = recordRolledBack, false
		rec.Error = fmt.Sprintf("aether-server %s started instead of %s before the update finished", version.Version, rec.ToVersion)
		t.rec = rec
		return t, saveRecord(dir, rec)
	}
	rec.Boots++
	now := time.Now().UTC()
	if rec.Deadline.IsZero() {
		rec.Deadline = now.Add(trialDeadline)
	}
	t.rec = rec
	switch {
	case rec.Boots > maxTrialBoots:
		cause := fmt.Sprintf("%s started %d times without finishing its boot", rec.ToVersion, rec.Boots-1)
		if rec.Error != "" {
			cause += ": " + rec.Error
		}
		return t, t.rollBackAndExec(cause)
	case now.After(rec.Deadline):
		return t, t.rollBackAndExec(fmt.Sprintf("%s did not finish its boot within %s", rec.ToVersion, trialDeadline))
	}
	return t, saveRecord(dir, rec)
}

// Deadline is when a trial start must have committed; zero when this start
// is not a trial.
func (t *Trial) Deadline() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec == nil || t.rec.Phase != recordVerifying {
		return time.Time{}
	}
	return t.rec.Deadline
}

// Fail records why a trial start stopped before it committed, so the
// start that rolls back can report the real error, and returns cause. The
// service manager restarts the binary; a start past maxTrialBoots or the
// deadline rolls back.
func (t *Trial) Fail(cause error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec == nil || t.rec.Phase != recordVerifying {
		return cause
	}
	t.rec.Error = cause.Error()
	return errors.Join(cause, saveRecord(t.dir, t.rec))
}

func (t *Trial) rollBackAndExec(cause string) error {
	slog.Error("serverupdate: rolling back", "from", t.rec.ToVersion, "to", t.rec.FromVersion, "cause", cause)
	if err := rollBack(t.dir, t.rec, cause); err != nil {
		return err
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
// one records the rollback. Either way the outcome is written once.
func (t *Trial) Commit(ctx context.Context, st store.Store, bus events.Bus) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec := t.rec
	if rec == nil || rec.Recorded {
		return nil
	}
	outcome, phase := store.ServerUpdateRolledBack, events.ServerUpdateRolledBack
	if rec.Phase == recordVerifying {
		rec.Phase, rec.Error = recordApplied, ""
		outcome, phase = store.ServerUpdateApplied, events.ServerUpdateApplied
	}
	rec.At = time.Now().UTC()
	if err := st.SetLastServerUpdate(ctx, store.ServerUpdateAttempt{
		Version: rec.ToVersion, Outcome: outcome, Detail: rec.Error, At: rec.At,
	}); err != nil {
		return err
	}
	publish(ctx, st, bus, events.ServerUpdatePayload{
		Phase: phase, Version: rec.ToVersion, FromVersion: rec.FromVersion,
		ActorID: domain.MemberID(rec.RequestedBy), Detail: rec.Error,
	})
	rec.Recorded = true
	if rec.Snapshot != "" && outcome == store.ServerUpdateApplied {
		// The new binary owns the database now; the snapshot stays as the
		// operator's way back until the next update replaces it.
		slog.Info("serverupdate: pre-update database snapshot kept", "path", rec.Snapshot)
	}
	return saveRecord(t.dir, rec)
}

// ExecAfterDrain re-executes argv once the server has shut down for an
// update. It returns only when the exec failed; the swap is then rolled
// back and the previous binary executed instead, and the returned error
// says both what failed and what was restored.
func ExecAfterDrain(dir string, host Host, argv []string) error {
	err := host.Exec(argv[0], argv, os.Environ())
	if err == nil {
		err = errors.New("returned without replacing the process")
	}
	cause := fmt.Errorf("re-exec %s: %w", argv[0], err)
	rec, lerr := loadRecord(dir)
	if lerr != nil || rec == nil || rec.Phase != recordVerifying {
		return errors.Join(cause, lerr)
	}
	t := &Trial{dir: dir, host: host, rec: rec}
	return errors.Join(cause, t.rollBackAndExec(cause.Error()))
}
