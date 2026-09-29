//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := checkDataDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("created data directory: %v %v, want mode 0700", info.Mode(), err)
	}

	// Another user's directory: root hands one to uid 65532; anyone else
	// meets root's /.
	foreign := "/"
	if os.Geteuid() == 0 {
		foreign = t.TempDir()
		if err := os.Chown(foreign, 65532, 65532); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkDataDir(foreign); err == nil || !strings.Contains(err.Error(), "data directory "+foreign+" belongs to uid ") ||
		!strings.Contains(err.Error(), "run chown -R ") {
		t.Errorf("another user's data directory: %v", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("root writes a directory of any mode")
	}
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := checkDataDir(readOnly); err == nil || !strings.Contains(err.Error(), "is not writable by uid") {
		t.Errorf("read-only data directory: %v", err)
	}
	if err := checkDataDir(filepath.Join(readOnly, "data")); err == nil || !strings.Contains(err.Error(), "create it for uid") {
		t.Errorf("data directory in a read-only parent: %v", err)
	}
}
