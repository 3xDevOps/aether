package serversetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WriteConfig replaces the file by a rename: the mode stays, a symlink
// stays a symlink with its target rewritten, and a write that fails leaves
// the old file and no temporary file. Run as a user other than root, it
// skips the owner step and the file stays the writer's.
func TestWriteConfigReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(path, map[string]string{"edge-url": "https://edge.cloudfield.ai"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after the write %v, %v; want 0600", info.Mode(), err)
	}
	if got, lerr := Load(path); lerr != nil || got["edge-url"] != "https://edge.cloudfield.ai" {
		t.Fatalf("config %v, %v", got, lerr)
	}

	realDir := t.TempDir()
	real := filepath.Join(realDir, "server.conf")
	if err = os.WriteFile(real, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.conf")
	if err = os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err = WriteConfig(link, map[string]string{"addr": ":2300"}); err != nil {
		t.Fatal(err)
	}
	if li, lerr := os.Lstat(link); lerr != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link after the write: %v, %v; want it kept", li.Mode(), lerr)
	}
	if got, lerr := Load(real); lerr != nil || got["addr"] != ":2300" {
		t.Fatalf("target after the write: %v, %v", got, lerr)
	}

	// A rename onto a directory that is not empty fails after the
	// temporary file is written.
	blocked := filepath.Join(dir, "blocked.conf")
	if err = os.MkdirAll(filepath.Join(blocked, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = WriteConfig(blocked, map[string]string{"addr": ":2300"}); err == nil {
		t.Fatal("write onto a directory succeeded")
	}
	if _, err = os.Stat(filepath.Join(blocked, "keep")); err != nil {
		t.Errorf("the old content is gone: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temporary file %s left behind", e.Name())
		}
	}
}
