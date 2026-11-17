package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/serversetup"
)

// Automation may install Docker after install, so a Docker that does not
// answer is a warning: the files are written, Sysbox is left out, and the
// next steps start on installing Docker.
func TestWriteInstallWarnsAndContinuesWithoutDocker(t *testing.T) {
	for name, tc := range map[string]struct {
		err      error
		installs int
		first    string
	}{
		"docker down":    {errors.New(dockerDown), 0, "  1. install or start Docker on this machine:\n       https://docs.docker.com/engine/install/\n  2. start the server"},
		"docker answers": {nil, 1, "  1. start the server"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeDocker(t, tc.err)
			installs := fakeSysbox(t, false, func(io.Writer) error { return nil })
			dir := t.TempDir()
			unitPath, configPath := filepath.Join(dir, "aether-server.service"), filepath.Join(dir, "server.conf")
			var out bytes.Buffer
			if err := writeInstall(&out, unitPath, configPath, map[string]string{"data-dir": filepath.Join(dir, "data")}, false, true); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{unitPath, configPath} {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("install did not write %s: %v", path, err)
				}
			}
			warned := strings.HasPrefix(out.String(), "Docker does not answer: "+dockerDown+"\n") &&
				strings.Contains(out.String(), "\nSkipping Docker inside sessions (Sysbox), which needs Docker; once Docker answers: sudo aether-server sysbox install\n")
			if warned != (tc.err != nil) || *installs != tc.installs || !strings.HasPrefix(nextSteps(t, out.String()), tc.first) {
				t.Errorf("warned = %v, Sysbox installs = %d, want %d and next steps starting %q:\n%s", warned, *installs, tc.installs, tc.first, out.String())
			}
		})
	}
}

// With the edge on, install shows the server id the claim names, and one
// warning line for what the host lacks.
func TestWriteInstallShowsTheServerAndWhatTheHostLacks(t *testing.T) {
	fakeDocker(t, nil)
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	values := map[string]string{"data-dir": dataDir, "edge-url": "https://edge.example", "edge-access": "approved-devices"}
	var out bytes.Buffer
	if err := writeInstall(&out, filepath.Join(dir, "aether-server.service"), filepath.Join(dir, "server.conf"), values, false, false); err != nil {
		t.Fatal(err)
	}
	id, err := serverID(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var warnings []string
	for line := range strings.Lines(out.String()) {
		if strings.HasPrefix(line, "warning: ") {
			warnings = append(warnings, line)
		}
	}
	if len(warnings) != 1 || !strings.HasPrefix(warnings[0], "warning: git is not installed; ssh-keygen is not installed") ||
		!strings.HasSuffix(warnings[0], " (docs/install.md#server-prerequisites)\n") {
		t.Errorf("want one warning line naming git and ssh-keygen, got %q:\n%s", warnings, out.String())
	}
	if !strings.Contains(out.String(), "\nserver id: "+id+"\n") {
		t.Errorf("install does not show the server id %s:\n%s", id, out.String())
	}
}

// A rerun that names an option the kept config sets otherwise writes
// nothing and prints the command that applies it; naming what the file
// already holds, even as a default it leaves out, goes through.
func TestInstallRefusesToDropANamedOption(t *testing.T) {
	t.Setenv("SUDO_USER", "ubuntu")
	configPath := filepath.Join(t.TempDir(), "server.conf")
	if err := serversetup.WriteConfig(configPath, map[string]string{"addr": ":2222", "edge-url": "https://edge.example", "edge-access": "account"}); err != nil {
		t.Fatal(err)
	}
	same := map[string]string{"addr": ":2222", "edge-access": "account", "tailnet-auto-join": "false"}
	if err := refuseKeptChanges(configPath, same); err != nil {
		t.Errorf("rerun naming the kept values: %v", err)
	}
	err := refuseKeptChanges(configPath, map[string]string{"addr": ":2222", "edge-access": "approved-devices", "edge-url": ""})
	want := "  sudo aether-server config set edge-access approved-devices --config " + configPath + "\n" +
		"  sudo aether-server config set edge-url '' --config " + configPath + "\n"
	if err == nil || !strings.Contains(err.Error(), "nothing was written") || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), " addr ") {
		t.Errorf("rerun naming other values = %v, want the config set commands:\n%s", err, want)
	}
	if err := refuseKeptChanges(filepath.Join(t.TempDir(), "none.conf"), same); err != nil {
		t.Errorf("first install: %v", err)
	}
}

// Sessions in VMs need KVM: without it install stops with the error and
// leaves the unit and config unwritten.
func TestInstallForVMsWithoutKVMWritesNothing(t *testing.T) {
	kvmDevice = filepath.Join(t.TempDir(), "kvm")
	t.Cleanup(func() { kvmDevice = "/dev/kvm" })
	dir := t.TempDir()
	unitPath, configPath := filepath.Join(dir, "aether-server.service"), filepath.Join(dir, "server.conf")
	var out bytes.Buffer
	err := writeInstall(&out, unitPath, configPath, map[string]string{"data-dir": filepath.Join(dir, "data"), "session-runtime": "vm"}, false, true)
	if err == nil || runtime.GOARCH == "amd64" && !strings.Contains(err.Error(), kvmDevice+": no such file or directory") {
		t.Fatalf("install without KVM = %v\n%s", err, out.String())
	}
	for _, path := range []string{unitPath, configPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("install without KVM wrote %s: %v", path, err)
		}
	}
}

// Install links cloudfield-server.service to the unit as the updater does,
// leaves whatever else holds the name as it is, and keeps one link on a
// rerun. systemctl enable refuses an alias, so the activation line keeps
// the unit's own name.
func TestInstallLinksTheUnitsSecondName(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	fakeDocker(t, nil)
	for name, tc := range map[string]struct {
		before func(alias, unit string) error
		link   string
	}{
		"fresh":                  {nil, "aether-server.service"},
		"linked by the updater":  {func(alias, _ string) error { return os.Symlink("aether-server.service", alias) }, "aether-server.service"},
		"an absolute link to it": {func(alias, unit string) error { return os.Symlink(unit, alias) }, ""},
		"another unit's file":    {func(alias, _ string) error { return os.WriteFile(alias, []byte("[Unit]\n"), 0o644) }, ""},
		"a link to another unit": {func(alias, _ string) error { return os.Symlink("other.service", alias) }, "other.service"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			unitPath, configPath := filepath.Join(dir, "aether-server.service"), filepath.Join(dir, "server.conf")
			alias := filepath.Join(dir, "cloudfield-server.service")
			if tc.before != nil {
				if err := tc.before(alias, unitPath); err != nil {
					t.Fatal(err)
				}
			}
			if tc.link == "" {
				tc.link, _ = os.Readlink(alias)
			}
			for range 2 {
				var out bytes.Buffer
				if err := writeInstall(&out, unitPath, configPath, map[string]string{"data-dir": filepath.Join(dir, "data")}, false, false); err != nil {
					t.Fatal(err)
				}
				want := "  1. start the server on this machine:\n       systemctl daemon-reload && systemctl enable --now aether-server\n"
				if got := nextSteps(t, out.String()); !strings.HasPrefix(got, want) || strings.Contains(out.String(), alias) {
					t.Errorf("next steps:\n%s\nwant them to start:\n%s", out.String(), want)
				}
			}
			if got, _ := os.Readlink(alias); got != tc.link {
				t.Errorf("%s links to %q, want %q", alias, got, tc.link)
			}
			if after, _ := os.ReadFile(alias); tc.link == "" && string(after) != "[Unit]\n" {
				t.Errorf("%s now holds %q, want the file that was there", alias, after)
			}
		})
	}
}
