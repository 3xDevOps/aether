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
	if !trial.Deadline().IsZero() {
		t.Fatal("a start with no swap record has a deadline")
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
		if trial.Deadline().IsZero() {
			t.Fatal("a trial start has no deadline")
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

// The database copy goes back only when the new binary already migrated
// past it; otherwise writes made since the copy would be lost for nothing.
func TestRollbackRestoresTheDatabaseOnlyWhenTheSchemaMoved(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		e := newTrialEnv(t)
		dbPath := filepath.Join(e.dir, "aether.db")
		db, err := storetest.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		before, err := db.SchemaVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(e.state, 0o700); err != nil {
			t.Fatal(err)
		}
		snap := snapshotPath(e.state)
		if err := db.Snapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if migrated {
			raw, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, before+1); err != nil {
				t.Fatal(err)
			}
			_ = raw.Close()
		}
		e.write(t, swapRecord{Boots: maxTrialBoots, Database: dbPath, Snapshot: snap, SchemaBefore: before})
		if _, err := Boot(e.state, e.host); err == nil {
			t.Fatal("expected a rollback")
		}
		got, err := store.ReadSchemaVersion(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != before {
			t.Fatalf("migrated=%v: schema after rollback = %d, want %d", migrated, got, before)
		}
		if _, err := os.Stat(snap); migrated == (err == nil) {
			t.Fatalf("migrated=%v: snapshot present = %v", migrated, err == nil)
		}
	}
}

func TestCommitRecordsAppliedOnceFromTheNewProcess(t *testing.T) {
	e := newTrialEnv(t)
	e.write(t, swapRecord{RequestedBy: "mem-admin"})
	db, bus := commitDeps(t, e)
	trial, err := Boot(e.state, e.host)
	if err != nil {
		t.Fatal(err)
	}
	if err := trial.Commit(t.Context(), db, bus); err != nil {
		t.Fatal(err)
	}
	if !trial.Deadline().IsZero() {
		t.Fatal("a committed start still has a deadline")
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
	if err != nil || !again.Deadline().IsZero() {
		t.Fatalf("start after commit: deadline %v, err %v", again.Deadline(), err)
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
	if err := trial.Commit(t.Context(), db, bus); err != nil {
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
