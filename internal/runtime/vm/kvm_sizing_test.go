//go:build kvm

package vm

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMGuestFullOfMemoryKeepsItsVM fills a session VM's memory with page
// cache, then runs a program that allocates until the session's own cap
// kills it, as Docker's would: the VM lives on, its unit never reaching an
// OOM kill of its own, and the session had its whole limit although its
// checkout has the helper keep the git service's reserve out of it too.
func TestKVMGuestFullOfMemoryKeepsItsVM(t *testing.T) {
	h := newCheckoutHost(t)
	e, key, ws := h.e, "run-vmtest-full", domain.WorkspaceID("wsfull")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The limit checkoutHost.boot gives.
	const limit = 1 << 30
	h.session(t, ws, key, h.workspace(t, ws, 10), runtime.Mount{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true})
	out, _ := vmRun(t, e, key, "cat /sys/fs/cgroup/session/memory.max; cat /proc/swaps; grep -E 'MemTotal|MemAvailable' /proc/meminfo")
	t.Logf("guest at boot: %q", out)
	if first, _, _ := strings.Cut(out, "\n"); first != strconv.Itoa(limit) {
		t.Errorf("the session's memory.max is %s, want its whole limit %d", first, limit)
	}
	if !strings.Contains(out, "/run/aether-guest/data/swap") || !strings.Contains(out, strconv.Itoa(limit/4>>10-4)) {
		t.Errorf("the guest's swap is not a quarter of the limit on the data disk: %q", out)
	}
	st, err := e.sample(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stats at boot: %d of %d bytes", st.MemoryBytes, st.MemoryLimitBytes)
	if st.MemoryLimitBytes != limit || st.MemoryBytes > limit/16 {
		t.Errorf("an idle session uses %d of %d bytes in its stats, want a few MiB of its limit %d", st.MemoryBytes, st.MemoryLimitBytes, limit)
	}

	// The file is larger than the session's limit, so its page cache fills
	// the session's memory.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	code, _, errOut, err := e.exec(ctx, key, []string{"/bin/sh", "-c", "head -c 1536M /dev/urandom >/tmp/fill && cat /tmp/fill >/dev/null"}, "")
	if err != nil || code != 0 {
		t.Fatalf("fill the page cache: %d, %v: %s", code, err, errOut)
	}
	code, _, errOut, err = e.exec(ctx, key, []string{"/bin/sh", "-c", "AETHER_VM_TEST_HOG_NOW=1 exec /opt/aether-vmtest -test.run '^TestKVMHogNow$'"}, "")
	if err != nil || code != 128+int(unix.SIGKILL) {
		t.Fatalf("the program that filled the session ended with %d, %v: %s", code, err, errOut)
	}
	session := guestFields(t, e, key, "grep -E '^(oom|oom_kill) ' /sys/fs/cgroup/session/memory.events; cat /sys/fs/cgroup/session/memory.peak /sys/fs/cgroup/session/memory.swap.peak", 6)
	t.Logf("session after the kill: %v", session)
	// oom counts the session reaching its own memory.max; oom_kill counts
	// its processes killed by any OOM killer, the guest's too.
	if session[1] == "0" || session[3] == "0" || parseUint(t, session[4]) < limit*99/100 {
		t.Errorf("the session's own cap did not kill the program at its whole limit %d: %v", limit, session)
	}

	unit := filepath.Join("/sys/fs/cgroup", runtime.SessionSlice, unitName(key))
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(unit, name))
		if err != nil {
			return err.Error()
		}
		return strings.TrimSpace(string(data))
	}
	t.Logf("unit: memory.max %s, memory.peak %s, memory.swap.max %s, memory.swap.peak %s", read("memory.max"), read("memory.peak"), read("memory.swap.max"), read("memory.swap.peak"))
	var stat []string
	for line := range strings.SplitSeq(read("memory.stat"), "\n") {
		if name, _, _ := strings.Cut(line, " "); name == "anon" || name == "file" || name == "shmem" || name == "kernel" || name == "sock" || name == "file_dirty" || name == "file_writeback" {
			stat = append(stat, line)
		}
	}
	t.Logf("unit memory.stat: %s", strings.Join(stat, ", "))
	events := read("memory.events")
	t.Logf("unit memory.events: %s", strings.ReplaceAll(events, "\n", ", "))
	if !strings.Contains(events, "oom_kill 0") {
		t.Errorf("the unit's own OOM killer ran: %s", events)
	}
	if out, code := vmRun(t, e, key, "echo alive"); code != 0 || !strings.Contains(out, "alive") {
		t.Errorf("the VM after the session filled it: %d %q", code, out)
	}
}
