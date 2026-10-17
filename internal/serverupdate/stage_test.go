package serverupdate

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// Stage is what `aether-server update` runs with no service behind it: it
// replaces both binaries, leaves a verifying record, and refuses while a
// previous update is still verifying.
func stagePlan(e *env) Plan {
	return Plan{
		BaseURL:  e.release.URL,
		Self:     e.self,
		StateDir: e.state,
		Database: filepath.Join(e.dir, "aether.db"),
		Schema:   e.db.SchemaVersion,
		Now:      func() time.Time { return e.now },
	}
}

func TestStageReplacesBinariesAndRefusesWhileVerifying(t *testing.T) {
	e := newEnv(t, false)
	plan := stagePlan(e)
	staged, err := Stage(t.Context(), plan, releaseTagUnderTest)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if got := e.read(t, e.self); got != newServerBinary {
		t.Fatalf("aether-server = %q, want the new binary", got)
	}
	if got := e.read(t, e.sibling); got != newCLIBinary {
		t.Fatalf("aether = %q, want the new binary", got)
	}
	if len(staged.Binaries) != 2 {
		t.Fatalf("Binaries = %v, want the server and the aether beside it", staged.Binaries)
	}
	out, err := ReadOutcome(e.state)
	if err != nil || out.Settled || out.Phase != recordVerifying {
		t.Fatalf("outcome = %+v, %v; want a verifying record", out, err)
	}
	if _, err := Stage(t.Context(), plan, releaseTagUnderTest); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Stage = %v, want ErrBusy while the first is verifying", err)
	}
}

func TestLockRefusesASecondHolderUntilUnlocked(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if _, err = Lock(dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Lock = %v, want ErrBusy", err)
	}
	unlock()
	again, err := Lock(dir)
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	again()
}

// The server's own update shares the lock `aether-server update` holds, so
// it refuses before it changes anything.
func TestUpdateRefusesWhileTheLockIsHeld(t *testing.T) {
	e := newEnv(t, false)
	unlock, err := Lock(e.state)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	now := protocol.ServerUpdateParams{Version: "v0.2.0", When: protocol.ServerUpdateNow}
	if _, _, err = e.svc.Update(t.Context(), "mem_admin", now); !errors.Is(err, ErrBusy) {
		t.Fatalf("Update = %v, want ErrBusy while the lock is held", err)
	}
	if got := e.read(t, e.self); got != "the old binary" {
		t.Fatalf("aether-server = %q, want it untouched", got)
	}
	unlock()
	if _, _, err = e.svc.Update(t.Context(), "mem_admin", now); err != nil {
		t.Fatalf("Update after unlock: %v", err)
	}
}

// A start systemd refuses leaves no process to roll the trial back, so
// `aether-server update` rolls it back itself.
func TestStagedRollBackRestoresThePreviousRelease(t *testing.T) {
	e := newEnv(t, false)
	staged, err := Stage(t.Context(), stagePlan(e), releaseTagUnderTest)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err = staged.RollBack("systemctl start aether-server: exit status 1"); err != nil {
		t.Fatalf("RollBack: %v", err)
	}
	for _, path := range staged.Binaries {
		if got := e.read(t, path); got != "the old binary" {
			t.Fatalf("%s = %q, want the previous binary", path, got)
		}
	}
	out, err := ReadOutcome(e.state)
	if err != nil || !out.RolledBack || out.Error != "systemctl start aether-server: exit status 1" {
		t.Fatalf("outcome = %+v, %v; want rolled back with the start error", out, err)
	}
	if _, err = Stage(t.Context(), stagePlan(e), releaseTagUnderTest); err != nil {
		t.Fatalf("Stage after the rollback: %v", err)
	}
}
