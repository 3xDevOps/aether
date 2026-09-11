package localops

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

func TestDetectHarnesses(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"claude", "pi"} {
		// Windows PATH lookup only finds files with an executable
		// extension, so the stub carries one there.
		if goruntime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	got := DetectHarnesses()
	want := []HarnessStatus{
		{Name: "claude", Installed: true},
		{Name: "codex", Installed: false},
		{Name: "pi", Installed: true},
	}
	if len(got) != len(want) {
		t.Fatalf("DetectHarnesses returned %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
