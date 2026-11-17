package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A logout without --edge revokes one token; the others are named with
// the command that signs out there.
func TestPrintStillSignedIn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "edge-tokens.json"), []byte(`{"edges":{"https://edge.onaether.dev":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printStillSignedIn(&out, dir); err != nil {
		t.Fatal(err)
	}
	if want := "still signed in to edge.onaether.dev; sign out there with: aether logout --edge https://edge.onaether.dev\n"; out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}
}
