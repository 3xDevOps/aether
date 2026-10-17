package serverupdate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
	"github.com/3xDevOps/Aether/internal/version"
)

// trialEnv is a swap as an update leaves it: the release's binaries in
// place, the previous ones beside them as .prev, and a swap record.
type trialEnv struct {
	dir, state string
	server     string
	cli        string
	execs      []string
	host       Host
}

func newTrialEnv(t *testing.T) *trialEnv {
	t.Helper()
	e := &trialEnv{dir: t.TempDir()}
	e.state = filepath.Join(e.dir, "server-update")
	e.server = filepath.Join(e.dir, "aether-server")
	e.cli = filepath.Join(e.dir, "aether")
	for path, body := range map[string]string{
		e.server: "new server", prevPath(e.server): "old server",
		e.cli: "new cli", prevPath(e.cli): "old cli",
	} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.host = Host{
		Exec: func(path string, _, _ []string) error {
			e.execs = append(e.execs, path)
			return errors.New("the test process is never replaced")
		},
		Restart:      func() error { return errors.New("no systemd in a test") },
		UnderSystemd: func() bool { return false },
	}
	return e
}

func (e *trialEnv) write(t *testing.T, rec swapRecord) {
	t.Helper()
	rec.Binaries = []string{e.server, e.cli}
	if rec.ToVersion == "" {
		rec.ToVersion = version.Version
	}
	if rec.FromVersion == "" {
		rec.FromVersion = "v0.1.0"
	}
	if rec.Phase == "" {
		rec.Phase = recordVerifying
	}
	if err := saveRecord(e.state, &rec); err != nil {
		t.Fatal(err)
	}
}

func (e *trialEnv) record(t *testing.T) *swapRecord {
	t.Helper()
	rec, err := loadRecord(e.state)
	if err != nil || rec == nil {
		t.Fatalf("swap record = %v, %v", rec, err)
	}
	return rec
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestBootWithoutARecordIsNotATrial(t *testing.T) {
	e := newTrialEnv(t)
	trial, err := Boot(e.state, e.host)
	if err != nil {
		t.Fatal(err)
	}
	if deadline, err := trial.Migrated(); err != nil || !deadline.IsZero() {
		t.Fatalf("a start with no swap record has a deadline %v (%v)", deadline, err)
	}
	cause := errors.New("bind: address already in use")
	if err := trial.Fail(cause); err != cause {
		t.Fatalf("Fail = %v, want the cause unchanged", err)
	}
	if len(e.execs) != 0 || readFile(t, e.server) != "new server" {
		t.Fatal("a start that is not a trial rolled something back")
	}
}

// A new binary that keeps exiting before it commits is rolled back on the
// start after maxTrialBoots: the previous binaries come back and run.
func TestBootRollsBackAfterRepeatedUncommittedStarts(t *testing.T) {
	e := newTrialEnv(t)
	e.write(t, swapRecord{Error: "store: apply migration 91: boom"})
	for range maxTrialBoots {
		trial, err := Boot(e.state, e.host)
		if err != nil {
			t.Fatalf("boot within the limit: %v", err)
		}
		if deadline, merr := trial.Migrated(); merr != nil || deadline.IsZero() {
			t.Fatalf("a trial start has no deadline (%v)", merr)
		}
	}
	_, err := Boot(e.state, e.host)
	if err == nil {
		t.Fatal("the start past the limit did not roll back")
	}
	if readFile(t, e.server) != "old server" || readFile(t, e.cli) != "old cli" {
		t.Fatal("the previous binaries were not restored")
	}
	if len(e.execs) != 1 || e.execs[0] != e.server {
		t.Fatalf("execs = %v, want the restored server", e.execs)
	}
	rec := e.record(t)
	if rec.Phase != recordRolledBack || rec.Recorded {
		t.Fatalf("record = %+v, want rolled_back not yet recorded", rec)
	}
	if !strings.Contains(rec.Error, "started 2 times without finishing its boot") || !strings.Contains(rec.Error, "store: apply migration 91: boom") {
		t.Fatalf("error = %q, want the count and the last real error", rec.Error)
	}
}

func TestBootRollsBackPastTheDeadline(t *testing.T) {
	e := newTrialEnv(t)
	e.write(t, swapRecord{Boots: 1, Deadline: time.Now().Add(-time.Minute)})
	if _, err := Boot(e.state, e.host); err == nil {
		t.Fatal("a start past the deadline did not roll back")
	}
	if rec := e.record(t); rec.Phase != recordRolledBack || !strings.Contains(rec.Error, "did not finish its boot within") {
		t.Fatalf("record = %+v", rec)
	}
}

// A failed trial start exits with its error; the start that rolls back
// reports it.
func TestFailedStartsRollBackWithTheRealError(t *testing.T) {
	e := newTrialEnv(t)
	e.write(t, swapRecord{})
	cause := errors.New("server: sshd: listen tcp :2222: bind: address already in use")
	for range maxTrialBoots {
		trial, err := Boot(e.state, e.host)
		if err != nil {
			t.Fatal(err)
		}
		if err := trial.Fail(cause); !errors.Is(err, cause) {
			t.Fatalf("Fail = %v, want the cause", err)
		}
		if readFile(t, e.server) != "new server" {
			t.Fatal("a failed start within the limit rolled back")
		}
	}
	if _, err := Boot(e.state, e.host); err == nil {
		t.Fatal("expected a rollback")
	}
	if readFile(t, e.server) != "old server" {
		t.Fatal("the previous server binary was not restored")
	}
	if rec := e.record(t); !strings.Contains(rec.Error, cause.Error()) {
		t.Fatalf("recorded error = %q, want it to carry %q", rec.Error, cause)
	}
}

// database is a store under e.dir with a snapshot of it at its current
// schema, and, when migrated, a later migration applied as a new binary
// would.
func (e *trialEnv) database(t *testing.T, migrated bool) (dbPath, snap string, before int) {
	t.Helper()
	dbPath = filepath.Join(e.dir, "aether.db")
	db, err := storetest.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if before, err = db.SchemaVersion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(e.state, 0o700); err != nil {
		t.Fatal(err)
	}
	snap = snapshotPath(e.state)
	if err = store.SnapshotFile(dbPath, snap); err != nil {
		t.Fatal(err)
	}
	if migrated {
		raw, oerr := sql.Open("sqlite", dbPath)
		if oerr != nil {
			t.Fatal(oerr)
		}
		if _, oerr = raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, before+1); oerr != nil {
			t.Fatal(oerr)
		}
		_ = raw.Close()
	}
	return dbPath, snap, before
}

func schemaOf(t *testing.T, dbPath string) int {
	t.Helper()
	got, err := store.ReadSchemaVersion(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The database copy goes back only when the new binary already migrated
// past it; otherwise writes made since the copy would be lost for nothing.
func TestRollbackRestoresTheDatabaseOnlyWhenTheSchemaMoved(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		e := newTrialEnv(t)
		dbPath, snap, before := e.database(t, migrated)
		e.write(t, swapRecord{Boots: maxTrialBoots, Database: dbPath, Snapshot: snap, SchemaBefore: before})
		if _, err := Boot(e.state, e.host); err == nil {
			t.Fatal("expected a rollback")
		}
		if got := schemaOf(t, dbPath); got != before {
			t.Fatalf("migrated=%v: schema after rollback = %d, want %d", migrated, got, before)
		}
		if _, err := os.Stat(snap); migrated == (err == nil) {
			t.Fatalf("migrated=%v: snapshot present = %v", migrated, err == nil)
		}
		if restored := strings.Contains(e.record(t).Error, "database restored to its snapshot of"); restored != migrated {
			t.Fatalf("migrated=%v: error %q, want the restore named only when it happened", migrated, e.record(t).Error)
		}
	}
}

// The database goes back before the binaries, and a rollback that cannot
// put a binary back records it and executes nothing: the file at the
// server's path may still be the release that failed.
func TestAnIncompleteRollbackDoesNotExecute(t *testing.T) {
	e := newTrialEnv(t)
	dbPath, snap, before := e.database(t, true)
	if err := os.Remove(prevPath(e.server)); err != nil {
		t.Fatal(err)
	}
	e.write(t, swapRecord{Boots: maxTrialBoots, Database: dbPath, Snapshot: snap, SchemaBefore: before})
	_, err := Boot(e.state, e.host)
	if err == nil || !strings.Contains(err.Error(), "restore "+e.server) {
		t.Fatalf("Boot = %v, want the failed restore named", err)
	}
	if len(e.execs) != 0 {
		t.Fatalf("execs = %v, want none after an incomplete rollback", e.execs)
	}
	if got := schemaOf(t, dbPath); got != before {
		t.Fatalf("schema = %d, want the database restored to %d first", got, before)
	}
	if rec := e.record(t); rec.Phase != recordRolledBack || !strings.Contains(rec.Error, "rollback incomplete: restore "+e.server) {
		t.Fatalf("record = %+v, want the incomplete rollback recorded", rec)
	}
}

// The previous binary started by hand, or by a rollback cut short after
// the binaries, must not run on the schema the new one migrated to.
func TestThePreviousBinaryRestoresAMigratedDatabase(t *testing.T) {
	setVersion(t, "v0.1.0")
	e := newTrialEnv(t)
	dbPath, snap, before := e.database(t, true)
	e.write(t, swapRecord{ToVersion: "v0.2.0", FromVersion: "v0.1.0", Database: dbPath, Snapshot: snap, SchemaBefore: before})
	if _, err := Boot(e.state, e.host); err != nil {
		t.Fatal(err)
	}
	if got := schemaOf(t, dbPath); got != before {
		t.Fatalf("schema = %d, want %d", got, before)
	}
	if rec := e.record(t); rec.Phase != recordRolledBack || !strings.Contains(rec.Error, "database restored to its snapshot of") {
		t.Fatalf("record = %+v", rec)
	}
}

// A swap cut short between renames puts every previous binary back, and
// a start of the new server executes the restored one.
func TestBootUndoesAnInterruptedSwap(t *testing.T) {
	for _, running := range []string{"v0.1.0", "v0.2.0"} {
		setVersion(t, running)
		e := newTrialEnv(t)
		e.write(t, swapRecord{Phase: recordSwapping, ToVersion: "v0.2.0", FromVersion: "v0.1.0"})
		_, err := Boot(e.state, e.host)
		if readFile(t, e.server) != "old server" || readFile(t, e.cli) != "old cli" {
			t.Fatalf("running %s: the previous binaries were not restored", running)
		}
		if rec := e.record(t); rec.Phase != recordRolledBack || !strings.Contains(rec.Error, "stopped while it replaced the binaries") {
			t.Fatalf("running %s: record = %+v", running, rec)
		}
		if running == "v0.1.0" && (err != nil || len(e.execs) != 0) {
			t.Fatalf("the previous binary: Boot = %v, execs %v; want it to go on starting", err, e.execs)
		}
		if running == "v0.2.0" && len(e.execs) != 1 {
			t.Fatalf("the new binary: execs = %v, want the restored server executed", e.execs)
		}
	}
}

// The deadline starts once the store is migrated: a slow migration is
// never cut short, and a start that misses the deadline after it records
// why.
func TestTheDeadlineStartsAfterTheMigration(t *testing.T) {
	e := newTrialEnv(t)
	e.write(t, swapRecord{At: time.Now().Add(-time.Hour)})
	trial, err := Boot(e.state, e.host)
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.record(t); !rec.Deadline.IsZero() {
		t.Fatalf("deadline before the migration = %v, want none", rec.Deadline)
	}
	if _, err = Boot(e.state, e.host); err != nil {
		t.Fatalf("a second start an hour into a migration rolled back: %v", err)
	}
	started := time.Now()
	deadline, err := trial.Migrated()
	if err != nil {
		t.Fatal(err)
	}
	if deadline.Before(started.Add(trialDeadline-time.Second)) || deadline.After(time.Now().Add(trialDeadline)) {
		t.Fatalf("deadline = %v, want %s after the migration", deadline, trialDeadline)
	}
	if again, _ := trial.Migrated(); !again.Equal(deadline) {
		t.Fatalf("second Migrated = %v, want the first deadline kept", again)
	}
	err = trial.Expire()
	if err == nil || !strings.Contains(err.Error(), "of migrating the database") {
		t.Fatalf("Expire = %v, want the missed deadline", err)
	}
	if rec := e.record(t); rec.Error != err.Error() || !rec.Deadline.Equal(deadline) {
		t.Fatalf("record = %+v, want the deadline and the error", rec)
	}
}

// The swap record is marked applied before the outcome goes to the store,
// so a store that refuses the write never leaves a record a later start
// would roll back; that start records the outcome instead.
func TestCommitMarksTheSwapAppliedBeforeTheStore(t *testing.T) {
	e := newTrialEnv(t)
	e.write(t, swapRecord{})
	db, bus := commitDeps(t, e)
	trial, err := Boot(e.state, e.host)
	if err != nil {
		t.Fatal(err)
	}
	refusing := &refusingStore{Store: db}
	if err = trial.Commit(t.Context(), refusing, bus); err == nil {
		t.Fatal("Commit over a refusing store succeeded")
	}
	if refusing.calls != storeAttempts {
		t.Fatalf("store writes = %d, want %d", refusing.calls, storeAttempts)
	}
	if rec := e.record(t); rec.Phase != recordApplied || rec.Recorded {
		t.Fatalf("record = %+v, want applied and not yet recorded", rec)
	}
	if err = trial.Expire(); err != nil {
		t.Fatalf("Expire after the swap record was marked applied: %v", err)
	}
	for range maxTrialBoots + 1 {
		again, berr := Boot(e.state, e.host)
		if berr != nil {
			t.Fatalf("a start after the commit rolled back: %v", berr)
		}
		trial = again
	}
	if err = trial.Commit(t.Context(), db, bus); err != nil {
		t.Fatal(err)
	}
	state, err := db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Last == nil || state.Last.Outcome != store.ServerUpdateApplied || !e.record(t).Recorded {
		t.Fatalf("last = %+v, want applied recorded by the later start", state.Last)
	}
}

type refusingStore struct {
	store.Store
	calls int
}

func (r *refusingStore) SetLastServerUpdate(context.Context, store.ServerUpdateAttempt) error {
	r.calls++
	return errors.New("database is locked")
}

func TestCommitRecordsAppliedOnceFromTheNewProcess(t *testing.T) {
	e := newTrialEnv(t)
	snap := snapshotPath(e.state)
	e.write(t, swapRecord{RequestedBy: "mem-admin", Snapshot: snap})
	if err := os.WriteFile(snap, []byte("the old database"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, bus := commitDeps(t, e)
	trial, err := Boot(e.state, e.host)
	if err != nil {
		t.Fatal(err)
	}
	if err = trial.Commit(t.Context(), db, bus); err != nil {
		t.Fatal(err)
	}
	if err = trial.Expire(); err != nil {
		t.Fatalf("a committed start expired: %v", err)
	}
	if _, err = os.Stat(snap); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the pre-update snapshot outlived the commit: %v", err)
	}
	state, err := db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Last == nil || state.Last.Outcome != store.ServerUpdateApplied || state.Last.Version != version.Version {
		t.Fatalf("last = %+v, want applied %s", state.Last, version.Version)
	}
	if rec := e.record(t); rec.Phase != recordApplied || !rec.Recorded {
		t.Fatalf("record = %+v", rec)
	}
	// The next start is an ordinary one.
	again, err := Boot(e.state, e.host)
	if err != nil {
		t.Fatal(err)
	}
	if deadline, _ := again.Migrated(); !deadline.IsZero() {
		t.Fatalf("start after commit has a deadline %v", deadline)
	}
}

// A binary the record does not expect - the operator restored the old
// one by hand - closes the trial as rolled back and records it.
func TestBootOfAnotherBinaryRecordsARollbackByHand(t *testing.T) {
	e := newTrialEnv(t)
	e.write(t, swapRecord{ToVersion: "v9.9.9"})
	db, bus := commitDeps(t, e)
	trial, err := Boot(e.state, e.host)
	if err != nil {
		t.Fatal(err)
	}
	if err = trial.Commit(t.Context(), db, bus); err != nil {
		t.Fatal(err)
	}
	state, err := db.GetServerUpdate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Last == nil || state.Last.Outcome != store.ServerUpdateRolledBack || !strings.Contains(state.Last.Detail, "started instead of v9.9.9") {
		t.Fatalf("last = %+v, want a rollback naming v9.9.9", state.Last)
	}
	if len(e.execs) != 0 {
		t.Fatal("a rollback by hand re-executed")
	}
}

func commitDeps(t *testing.T, e *trialEnv) (*store.DB, events.Bus) {
	t.Helper()
	db, err := storetest.Open(filepath.Join(e.dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, newTestBus(t)
}

// Under a unit that restarts on status 75, an update must leave through
// systemd instead of re-executing, so the restart shows in the journal.
func TestRestartThroughTheUnitInsteadOfExec(t *testing.T) {
	e := newTrialEnv(t)
	e.write(t, swapRecord{})
	e.host.RestartsOnExit75 = func() bool { return true }
	runnable := filepath.Join(t.TempDir(), "aether-server")
	if err := os.WriteFile(runnable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := ExecAfterDrain(e.state, e.host, []string{runnable, "serve"})
	if !errors.Is(err, ErrRestartUnit) || !strings.Contains(err.Error(), version.Version) {
		t.Fatalf("ExecAfterDrain = %v, want ErrRestartUnit naming %s", err, version.Version)
	}
	f := newTrialEnv(t)
	f.host.RestartsOnExit75 = func() bool { return true }
	f.write(t, swapRecord{})
	broken := filepath.Join(t.TempDir(), "aether-server")
	if err = os.WriteFile(broken, []byte("#!/bin/sh\necho exec format\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = ExecAfterDrain(f.state, f.host, []string{broken, "serve"})
	if !errors.Is(err, ErrRestartUnit) || !strings.Contains(err.Error(), "does not run") || !strings.Contains(err.Error(), "exec format") ||
		readFile(t, f.server) != "old server" || f.record(t).Phase != recordRolledBack {
		t.Fatalf("a new binary that does not run was not rolled back: %v", err)
	}
	e.write(t, swapRecord{Boots: maxTrialBoots})
	if _, err = Boot(e.state, e.host); !errors.Is(err, ErrRestartUnit) {
		t.Fatalf("rollback Boot = %v, want ErrRestartUnit", err)
	}
	if readFile(t, e.server) != "old server" || e.record(t).Phase != recordRolledBack {
		t.Fatal("the rollback did not restore the binaries and record itself")
	}
	if len(e.execs) != 0 {
		t.Fatalf("execs = %v, want none", e.execs)
	}

	e.write(t, swapRecord{})
	e.host.RestartsOnExit75 = nil
	if err = ExecAfterDrain(e.state, e.host, []string{e.server, "serve"}); errors.Is(err, ErrRestartUnit) || len(e.execs) == 0 {
		t.Fatalf("a foreground server must exec in place: err = %v, execs = %v", err, e.execs)
	}
}

func TestRestartsOnExit75(t *testing.T) {
	for show, want := range map[string]bool{
		"Restart=on-failure\nRestartForceExitStatus=\nSuccessExitStatus=\n":     true,
		"Restart=on-failure\nRestartForceExitStatus=75\nSuccessExitStatus=75\n": true,
		"Restart=on-failure\nRestartForceExitStatus=\nSuccessExitStatus=75\n":   false,
		"Restart=always\nRestartForceExitStatus=\nSuccessExitStatus=75\n":       true,
		"Restart=no\nRestartForceExitStatus=\nSuccessExitStatus=\n":             false,
		"Restart=no\nRestartForceExitStatus=75\nSuccessExitStatus=\n":           true,
		"Restart=always\nRestartPreventExitStatus=75\n":                         false,
		"": false,
	} {
		if got := restartsOnExit75(show); got != want {
			t.Errorf("restartsOnExit75(%q) = %v, want %v", show, got, want)
		}
	}
}
