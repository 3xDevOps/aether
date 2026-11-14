//go:build kvm

package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// capBounding is the CapBnd mask of process pid.
func capBounding(t *testing.T, pid int) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(line, "CapBnd:"); ok {
			mask, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return mask
		}
	}
	t.Fatalf("no CapBnd for %d", pid)
	return 0
}

// TestKVMUnitSandboxHoldsAgainstRootInIt boots a VM with a virtio-fs share
// and a network, then checks that its monitor and Cloud Hypervisor gave up
// CAP_SYS_ADMIN, and that root in a unit with the VM's properties can
// neither write kernel settings, open a host disk, use netlink, see /root nor
// write outside the VM's paths.
func TestKVMUnitSandboxHoldsAgainstRootInIt(t *testing.T) {
	e := kvmEngine(t)
	const key = "run-vmsandbox"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	share := filepath.Join(e.root, "..", "src-sandbox")
	if err := os.MkdirAll(share, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(share) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 512 << 20,
		WorktreeHostPath: share, WorktreeMountPath: "/workspace",
	}
	if _, err := e.create(t.Context(), key, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	st, err := e.units.State(t.Context(), unitName(key))
	if err != nil || st.MainPID == 0 {
		t.Fatalf("unit state %+v, %v", st, err)
	}
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(st.MainPID), "-x", "cloud-hyperviso").Output()
	if err != nil {
		t.Fatalf("find Cloud Hypervisor under the monitor %d: %v", st.MainPID, err)
	}
	ch, _ := strconv.Atoi(strings.Fields(string(out))[0])
	const sysAdmin = 1 << 21
	for name, pid := range map[string]int{"monitor": st.MainPID, "cloud-hypervisor": ch} {
		if mask := capBounding(t, pid); mask&sysAdmin != 0 {
			t.Errorf("the %s kept CAP_SYS_ADMIN after the share was made: CapBnd %x", name, mask)
		}
	}

	rec, err := e.load(key)
	if err != nil {
		t.Fatal(err)
	}
	booted, err := readLaunch(e.dir(key))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--quiet", "--wait", "--pipe", "--collect", "--unit=aether-vm-sandbox-probe"}
	for _, p := range sandbox(filepath.Dir(e.root), rec, booted) {
		args = append(args, "--property="+p)
	}
	source, err := exec.Command("findmnt", "-no", "SOURCE", "-T", e.root).Output()
	if err != nil {
		t.Fatal(err)
	}
	disk, _, _ := strings.Cut(strings.TrimSpace(string(source)), "[")
	// The database and another VM's file, both under the data directory the
	// unit hides.
	db := filepath.Join(filepath.Dir(e.root), "aether.db")
	if err = os.WriteFile(db, []byte("secret database"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(db) })
	other := e.path("run-vmother", "root.img")
	if err = os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(other)) })
	if err = os.WriteFile(other, []byte("another VM"), 0o600); err != nil {
		t.Fatal(err)
	}
	pin := filepath.Join(e.dir(key), helperImage)
	// Each attempt is guarded by if, so a denial (a read-only-filesystem
	// error on a redirection, which would otherwise end a POSIX shell) never
	// aborts the probe; a marker prints only if the forbidden action worked.
	write := func(path string) string { return "if (echo x >> " + path + ") 2>/dev/null; then echo " }
	probe := `disk=` + disk + `
if (cat /proc/sys/kernel/core_uses_pid > /proc/sys/kernel/core_uses_pid) 2>/dev/null; then echo sysctl-written; fi
if head -c 1 "$disk" >/dev/null 2>&1; then echo disk-opened; fi
if ip link show >/dev/null 2>&1; then echo netlink-used; fi
if ls -A /root 2>/dev/null | grep -q .; then echo root-seen; fi
if cat ` + db + ` >/dev/null 2>&1; then echo database-read; fi
if cat ` + other + ` >/dev/null 2>&1; then echo other-vm-read; fi
if (touch ` + filepath.Join(e.root, "escape") + `) 2>/dev/null; then echo outside-written; fi
` + write(pin) + `base-written; fi
` + write(e.path(key, recordFile)) + `record-written; fi
` + write(e.path(key, launchFile)) + `launch-written; fi
if touch ` + e.path(key, "inside") + `; then echo own-dir-written; fi
echo probe-done`
	out, err = exec.Command("systemd-run", append(append(args, "--"), "/bin/sh", "-c", probe)...).CombinedOutput()
	got := string(out)
	if err != nil || !strings.Contains(got, "probe-done") || !strings.Contains(got, "own-dir-written") {
		t.Fatalf("probe unit: %v: %s", err, got)
	}
	for _, escaped := range []string{
		"sysctl-written", "disk-opened", "netlink-used", "root-seen", "database-read",
		"other-vm-read", "outside-written", "base-written", "record-written", "launch-written",
	} {
		if strings.Contains(got, escaped) {
			t.Errorf("root in a unit with the VM's sandbox got %s:\n%s", escaped, got)
		}
	}
	_ = os.Remove(filepath.Join(e.root, "escape"))
}
