package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
