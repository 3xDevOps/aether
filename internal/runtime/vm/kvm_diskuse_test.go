//go:build kvm

package vm

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMDiskUseFollowsWhatTheGuestKeeps writes to a VM's raw data disk and
// its qcow2 root disk, then deletes what it wrote: the host blocks its disks
// take grow, stop growing while the VM is paused, and come back through
// discard.
func TestKVMDiskUseFollowsWhatTheGuestKeeps(t *testing.T) {
	e := kvmEngine(t)
	const key = "run-vmdiskuse"
	rootSession(t, e, key, "docker.io/library/ubuntu:24.04")
	id := runtime.ID(idPrefix + key)
	use := func() int64 {
		t.Helper()
		all, err := e.diskUse()
		if err != nil {
			t.Fatal(err)
		}
		return all[id]
	}
	before := use()
	if out, code := vmRun(t, e, key, "head -c 536870912 /dev/urandom > /tmp/big && head -c 268435456 /dev/urandom > /opt/big && sync && echo wrote"); code != 0 || !strings.Contains(out, "wrote") {
		t.Fatalf("write: %d %q", code, out)
	}
	grown := use()
	t.Logf("the disks took %d MiB, then %d MiB after 768 MiB of writes", before>>20, grown>>20)
	if grown-before < 700<<20 {
		t.Fatalf("disk use grew %d MiB for 768 MiB written", (grown-before)>>20)
	}
	// A pause, which the scheduler's disk guard makes, stops a writer.
	vmRun(t, e, key, "setsid sh -c 'for i in $(seq 200); do head -c 8388608 /dev/urandom >> /tmp/grow; sync; done' </dev/null >/dev/null 2>&1 &")
	growth := func() int64 {
		start := use()
		time.Sleep(2 * time.Second)
		return use() - start
	}
	if g := growth(); g <= 0 {
		t.Fatalf("a running writer grew the disks %d bytes in 2 s", g)
	}
	if err := e.pause(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if g := growth(); g != 0 {
		t.Errorf("a paused VM's disks grew %d bytes in 2 s", g)
	}
	if err := e.resume(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if g := growth(); g <= 0 {
		t.Errorf("a resumed writer grew the disks %d bytes in 2 s", g)
	}
	if out, code := vmRun(t, e, key, "pkill -f 'seq 200'; sleep 0.5; rm -f /tmp/grow /tmp/big /opt/big && sync && echo deleted"); code != 0 || !strings.Contains(out, "deleted") {
		t.Fatalf("delete: %d %q", code, out)
	}
	deadline := time.Now().Add(30 * time.Second)
	for use()-before > 64<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("after the deletes the disks still take %d MiB more than before", (use()-before)>>20)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("after the deletes the disks take %d MiB", use()>>20)
}
