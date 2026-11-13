package vmimage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCachePoolPromotesAStoppedVMsCache(t *testing.T) {
	needTools(t, "mkfs.ext4", "qemu-img")
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "images"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	vm := filepath.Join(dir, "vm-a")
	if err = os.Mkdir(vm, 0o700); err != nil {
		t.Fatal(err)
	}
	disk, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(vm, "cache.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.readNewest(filepath.Join(s.cachesDir(), "pool1"))
	if err != nil || disk.Format != FormatQcow2 {
		t.Fatalf("new cache disk %+v over %+v, %v", disk, first, err)
	}
	second, err := s.NewCacheDisk(t.Context(), "pool1", filepath.Join(vm, "second.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	if pin, pinErr := backingFile(second.Path); pinErr != nil || !sameFile(pin, first.Path) {
		t.Errorf("a second session's cache backs onto %q, %v; want the same empty cache %s", pin, pinErr, first.Path)
	}
	snapshot, err := s.SnapshotCache(t.Context(), disk.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(vm); err != nil {
		t.Fatal(err)
	}
	if err = s.PromoteCache(t.Context(), "pool1", snapshot); err != nil {
		t.Fatal(err)
	}
	newest, err := s.readNewest(filepath.Join(s.cachesDir(), "pool1"))
	if err != nil || newest.Path == first.Path {
		t.Fatalf("newest after promotion = %+v, %v", newest, err)
	}
	if _, err = os.Stat(first.Path); !os.IsNotExist(err) {
		t.Errorf("the replaced cache no disk uses was kept: %v", err)
	}
	if _, err = os.Stat(snapshot); !os.IsNotExist(err) {
		t.Errorf("the snapshot was kept: %v", err)
	}
	if _, err = s.NewCacheDisk(t.Context(), "../escape", filepath.Join(dir, "x.qcow2")); err == nil {
		t.Error("a pool name with a path was accepted")
	}
}
