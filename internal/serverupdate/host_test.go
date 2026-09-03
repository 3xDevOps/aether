package serverupdate

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/store"
)

// A service built the way anything but cmd/aether-server builds it - no
// Host - must not be able to reach the machine it runs on. This is the
// regression test for `make test-integration` shelling out to
// `systemctl restart aether-server` on a developer's own box because the
// integration test injected Exec and left Restart to its default.
func TestServiceWithoutAHostCannotTouchTheMachine(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "aether-server")
	if err := os.WriteFile(self, []byte("the installed binary"), 0o755); err != nil {
		t.Fatalf("write %s: %v", self, err)
	}
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus := newTestBus(t)

	// A checker pointed at a dead local address, never the real release
	// feed. If the guards below ever regress, this test fails on a
	// refused connection instead of quietly reaching github.com - which
	// is what it did while Tick skipped the capability check.
	svc, err := New(Config{
		Store: db, Bus: bus, Executable: self, Checker: offlineChecker(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No host controls were supplied, so none exist to be called.
	if svc.cfg.Host.Exec != nil || svc.cfg.Host.Restart != nil || svc.cfg.Host.UnderSystemd != nil {
		t.Fatalf("a service built with no Host has host controls: %+v", svc.cfg.Host)
	}
	if svc.Capable() {
		t.Fatal("a service with no host controls reports itself capable")
	}
	for _, when := range []string{protocol.ServerUpdateNow, protocol.ServerUpdateIdle} {
		_, restart, uerr := svc.Update(t.Context(), "mem_a",
			protocol.ServerUpdateParams{Version: "v0.2.0", When: when})
		if !errors.Is(uerr, ErrIncapable) {
			t.Fatalf("Update(when=%s) error = %v, want ErrIncapable", when, uerr)
		}
		if restart != nil {
			t.Fatalf("Update(when=%s) handed back a restart", when)
		}
	}
	// A pending row planted behind the service's back must not make the
	// poll loop apply anything either. The row outlives the process, so
	// this is a real state: scheduled while capable, restarted into an
	// install that is not.
	if perr := db.SetPendingServerUpdate(t.Context(), &store.PendingServerUpdate{
		Version: "v0.2.0", RequestedBy: "mem_a", RequestedAt: svc.cfg.Now(),
	}); perr != nil {
		t.Fatalf("SetPendingServerUpdate: %v", perr)
	}
	svc.Tick(t.Context())
	if body, rerr := os.ReadFile(self); rerr != nil || string(body) != "the installed binary" {
		t.Fatalf("aether-server = %q (%v), want it untouched", body, rerr)
	}
	// The tick retires the row with the reason rather than refusing it
	// again on every poll for the rest of the process's life.
	state, serr := db.GetServerUpdate(t.Context())
	if serr != nil {
		t.Fatalf("GetServerUpdate: %v", serr)
	}
	if state.Pending != nil {
		t.Fatalf("pending = %+v, want it retired by the tick", state.Pending)
	}
	if state.Last == nil || state.Last.Outcome != store.ServerUpdateFailed {
		t.Fatalf("last = %+v, want a failed attempt", state.Last)
	}
	if !strings.Contains(state.Last.Detail, noHost) {
		t.Fatalf("last detail = %q, want it to name the missing host controls", state.Last.Detail)
	}

	status, err := svc.Status(t.Context())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Capable || status.Incapable != noHost {
		t.Fatalf("status = %+v, want incapable naming the missing host controls", status)
	}
	if len(status.ManualCommands) != len(ManualCommands()) {
		t.Fatalf("manual commands = %v, want %v", status.ManualCommands, ManualCommands())
	}
}

// A partially filled Host is treated as none at all: half the controls is
// not a state worth having, and defaulting the other half is exactly the
// bug this guards.
func TestPartialHostIsNoHost(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "aether-server")
	if err := os.WriteFile(self, []byte("x"), 0o755); err != nil {
		t.Fatalf("write %s: %v", self, err)
	}
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc, err := New(Config{
		Store:      db,
		Bus:        newTestBus(t),
		Executable: self,
		Host:       Host{Exec: func(string, []string, []string) error { return nil }},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.Capable() {
		t.Fatal("a Host with only Exec set reports itself capable")
	}
}

// HostProcess is the single opt-in. It must hand back all three controls,
// or Capable would silently stay false on a real server.
func TestHostProcessIsComplete(t *testing.T) {
	if !HostProcess().complete() {
		t.Fatal("HostProcess() is missing a control")
	}
}

// A binary that cannot be resolved disables the feature rather than
// failing construction; a server that cannot find its own executable must
// still serve runs.
func TestUnresolvableBinaryDisablesRatherThanFails(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc, err := New(Config{
		Store:      db,
		Bus:        newTestBus(t),
		Executable: filepath.Join(dir, "gone"),
		Host:       HostProcess(),
	})
	if err != nil {
		t.Fatalf("New returned an error instead of degrading: %v", err)
	}
	if svc.Capable() {
		t.Fatal("a service that could not resolve its binary reports itself capable")
	}
	status, err := svc.Status(t.Context())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(status.Incapable, "gone") {
		t.Fatalf("incapable = %q, want it to name the path that could not be resolved", status.Incapable)
	}
}

// offlineChecker points the release feed at a closed local port. Any
// attempt to reach it fails at connect, so a unit test that regressed into
// downloading a release fails loudly here instead of dialing github.com.
func offlineChecker(t *testing.T) *selfupdate.Checker {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close the reserved port: %v", err)
	}
	return selfupdate.NewChecker("http://"+addr, time.Hour)
}

// apply is the choke point: it is where a release is downloaded and a
// binary replaced, so the capability check belongs there rather than at
// each call site. Both callers are covered above - Update refuses with the
// reason, Tick retires the row - and this proves the guard is in apply
// itself, so a third caller cannot get past it.
func TestApplyRefusesOnAnIncapableServer(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "aether-server")
	if err := os.WriteFile(self, []byte("the installed binary"), 0o755); err != nil {
		t.Fatalf("write %s: %v", self, err)
	}
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc, err := New(Config{
		Store: db, Bus: newTestBus(t), Executable: self, Checker: offlineChecker(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	restart, err := svc.apply(t.Context(), store.PendingServerUpdate{
		Version: "v0.2.0", RequestedBy: "mem_a", RequestedAt: svc.cfg.Now(),
	})
	if !errors.Is(err, ErrIncapable) {
		t.Fatalf("apply error = %v, want ErrIncapable", err)
	}
	if restart != nil {
		t.Fatal("apply handed back a restart on an incapable server")
	}
	// Refused before the claim, so the one update slot is still free.
	if svc.applying {
		t.Fatal("a refused apply left the update slot held")
	}
	if body, rerr := os.ReadFile(self); rerr != nil || string(body) != "the installed binary" {
		t.Fatalf("aether-server = %q (%v), want it untouched", body, rerr)
	}
}
