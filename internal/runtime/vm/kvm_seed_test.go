//go:build kvm

package vm

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/runtime"
	"golang.org/x/sys/unix"
)

// TestKVMSeedsStateAndRelaysALoopbackPort covers what Copilot and omp need
// in a session VM: a login copied from the home into the session's own
// directory at boot, and a host socket reached at a loopback port.
func TestKVMSeedsStateAndRelaysALoopbackPort(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-seed")
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	home, state, broker := filepath.Join(src, "home"), filepath.Join(src, "state"), filepath.Join(src, "broker")
	writeHome(t, home, map[string]string{".copilot/config.json": "fake-login-1", ".copilot/installed-plugins/x": "home-only"})
	for _, d := range []string{state, broker} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("unix", filepath.Join(broker, "broker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				line, _ := bufio.NewReader(c).ReadString('\n')
				_, _ = c.Write([]byte("broker saw " + line))
			}()
		}
	}()
	kvmUserSession(t, e, "run-seed", runtime.Mount{HostPath: home, ContainerPath: "/root"},
		runtime.Mount{HostPath: state, Subpath: "0", ContainerPath: "/root/.copilot", Kind: runtime.MountState},
		runtime.Mount{HostPath: state, ContainerPath: "/root/.copilot/config.json", Kind: runtime.MountSeed},
		runtime.Mount{HostPath: broker, ContainerPath: "/run/aether-omp", ReadOnly: true, Port: 47630})
	if out, code := vmRun(t, e, "run-seed", "cat /root/.copilot/config.json; ls /root/.copilot; grep ' /root/.copilot ' /proc/mounts"); code != 0 ||
		!strings.HasPrefix(out, "fake-login-1") || strings.Contains(out, "installed-plugins") || !strings.Contains(out, " ext4 ") {
		t.Errorf("the session's own ~/.copilot: %d %q", code, out)
	}
	if out, code := vmRun(t, e, "run-seed", "echo fake-login-2 > /root/.copilot/config.json"); code != 0 {
		t.Fatalf("write the session's copy: %d %q", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".copilot/config.json")); string(data) != "fake-login-1" {
		t.Errorf("the session's copy reached the home: %q", data)
	}
	script := `exec 3<>/dev/tcp/127.0.0.1/47630 && echo hello >&3 && head -n 1 <&3`
	if out, code := vmRun(t, e, "run-seed", "bash -c '"+script+"'"); code != 0 || strings.TrimSpace(out) != "broker saw hello" {
		t.Errorf("the loopback port: %d %q", code, out)
	}
}

// TestKVMBindBeneathKeepsTheBrokerInsideTheHome checks the bind the omp
// broker's unit gets: a home whose ~/.omp is a symlink out of it binds
// nothing.
func TestKVMBindBeneathKeepsTheBrokerInsideTheHome(t *testing.T) {
	dir := t.TempDir()
	home, outside, target := filepath.Join(dir, "home"), filepath.Join(dir, "outside"), filepath.Join(dir, "staging")
	writeHome(t, home, map[string]string{".omp/agent/agent.db": "fake-db"})
	writeHome(t, outside, map[string]string{"agent/secret": "host-only"})
	if err := BindBeneath(target, home, ".omp/agent", IDMap{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(target, unix.MNT_DETACH) })
	if data, err := os.ReadFile(filepath.Join(target, "agent.db")); err != nil || string(data) != "fake-db" {
		t.Fatalf("the bound login directory: %q, %v", data, err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".omp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".omp")); err != nil {
		t.Fatal(err)
	}
	if err := BindBeneath(target, home, ".omp/agent", IDMap{}); err == nil {
		t.Fatal("a symlink out of the home was bound")
	}
	if _, err := os.Stat(filepath.Join(target, "secret")); err == nil {
		t.Fatal("the directory outside the home shows at the staging path")
	}
}
