//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWarnShadowedNamesADifferentAetherInPath(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "self", "aether")
	onPath := filepath.Join(dir, "bin", "aether")
	for _, path := range []string{self, onPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", filepath.Dir(onPath))

	var out bytes.Buffer
	warnShadowed(&out, self)
	want := "warning: aether in PATH is " + onPath + ", a different binary; update it with: " + onPath + " update\n"
	if out.String() != want {
		t.Errorf("warning = %q, want %q", out.String(), want)
	}

	if err := os.Remove(onPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, onPath); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	warnShadowed(&out, self)
	if out.Len() != 0 {
		t.Errorf("a PATH link to this binary warned: %q", out.String())
	}
}
