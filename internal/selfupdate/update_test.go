package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateRefusesUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Update refuses on Windows before it reaches the preflight")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	err := checkWritable(dir)
	if err == nil {
		t.Fatal("expected a permission error")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want one wrapping os.ErrPermission", err)
	}
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "sudo aether update") {
		t.Fatalf("err = %v, want it to name %s and sudo aether update", err, dir)
	}
}
