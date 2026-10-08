package disk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsIdentityDoesNotFollowReplacedSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	name := filepath.Join(dir, "object")
	if err := os.WriteFile(name, []byte("object"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "private")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	counted := newSeen(root)
	claimed, err := counted.claim("object", info)
	if err != nil {
		t.Fatal(err)
	}
	if claimed || len(counted.files) != 0 {
		t.Fatal("replaced symlink was charged as a regular file")
	}
}

func TestWindowsIdentityFailureReportsPartialMeasurement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aether.db"), []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	// Traversal can still read the entry, but obtaining its rooted identity
	// fails. Do not silently charge it without global hardlink deduplication.
	u := componentTree(os.DirFS(dir), newSeen(root))
	if u.DatabaseBytes != 0 || len(u.Warnings) != 1 || len(u.Entries) != 1 {
		t.Fatalf("identity failure was not a partial measurement: %+v", u)
	}
	if u.Entries[0].Kind != "database" || u.Entries[0].Bytes != 0 || u.Entries[0].Error == "" {
		t.Fatalf("failed owner was not retained: %+v", u.Entries[0])
	}
}
