//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/selfupdate"
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

// TestUpdatePackagedChecksButNeverApplies covers the Android app, which runs
// `update --check --json` for its notice and installs updates itself.
func TestUpdatePackagedChecksButNeverApplies(t *testing.T) {
	t.Setenv(selfupdate.PackagedEnv, "Android app")
	out, err := captureStdout(t, func() error { return runUpdate([]string{"--check", "--json"}) })
	if err != nil {
		t.Fatalf("packaged check: %v", err)
	}
	var got selfupdate.Check
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("check output %q: %v", out, err)
	}
	if got.Packaged != "Android app" || got.CanSelfUpdate {
		t.Fatalf("check = %+v, want packaged with no self-update", got)
	}

	err = runUpdate(nil)
	if err == nil || !strings.Contains(err.Error(), "update the Android app instead") {
		t.Fatalf("packaged apply = %v, want the refusal", err)
	}
}

func TestServerFirstRefusesWhenAnAdminCanUpdateTheServer(t *testing.T) {
	var out bytes.Buffer
	err := serverFirst(&out, "v0.9.0", linkedServer{reachable: true, version: "v0.8.0", admin: true, capable: true})
	if err == nil || !strings.HasPrefix(err.Error(), "update the server first: aether server update") {
		t.Fatalf("err = %v, want the server-first refusal", err)
	}
}

func TestServerFirstProceeds(t *testing.T) {
	for name, server := range map[string]linkedServer{
		"unreachable":   {},
		"current":       {reachable: true, version: "v0.9.0", admin: true, capable: true},
		"ahead":         {reachable: true, version: "v0.9.1", admin: true, capable: true},
		"dev server":    {reachable: true, version: "dev", admin: true, capable: true},
		"member":        {reachable: true, version: "v0.8.0"},
		"manual server": {reachable: true, version: "v0.8.0", admin: true, manual: []string{"sudo aether update"}},
	} {
		var out bytes.Buffer
		if err := serverFirst(&out, "v0.9.0", server); err != nil {
			t.Errorf("%s: err = %v, want the install to proceed", name, err)
		}
	}
}
