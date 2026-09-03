package serverupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
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

	svc, err := New(Config{Store: db, Bus: bus, Executable: self})
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
	// poll loop apply anything either.
	if perr := db.SetPendingServerUpdate(t.Context(), &store.PendingServerUpdate{
		Version: "v0.2.0", RequestedBy: "mem_a", RequestedAt: svc.cfg.Now(),
	}); perr != nil {
		t.Fatalf("SetPendingServerUpdate: %v", perr)
	}
	svc.Tick(t.Context())
	if body, rerr := os.ReadFile(self); rerr != nil || string(body) != "the installed binary" {
		t.Fatalf("aether-server = %q (%v), want it untouched", body, rerr)
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
