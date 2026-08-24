package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRunInitPreparesDataDir: on the platforms that can run a server,
// init creates the requested data directory. On Windows there is no
// server, so the subcommand refuses instead of creating a directory on
// whatever drive happens to be current.
func TestRunInitPreparesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	err := runInit([]string{"--data-dir", dir})

	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("init succeeded on Windows")
		}
		if !strings.Contains(err.Error(), "Linux server data directory") {
			t.Fatalf("error = %v, want a Linux-only refusal", err)
		}
		if _, statErr := os.Stat(dir); statErr == nil {
			t.Fatalf("init created %s on Windows", dir)
		}
		return
	}

	if err != nil {
		t.Fatalf("init: %v", err)
	}
	info, statErr := os.Stat(dir)
	if statErr != nil {
		t.Fatalf("stat data dir: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
}
