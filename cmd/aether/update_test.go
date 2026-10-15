//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWarnShadowedNamesADifferentAetherInPath(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "self", "aether")
	onPath := filepath.Join(dir, "my tools", "aether")
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
	want := "warning: aether in PATH is " + onPath + ", a different binary; update it with: '" + onPath + "' update\n"
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

func TestOfferSysbox(t *testing.T) {
	ctx := t.Context()
	osRelease := func(body string) string {
		path := filepath.Join(t.TempDir(), "os-release")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	runtimes := func(out string, err error) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return out, err }
	}
	plain := `{"runc":{}}`
	for name, tc := range map[string]struct {
		release  string
		runtimes func(context.Context) (string, error)
		want     bool
	}{
		"ubuntu without sysbox":     {"ID=ubuntu\nID_LIKE=debian\n", runtimes(plain, nil), true},
		"debian derivative":         {"ID=pop\nID_LIKE=\"ubuntu debian\"\n", runtimes(plain, nil), true},
		"sysbox already registered": {"ID=ubuntu\n", runtimes(`{"runc":{},"sysbox-runc":{}}`, nil), false},
		"not debian":                {"ID=fedora\n", runtimes(plain, nil), false},
		"docker unavailable":        {"ID=ubuntu\n", runtimes("", errors.New("no docker")), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := offerSysbox(ctx, osRelease(tc.release), tc.runtimes); got != tc.want {
				t.Errorf("offerSysbox = %v, want %v", got, tc.want)
			}
		})
	}
}
