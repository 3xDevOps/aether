//go:build !windows

// The install swap with a running app. The fixture holds an open file
// handle inside the installed directory, which is what a live Electron
// does; only a unix filesystem lets the rename go ahead while it is open,
// and Windows is where InstallDesktop still tells the user to close the
// window.

package localops

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unpacked writes a one-file "app" carrying marker, the way
// electron-builder leaves one under dist.
func unpacked(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aether-desktop"), []byte(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInstallDesktopSwapsUnderARunningApp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")

	first, err := InstallDesktop("linux", home, unpacked(t, "old"), []byte("png"))
	if err != nil {
		t.Fatalf("InstallDesktop: %v", err)
	}
	// A running app keeps its own files open. Deleting the directory under
	// it would take the window down; a rename must not.
	running, err := os.Open(filepath.Join(first.App, "aether-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = running.Close() }()

	second, err := InstallDesktop("linux", home, unpacked(t, "new"), []byte("png"))
	if err != nil {
		t.Fatalf("second InstallDesktop: %v", err)
	}
	if second.App != first.App {
		t.Fatalf("installed at %s, want the same %s", second.App, first.App)
	}
	// The app the user launches now is the new one.
	installed, err := os.ReadFile(filepath.Join(second.App, "aether-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != "new" {
		t.Fatalf("installed app = %q, want the new build", installed)
	}
	// The window still running off the old files can still read them.
	stillOpen, err := io.ReadAll(running)
	if err != nil {
		t.Fatalf("the running app lost its own binary: %v", err)
	}
	if string(stillOpen) != "old" {
		t.Fatalf("the running app reads %q, want the build it started from", stillOpen)
	}
	// The icon rides inside the app, so it has to survive the swap.
	if _, err := os.Stat(filepath.Join(second.App, desktopIcon)); err != nil {
		t.Fatalf("icon: %v", err)
	}
	leftovers(t, filepath.Dir(second.App))
}

// A build directory left behind by an interrupted install is swept by the
// next one, so the Applications folder does not fill up with copies.
func TestInstallDesktopSweepsEarlierLeftovers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	parent := filepath.Join(home, ".local", "share", "aether")
	if err := os.MkdirAll(filepath.Join(parent, installOldPrefix+"stale", "desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	app, err := InstallDesktop("linux", home, unpacked(t, "new"), []byte("png"))
	if err != nil {
		t.Fatalf("InstallDesktop: %v", err)
	}
	leftovers(t, filepath.Dir(app.App))
}

// leftovers fails when a swap left a staging or set-aside directory behind.
func leftovers(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), installOldPrefix) || strings.HasPrefix(entry.Name(), installStagingPrefix) {
			t.Fatalf("%s left behind in %s", entry.Name(), parent)
		}
	}
}
