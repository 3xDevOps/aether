//go:build kvm

package ompbroker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime/vm"
)

// standIn serves omp's /v1/healthz and notes who it runs as in the login
// directory, as `omp auth-broker serve --bind=<addr>` would write there.
const standIn = `#!/bin/sh
id -u > "$PI_CODING_AGENT_DIR/written-by"
exec python3 -c '
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"{\"ok\":true}")
host, port = sys.argv[1].split(":")
http.server.HTTPServer((host, int(port)), H).serve_forever()
' "${3#--bind=}"
`

// TestKVMBrokerUnitRunsUnprivilegedOnARootOwnedHome runs a broker unit
// under systemd, as root on the KVM runner, for a home that root owns as
// it does on a VM host: the unit runs as the broker user and writes the
// login directory, whose files stay the session user's on the host, as a
// Docker session needs them, and the rest of the home is left alone.
func TestKVMBrokerUnitRunsUnprivilegedOnARootOwnedHome(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root and systemd")
	}
	// Outside /tmp and /var/tmp, which the unit's PrivateTmp replaces.
	base, err := os.MkdirTemp("/opt", "aether-omp-kvm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	// MkdirTemp's 0700 would keep the broker user from its binary here.
	if err = os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	home, data, binary := filepath.Join(base, "home"), filepath.Join(base, "data"), filepath.Join(base, "omp")
	for path, body := range map[string]string{
		filepath.Join(home, ".omp/agent/agent.db"):  "fake-db",
		filepath.Join(home, ".claude/.credentials"): "other-login",
	} {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(binary, []byte(standIn), 0o755); err != nil {
		t.Fatal(err)
	}
	// The broker runs as nobody; the member's sessions run as 1000, which
	// must keep owning their files.
	const nobody, session = 65534, 1000
	bind := func(target, base, subpath string, onDiskUID, onDiskGID int) error {
		return vm.BindBeneath(target, base, subpath, vm.IDMap{OnDiskUID: onDiskUID, OnDiskGID: onDiskGID, ShownUID: nobody, ShownGID: nobody})
	}
	m := NewManager(Config{Binary: binary, Dir: filepath.Join(data, "omp-broker"), DataDir: data, Units: vm.SystemdUnits{},
		Bind: bind, Unbind: vm.Unbind, UID: nobody, GID: nobody, Ready: time.Minute})
	t.Cleanup(func() { _ = m.Stop(context.Background(), "kvmtest") })
	owner := func(context.Context) (int, int, error) { return session, session, nil }
	if _, err = m.ensure(t.Context(), Account{ID: "kvmtest", Home: home, Prepare: owner}); err != nil {
		journal, _ := exec.Command("journalctl", "--no-pager", "-n", "40", "-u", unitName("kvmtest")).CombinedOutput()
		t.Fatalf("%v\n%s", err, journal)
	}
	if got, readErr := os.ReadFile(filepath.Join(home, ".omp/agent/written-by")); readErr != nil || strings.TrimSpace(string(got)) != "65534" {
		t.Errorf("the broker ran as %q, %v", got, readErr)
	}
	ownerOf := func(path string) uint32 {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		return info.Sys().(*syscall.Stat_t).Uid
	}
	for _, name := range []string{".omp/agent", ".omp/agent/agent.db", ".omp/agent/written-by"} {
		if got := ownerOf(filepath.Join(home, name)); got != session {
			t.Errorf("~/%s is owned by %d on the host, not the session user", name, got)
		}
	}
	if got := ownerOf(filepath.Join(home, ".claude/.credentials")); got != 0 {
		t.Errorf("a file outside ~/.omp/agent is now owned by %d", got)
	}
	if err = m.Stop(t.Context(), "kvmtest"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(data, "omp-broker", "kvmtest.agent")); !os.IsNotExist(err) {
		t.Errorf("the stopped broker's bind: %v", err)
	}
}
