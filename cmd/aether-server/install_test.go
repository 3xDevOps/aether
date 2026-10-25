package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
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
				strings.Contains(out.String(), "\nSkipping Docker inside runs (Sysbox), which needs Docker; once Docker answers: sudo aether-server sysbox install\n")
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
