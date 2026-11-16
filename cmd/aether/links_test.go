package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/version"
)

// TestUpdateWithNothingToInstallLinks runs this binary installed as
// aether, the layout an updater from before the second names leaves:
// `aether update` that finds it is already on the release still creates
// cloudfield beside it, and says nothing where it may not, since it would
// say it again on every run.
func TestUpdateWithNothingToInstallLinks(t *testing.T) {
	if argv := os.Getenv("AETHER_CLI_TEST_ARGV"); argv != "" {
		os.Args = strings.Split(argv, "\x1f")
		main()
		return
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	dir := t.TempDir()
	self, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = self.Close() }()
	f, err := os.OpenFile(filepath.Join(dir, "aether"), os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(f, self); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	update := func() string {
		t.Helper()
		cmd := exec.Command(filepath.Join(dir, "aether"), "-test.run=^TestUpdateWithNothingToInstallLinks$")
		home := t.TempDir()
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "AETHER_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+home, "XDG_CONFIG_HOME="+home,
			"AETHER_CLI_TEST_ARGV="+strings.Join([]string{"aether", "update", "--version", version.Version, "--no-app"}, "\x1f"))
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("aether update: %v\n%s", runErr, out)
		}
		return string(out)
	}

	if err = os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	if out := update(); strings.Contains(out, "warning") || strings.Contains(out, "ln -s") {
		t.Fatalf("update with nothing to install in a directory it may not write printed:\n%s", out)
	}

	if err = os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out := update(); strings.Contains(out, "warning") {
		t.Fatalf("update printed a warning:\n%s", out)
	}
	if got, err := os.Readlink(filepath.Join(dir, "cloudfield")); err != nil || got != "aether" {
		t.Fatalf("cloudfield -> %q, %v; want aether", got, err)
	}
}
