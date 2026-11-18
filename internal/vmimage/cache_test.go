package vmimage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// writeAt writes p at the start of the file at path.
func writeAt(t *testing.T, path string, p []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt(p, 0)
	if err = errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
}

// head reads the first n bytes of the file at path.
func head(t *testing.T, path string, n int) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, n)
	if _, err = io.ReadFull(f, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// qemuWrite writes 512 bytes of pattern at the start of the qcow2 disk at path.
func qemuWrite(t *testing.T, path string, pattern byte) {
	t.Helper()
	if out, err := exec.Command("qemu-io", "-f", "qcow2", "-c", fmt.Sprintf("write -P %d 0 512", pattern), path).CombinedOutput(); err != nil {
		t.Fatalf("qemu-io: %v: %s", err, out)
	}
}

func TestCachePoolLayersCachesOverRawBases(t *testing.T) {
	needTools(t, "mkfs.ext4", "cp", "qemu-img", "qemu-io")
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "images"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	vm := filepath.Join(dir, "vm-a")
	if err = os.Mkdir(vm, 0o700); err != nil {
		t.Fatal(err)
	}
	disk, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(vm, "cache.img"))
	if err != nil {
		t.Fatal(err)
	}
	if disk.Format != FormatRaw {
		t.Fatalf("new cache disk = %+v, want raw", disk)
	}
	if _, err = s.readNewest(filepath.Join(s.cachesDir(), "pool1")); !os.IsNotExist(err) {
		t.Errorf("an empty cache became the pool's newest: %v", err)
	}
	header := []byte("written by the session")
	writeAt(t, disk.Path, header)
	second, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(vm, "second.img"))
	if err != nil {
		t.Fatal(err)
	}
	older, err := s.SnapshotCache(second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SnapshotCache(disk)
	if err != nil {
		t.Fatal(err)
	}
	// The VM starts again before the promotion reads its cache.
	if err = s.UnshareCache(t.Context(), disk); err != nil {
		t.Fatal(err)
	}
	if got := nlink(t, disk.Path); got != 1 || !bytes.Equal(head(t, disk.Path, len(header)), header) {
		t.Errorf("the unshared cache disk has %d links and starts %q, want its own copy", got, head(t, disk.Path, len(header)))
	}
	writeAt(t, disk.Path, []byte("after the snapshot"))
	if err = s.PromoteCache("pool1", snapshot); err != nil {
		t.Fatal(err)
	}
	newest, err := s.readNewest(filepath.Join(s.cachesDir(), "pool1"))
	if err != nil {
		t.Fatalf("newest after promotion: %v", err)
	}
	if got := head(t, newest.Path, len(header)); !bytes.Equal(got, header) {
		t.Errorf("the promoted cache starts %q, want the session's bytes %q", got, header)
	}
	if err = s.PromoteCache("pool1", older); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.readNewest(filepath.Join(s.cachesDir(), "pool1")); after.Path != newest.Path {
		t.Errorf("a promotion that finished late replaced the newer cache: %s, want %s", after.Path, newest.Path)
	}
	for _, snap := range []string{older, snapshot} {
		if _, err = os.Stat(snap); !os.IsNotExist(err) {
			t.Errorf("the snapshot %s was kept: %v", snap, err)
		}
	}

	next, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(vm, "next.img"))
	if err != nil {
		t.Fatal(err)
	}
	info := qemuInfo(t, next.Path)
	if next.Format != FormatQcow2 || !sameFile(next.Backing, newest.Path) || info.Backing != next.Backing || info.BackingFormat != "raw" {
		t.Errorf("the next session's cache is %+v, %+v; want a qcow2 disk over the newest cache as raw", next, info)
	}
	qemuWrite(t, next.Path, 0xcd)
	if got := head(t, newest.Path, len(header)); !bytes.Equal(got, header) {
		t.Errorf("a session's write reached the pool's newest cache: %q", got)
	}
	// The dirty bit Cloud Hypervisor leaves on a disk it did not close makes
	// qemu-img repair a disk it opens for writing.
	f, err := os.OpenFile(next.Path, os.O_WRONLY, 0)
	if err == nil {
		_, err = f.WriteAt([]byte{1}, 79)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		t.Fatal(err)
	}
	vmDisk, err := os.ReadFile(next.Path)
	if err != nil {
		t.Fatal(err)
	}
	layered, err := s.SnapshotCache(next)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PromoteCache("pool1", layered); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(next.Path); !bytes.Equal(got, vmDisk) {
		t.Error("promotion wrote the cache disk of the VM it read")
	}
	flat, err := s.readNewest(filepath.Join(s.cachesDir(), "pool1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := head(t, flat.Path, 2048); !bytes.Equal(got[:512], bytes.Repeat([]byte{0xcd}, 512)) || !bytes.Equal(got[1024:], head(t, newest.Path, 2048)[1024:]) {
		t.Errorf("the cache promoted from a qcow2 disk is not its writes over its base: starts %x", got[:16])
	}
	if _, err = os.Stat(newest.Path); err != nil {
		t.Errorf("the base a session's cache reads through was swept: %v", err)
	}
	if err = os.RemoveAll(vm); err != nil {
		t.Fatal(err)
	}
	if err = s.Sweep(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(newest.Path); !os.IsNotExist(err) {
		t.Errorf("a cache no disk reads through any more outlived the sweep: %v", err)
	}

	// A VM deleted before its promotion runs leaves only the snapshot on
	// the base it read through, which the promotion's own sweep deletes.
	deleted, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(dir, "deleted.img"))
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.SnapshotCache(deleted)
	if err == nil {
		err = errors.Join(os.Remove(deleted.Path), os.Remove(deleted.Backing))
	}
	if err == nil {
		err = s.PromoteCache("pool1", last)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(flat.Path); !os.IsNotExist(err) {
		t.Errorf("the promotion of a deleted VM's cache kept the base it read through: %v", err)
	}
	if flat, err = s.readNewest(filepath.Join(s.cachesDir(), "pool1")); err != nil {
		t.Fatal(err)
	}

	idle := time.Now().Add(-cacheIdleAge - time.Hour)
	started := filepath.Join(dir, "started.img")
	if _, err = s.NewCacheDisk(t.Context(), "pool1", started); err != nil {
		t.Fatal(err)
	}
	// The session outlives cacheIdleAge, and ends without promoting.
	if err = os.Chtimes(flat.Path, idle, idle); err == nil {
		err = s.Sweep()
	}
	if err == nil {
		err = errors.Join(os.Remove(started), prunePins(started, ""))
	}
	if err == nil {
		err = s.Sweep()
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.readNewest(filepath.Join(s.cachesDir(), "pool1")); err != nil {
		t.Errorf("the newest cache a session used until just now was swept: %v", err)
	}
	if err = os.Chtimes(flat.Path, idle, idle); err == nil {
		err = s.Sweep()
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(flat.Path); !os.IsNotExist(err) {
		t.Errorf("a newest cache idle for %s outlived the sweep: %v", cacheIdleAge, err)
	}
	if _, err = s.readNewest(filepath.Join(s.cachesDir(), "pool1")); !os.IsNotExist(err) {
		t.Errorf("the pool still names a newest cache: %v", err)
	}

	if _, err = s.SnapshotCache(Disk{Path: next.Path, Format: FormatQcow2}); err == nil {
		t.Error("a qcow2 cache disk with no base was copied")
	}
	if _, err = s.NewCacheDisk(t.Context(), "../escape", filepath.Join(dir, "x.img")); err == nil {
		t.Error("a pool name with a path was accepted")
	}
}
