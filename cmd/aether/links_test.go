package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

// TestUpdateWithNothingToInstallLinks runs this binary installed as
// aether (aether.exe on Windows), the layout an updater from before the second names leaves:
// `aether update` that finds it is already on the release still creates
// cloudfield (cloudfield.cmd on Windows) beside it, and says nothing where it may not, since it would
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
	bin := "aether"
	if runtime.GOOS == "windows" {
		bin = "aether.exe"
	}
	dir := t.TempDir()
	self, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = self.Close() }()
	f, err := os.OpenFile(filepath.Join(dir, bin), os.O_CREATE|os.O_WRONLY, 0o755)
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
		cmd := exec.Command(filepath.Join(dir, bin), "-test.run=^TestUpdateWithNothingToInstallLinks$")
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

	// A read-only directory is a Unix permission; on Windows chmod only
	// sets the read-only attribute, which does not stop a new file.
	if runtime.GOOS != "windows" {
		if err = os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		if out := update(); strings.Contains(out, "warning") || strings.Contains(out, "ln -s") {
			t.Fatalf("update with nothing to install in a directory it may not write printed:\n%s", out)
		}
		if err = os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if out := update(); strings.Contains(out, "warning") {
		t.Fatalf("update printed a warning:\n%s", out)
	}
	if runtime.GOOS == "windows" {
		if got, err := os.ReadFile(filepath.Join(dir, "cloudfield.cmd")); err != nil || string(got) != "@\"%~dp0aether.exe\" %*\r\n" {
			t.Fatalf("cloudfield.cmd = %q, %v; want the shim that runs aether.exe", got, err)
		}
		return
	}
	if got, err := os.Readlink(filepath.Join(dir, "cloudfield")); err != nil || got != "aether" {
		t.Fatalf("cloudfield -> %q, %v; want aether", got, err)
	}
}

// TestUpdateThatInstallsWarnsOfALinkItCannotMake drives runUpdate through
// an install with the swap stubbed: the links are made beside every
// replaced binary, and a link that cannot be made is a warning, never a
// failed update.
func TestUpdateThatInstallsWarnsOfALinkItCannotMake(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(selfupdate.PackagedEnv, "")
	self, err := executablePath()
	if err != nil {
		t.Fatal(err)
	}
	oldUpdate, oldLink := selfUpdate, linkBinaries
	t.Cleanup(func() { selfUpdate, linkBinaries = oldUpdate, oldLink })
	selfUpdate = func(context.Context, string, string) ([]string, error) { return []string{self}, nil }

	var linked []string
	linkBinaries = func(paths []string) error {
		linked = paths
		return errors.New("symlink aether /usr/local/bin/cloudfield: permission denied\nrun: sudo ln -s aether /usr/local/bin/cloudfield")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	runErr := runUpdate([]string{"--version", "v9.9.9", "--force", "--no-app"})
	os.Stderr = saved
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	stderr, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("runUpdate = %v, want the update to succeed despite the link", runErr)
	}
	if len(linked) != 1 || linked[0] != self {
		t.Fatalf("linked %v, want the replaced %s", linked, self)
	}
	if !strings.Contains(string(stderr), "warning: symlink aether") || strings.Count(string(stderr), "ln -s") != 1 {
		t.Fatalf("stderr = %q, want the warning with one ln -s line", stderr)
	}
}
