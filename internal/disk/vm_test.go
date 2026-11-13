//go:build unix

package disk

import (
	"os"
	"path/filepath"
	"testing"
)

// A VM's sparse disks, its data disk and its session's database disk, count
// what the guest wrote, and the disks VMs and checkouts hard-link from the
// image store count once, under the store, even for a VM or checkout that
// sorts before it.
func TestVMDisks(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	link := func(target, name string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(target, p); err != nil {
			t.Fatal(err)
		}
	}
	base := write("vm/images/bases/b1/disk.raw", 1000)
	helper := write("vm/images/helpers/h1/helper.raw", 200)
	prepared := write("vm/images/prepared/p1/disk.raw", 400)
	write("vm/images/caches/pool1/0001.raw", 300)
	link(base, "vm/assistant-m1/root.img.b1.base")
	link(base, "vm/run-1/root.img.b1.base")
	link(helper, "vm/run-1/helper.img")
	link(prepared, "checkouts/run-1/disk.qcow2.prepared-p1.base")
	write("vm/firewall.rules", 7)
	write("vm/run-1/root.img", 50)
	write("vm/run-1/snapshot/memory-ranges", 500)
	data := write("vm/run-1/data.img", 4096)
	state := write("scheduler/vm-databases/run-1/disk.img", 4096)
	for _, disk := range []string{data, state} {
		if err := os.Truncate(disk, 16<<30); err != nil {
			t.Fatal(err)
		}
	}

	u := components(dir)
	got := map[[2]string]uint64{}
	for _, e := range u.Entries {
		got[[2]string{e.Kind, e.Key}] = e.Bytes
	}
	if got[[2]string{"vm-image", ""}] != 1600 || got[[2]string{"vm-cache", "pool1"}] != 300 || got[[2]string{"vm-state", "run-1"}] != 500 {
		t.Errorf("image store and saved state: %v", got)
	}
	if own := got[[2]string{"vm", "run-1"}]; own <= 50 || own > 50+1<<20 {
		t.Errorf("run-1's own disks count %d bytes, want its 50-byte root disk plus the few KiB its 16 GiB sparse data disk holds", own)
	}
	db := got[[2]string{"vm-db", "run-1"}]
	if db == 0 || db > 1<<20 {
		t.Errorf("run-1's sparse database disk counts %d bytes, want the few KiB it holds", db)
	}
	if _, ok := got[[2]string{"vm", "assistant-m1"}]; ok || u.WorktreeBytes != 0 {
		t.Errorf("a link into the image store was charged to its VM or checkout: %v, worktrees %d", got, u.WorktreeBytes)
	}
	if u.VMStateBytes != 500 || u.VMBytes != 2400+got[[2]string{"vm", "run-1"}]+db || u.OtherBytes != 7 || len(u.Warnings) != 0 {
		t.Errorf("VM bytes %d, saved state %d, other %d, warnings %v", u.VMBytes, u.VMStateBytes, u.OtherBytes, u.Warnings)
	}
}
