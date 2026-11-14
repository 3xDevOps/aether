package vmimage

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
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

func TestCachePoolCopiesCachesAsRawData(t *testing.T) {
	needTools(t, "mkfs.ext4", "cp")
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
	older, err := s.SnapshotCache(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SnapshotCache(t.Context(), disk)
	if err != nil {
		t.Fatal(err)
	}
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
	if next.Format != FormatRaw || !bytes.Equal(head(t, next.Path, len(header)), header) {
		t.Errorf("the next session's cache %+v starts %q, want a raw copy of the newest", next, head(t, next.Path, len(header)))
	}
	if sameFile(next.Path, newest.Path) || nlink(t, newest.Path) != 1 {
		t.Errorf("the next session's cache shares the newest cache's file, which has %d links", nlink(t, newest.Path))
	}
	writeAt(t, next.Path, []byte("session c"))
	if got := head(t, newest.Path, len(header)); !bytes.Equal(got, header) {
		t.Errorf("a session's write reached the pool's newest cache: %q", got)
	}

	if _, err = s.SnapshotCache(t.Context(), Disk{Path: next.Path, Format: FormatQcow2}); err == nil {
		t.Error("a qcow2 cache disk was copied")
	}
	if _, err = s.NewCacheDisk(t.Context(), "../escape", filepath.Join(dir, "x.img")); err == nil {
		t.Error("a pool name with a path was accepted")
	}
}
