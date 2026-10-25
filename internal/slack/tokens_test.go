package slack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokensRoundTrip(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, _, err := ReadTokens(dir); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("ReadTokens on an empty dir = %v, want ErrNotSetUp", err)
	}
	if removed, err := RemoveTokens(dir); err != nil || removed {
		t.Fatalf("RemoveTokens with nothing stored = %v, %v; want false", removed, err)
	}

	if err := WriteTokens(dir, "xapp-1", "xoxb-1"); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	app, bot, err := ReadTokens(dir)
	if err != nil || app != "xapp-1" || bot != "xoxb-1" {
		t.Fatalf("ReadTokens = %q, %q, %v; want the tokens written", app, bot, err)
	}
	for path, mode := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, appTokenFile): 0o600, filepath.Join(dir, botTokenFile): 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: mode %v, %v; want %v", path, info.Mode().Perm(), err, mode)
		}
	}

	if err := os.Remove(filepath.Join(dir, botTokenFile)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadTokens(dir); err == nil || errors.Is(err, ErrNotSetUp) || !strings.Contains(err.Error(), "aether-server slack setup") {
		t.Fatalf("ReadTokens with one token = %v, want it to point to aether-server slack setup", err)
	}
	if removed, err := RemoveTokens(dir); err != nil || !removed {
		t.Fatalf("RemoveTokens = %v, %v; want true", removed, err)
	}
}
