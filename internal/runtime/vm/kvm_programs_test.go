//go:build kvm

package vm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMAgentProgramLoadsFromItsDisk boots sessions whose agent program
// has a disk of its own: the guest runs it from that disk, a process
// started from it keeps reading it after the home's copy was replaced and
// the VM slept, and each cold boot takes the version the home holds.
func TestKVMAgentProgramLoadsFromItsDisk(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-programs")
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	home := filepath.Join(src, "home")
	agent := filepath.Join(home, ".local", "bin", "agent")
	// sh reads a script as it runs it: with WAIT set, the script prints its
	// version only after /tmp/go appears, reading it past the padding.
	install := func(version string) {
		t.Helper()
		script := "#!/bin/sh\n[ -z \"$WAIT\" ] || while [ ! -e /tmp/go ]; do sleep 0.1; done\n" +
			strings.Repeat("# padding past the shell's read buffer\n", 2000) + "echo " + version + "\n"
		// As an installer does: a new file renamed over the old one.
		writeHome(t, home, map[string]string{".local/bin/agent.new": script})
		if err := os.Chmod(agent+".new", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(agent+".new", agent); err != nil {
			t.Fatal(err)
		}
	}
	session := func(key string, homeMount runtime.Mount) {
		t.Helper()
		t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
		spec := runtime.Spec{
			Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
			Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 512 << 20,
			Mounts:   []runtime.Mount{homeMount},
			Programs: []runtime.Program{{Home: home, Target: "/root", Executable: "agent"}},
		}
		if _, err := e.create(t.Context(), key, spec, options{}); err != nil {
			t.Fatal(err)
		}
		if err := e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	const key = "run-vmprog"
	runs := func(want string) {
		t.Helper()
		out, code := vmRun(t, e, key, `[ "$(stat -c %d /root/.local/bin/agent)" = "$(stat -c %d /root)" ] && printf 'home ' || printf 'disk '; /root/.local/bin/agent`)
		if code != 0 || out != want+"\n" {
			t.Fatalf("the agent ran as %q (%d), want %q", out, code, want)
		}
	}
	restart := func() {
		t.Helper()
		if err := e.stop(t.Context(), key, time.Second); err != nil {
			t.Fatal(err)
		}
		if err := e.start(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}

	install("v1")
	session(key, runtime.Mount{HostPath: home, ContainerPath: "/root"})
	runs("home v1")
	e.builds.Wait()
	restart()
	runs("disk v1")

	if out, code := vmRun(t, e, key, "WAIT=1 setsid -f /root/.local/bin/agent > /tmp/out 2>&1"); code != 0 {
		t.Fatalf("start the agent: %d %s", code, out)
	}
	install("v2")
	if err := e.save(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := e.restore(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if out, code := vmRun(t, e, key, "touch /tmp/go; for i in $(seq 100); do [ -s /tmp/out ] && break; sleep 0.1; done; cat /tmp/out"); code != 0 || out != "v1\n" {
		t.Errorf("an agent started before the update read its program after a wake as %q (%d), want v1", out, code)
	}

	restart()
	runs("home v2")
	e.builds.Wait()
	restart()
	runs("disk v2")

	const isolated = "run-vmprog-i"
	session(isolated, runtime.Mount{HostPath: home, ContainerPath: "/root", Overlay: filepath.Join(src, "overlay")})
	if out, code := vmRun(t, e, isolated, `[ "$(stat -c %d /root/.local/bin/agent)" != "$(stat -c %d /root)" ] && /root/.local/bin/agent`); code != 0 || out != "v2\n" {
		t.Errorf("an Isolated session's agent: %q (%d), want v2 from its disk", out, code)
	}
}
