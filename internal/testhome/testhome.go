// Package testhome isolates a test from the developer's real home
// directory. Client code resolves its config file through
// os.UserConfigDir and its SSH keys through os.UserHomeDir, and each of
// those reads a different variable per platform: XDG_CONFIG_HOME and HOME
// on Linux, HOME alone on macOS (~/Library/Application Support ignores
// XDG), AppData and USERPROFILE on Windows. A helper that sets only some
// of them lets a test overwrite the user's actual Aether config, which is
// exactly what happened on macOS before this package existed.
package testhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Isolate points every home and config lookup at a fresh temporary
// directory for the rest of the test and returns that directory. It
// fails the test if os.UserConfigDir or os.UserHomeDir still resolve
// outside it. SSH_AUTH_SOCK is cleared so key discovery never reaches a
// real agent; Windows tests that must also ignore the OpenSSH pipe stub
// the agent transport in the cli package.
func Isolate(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("AppData", filepath.Join(dir, "AppData", "Roaming"))
	t.Setenv("SSH_AUTH_SOCK", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("testhome: user home: %v", err)
	}
	if !within(dir, home) {
		t.Fatalf("testhome: user home %s resolves outside %s", home, dir)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("testhome: user config dir: %v", err)
	}
	if !within(dir, cfg) {
		t.Fatalf("testhome: user config dir %s resolves outside %s", cfg, dir)
	}
	return dir
}

// within reports whether path is dir or lies beneath it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
