package testhome

import (
	"os"
	"path/filepath"
	"testing"
)

// The whole point of Isolate is that no platform's config lookup escapes
// the scratch directory; this pins that on whichever OS runs the suite.
func TestIsolateKeepsConfigAndHomeInsideTempDir(t *testing.T) {
	dir := Isolate(t)
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if !within(dir, cfg) {
		t.Fatalf("config dir %s escaped %s", cfg, dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if home != dir {
		t.Fatalf("home = %s, want %s", home, dir)
	}
	if got := os.Getenv("SSH_AUTH_SOCK"); got != "" {
		t.Fatalf("SSH_AUTH_SOCK = %q, want it cleared so key discovery never reaches a real agent", got)
	}
}

func TestWithin(t *testing.T) {
	dir := filepath.Join("a", "b")
	for path, want := range map[string]bool{
		dir:                           true,
		filepath.Join(dir, "c"):       true,
		filepath.Join("a", "bc"):      false,
		filepath.Join("a"):            false,
		filepath.Join(dir, "..", "x"): false,
	} {
		if got := within(dir, path); got != want {
			t.Errorf("within(%s, %s) = %v, want %v", dir, path, got, want)
		}
	}
}
