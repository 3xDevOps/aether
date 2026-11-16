package localops

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeInstall puts an app's executable under app, so app looks installed.
func fakeInstall(t *testing.T, goos, app string) string {
	t.Helper()
	exe := DesktopAppExe(goos, app)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("%s was removed: %v", p, err)
	}
}

func mustBeGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s still exists (err %v)", p, err)
	}
}

type retireFixture struct {
	goos                  string
	home, local, programs string
	legacy, renamed       []string
	renamedExe            string
	dataDir, shortcut     string
	renamedShortcut       string
}

// newRetireFixture installs the legacy and the renamed app for goos in a
// temporary home, with the data directory and launchers beside them.
func newRetireFixture(t *testing.T, goos string) retireFixture {
	t.Helper()
	root := t.TempDir()
	f := retireFixture{
		goos:     goos,
		home:     filepath.Join(root, "home"),
		local:    filepath.Join(root, "local"),
		programs: filepath.Join(root, "programs"),
	}
	system := filepath.Join(root, "Applications")
	setMacSystemApplications(t, system)
	setWindowsProgramsResolver(t, func() (string, error) { return f.programs, nil })
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("LOCALAPPDATA", f.local)
	switch goos {
	case "linux":
		share := filepath.Join(f.home, ".local", "share")
		f.legacy = []string{filepath.Join(share, "aether", "desktop")}
		f.renamed = []string{filepath.Join(share, "cloudfield", "desktop")}
		f.dataDir = filepath.Join(f.home, ".config", "aether-desktop")
	case "darwin":
		f.legacy = []string{filepath.Join(system, "Aether.app"), filepath.Join(f.home, "Applications", "Aether.app")}
		f.renamed = []string{filepath.Join(f.home, "Applications", "Cloudfield.app")}
		f.dataDir = filepath.Join(f.home, "Library", "Application Support", "aether-desktop")
	case "windows":
		f.legacy = []string{filepath.Join(f.local, "Programs", "Aether Desktop")}
		f.renamed = []string{filepath.Join(f.local, "Programs", "Cloudfield Desktop")}
		f.dataDir = filepath.Join(root, "roaming", "aether-desktop")
		f.shortcut = filepath.Join(f.programs, "Aether.lnk")
		f.renamedShortcut = filepath.Join(f.programs, "Cloudfield.lnk")
	}
	for _, app := range f.legacy {
		fakeInstall(t, goos, app)
	}
	f.renamedExe = fakeInstall(t, goos, f.renamed[0])
	if err := os.MkdirAll(f.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{f.shortcut, f.renamedShortcut} {
		if link == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(link, []byte("lnk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f retireFixture) user() RealUser { return RealUser{Home: f.home} }

func (f retireFixture) assertLegacyKept(t *testing.T) {
	t.Helper()
	for _, app := range f.legacy {
		mustExist(t, DesktopAppExe(f.goos, app))
	}
	if f.shortcut != "" {
		mustExist(t, f.shortcut)
	}
}

func TestRetireLegacyDesktopRemovesTheOldAppOnceTheRenamedOneRuns(t *testing.T) {
	renameProduct(t)
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f := newRetireFixture(t, goos)
			if err := RetireLegacyDesktop(goos, f.user(), f.renamedExe); err != nil {
				t.Fatal(err)
			}
			for _, app := range f.legacy {
				mustBeGone(t, app)
			}
			mustExist(t, f.renamedExe)
			mustExist(t, f.dataDir)
			if goos == "windows" {
				mustBeGone(t, f.shortcut)
				mustExist(t, f.renamedShortcut)
			}
		})
	}
}

func TestRetireLegacyDesktopIsANoOpUnderTheLegacyName(t *testing.T) {
	f := newRetireFixture(t, "linux")
	if err := RetireLegacyDesktop("linux", f.user(), DesktopAppExe("linux", f.legacy[0])); err != nil {
		t.Fatal(err)
	}
	f.assertLegacyKept(t)
}

// Only a shell of an install under the new name removes anything: an old
// shell does not set the variable, a legacy or development shell is not
// such an install, and neither is a path that does not exist.
func TestRetireLegacyDesktopNeedsTheRenamedShell(t *testing.T) {
	renameProduct(t)
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f := newRetireFixture(t, goos)
			dev := filepath.Join(t.TempDir(), "node_modules", "electron", "dist", "electron")
			if err := os.MkdirAll(filepath.Dir(dev), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dev, nil, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, shell := range []string{"", DesktopAppExe(goos, f.legacy[0]), dev, filepath.Join(f.renamed[0], "missing")} {
				if err := RetireLegacyDesktop(goos, f.user(), shell); err != nil {
					t.Fatalf("shell %q: %v", shell, err)
				}
				f.assertLegacyKept(t)
			}
		})
	}
}

func TestRetireLegacyDesktopFollowsASymlinkedShell(t *testing.T) {
	renameProduct(t)
	f := newRetireFixture(t, "linux")
	link := filepath.Join(t.TempDir(), "cloudfield")
	if err := os.Symlink(f.renamedExe, link); err != nil {
		t.Fatal(err)
	}
	if err := RetireLegacyDesktop("linux", f.user(), link); err != nil {
		t.Fatal(err)
	}
	mustBeGone(t, f.legacy[0])
	mustExist(t, f.renamedExe)
}

// A legacy location that is not a plain app install is left alone: a
// symlink (here to the renamed app, which a parent directory symlink would
// also resolve to), a directory without the app's executable, and a
// directory that resolves to the running app.
func TestRetireLegacyDesktopKeepsWhatIsNotAnOldInstall(t *testing.T) {
	renameProduct(t)
	t.Run("symlink", func(t *testing.T) {
		f := newRetireFixture(t, "linux")
		if err := os.RemoveAll(f.legacy[0]); err != nil {
			t.Fatal(err)
		}
		elsewhere := t.TempDir()
		fakeInstall(t, "linux", elsewhere)
		if err := os.Symlink(elsewhere, f.legacy[0]); err != nil {
			t.Fatal(err)
		}
		if err := RetireLegacyDesktop("linux", f.user(), f.renamedExe); err != nil {
			t.Fatal(err)
		}
		mustExist(t, f.legacy[0])
		mustExist(t, DesktopAppExe("linux", elsewhere))
	})
	t.Run("not an app", func(t *testing.T) {
		f := newRetireFixture(t, "linux")
		if err := os.Remove(DesktopAppExe("linux", f.legacy[0])); err != nil {
			t.Fatal(err)
		}
		keep := filepath.Join(f.legacy[0], "notes.txt")
		if err := os.WriteFile(keep, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := RetireLegacyDesktop("linux", f.user(), f.renamedExe); err != nil {
			t.Fatal(err)
		}
		mustExist(t, keep)
	})
	t.Run("parent symlinked to the running app", func(t *testing.T) {
		f := newRetireFixture(t, "linux")
		legacyParent := filepath.Dir(f.legacy[0])
		if err := os.RemoveAll(legacyParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Dir(f.renamed[0]), legacyParent); err != nil {
			t.Fatal(err)
		}
		if err := RetireLegacyDesktop("linux", f.user(), f.renamedExe); err != nil {
			t.Fatal(err)
		}
		mustExist(t, f.renamedExe)
	})
}

func TestRetireLegacyDesktopReportsAFailedRemoval(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a unix account that directory permissions apply to")
	}
	renameProduct(t)
	f := newRetireFixture(t, "linux")
	parent := filepath.Dir(f.legacy[0])
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	err := RetireLegacyDesktop("linux", f.user(), f.renamedExe)
	if err == nil || !strings.Contains(err.Error(), f.legacy[0]) || !strings.Contains(err.Error(), "sudo rm -rf") {
		t.Fatalf("err = %v, want the path and the remove hint", err)
	}
	f.assertLegacyKept(t)
	mustExist(t, f.renamedExe)
}

// The legacy app is still running from behind a symlinked parent, where
// /proc names the resolved path.
func TestRetireLegacyDesktopKeepsARunningOldApp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	renameProduct(t)
	f := newRetireFixture(t, "linux")
	target := filepath.Join(t.TempDir(), "share")
	share := filepath.Dir(filepath.Dir(f.legacy[0]))
	if err := os.Rename(share, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, share); err != nil {
		t.Fatal(err)
	}
	legacyExe := DesktopAppExe("linux", f.legacy[0])
	if err := os.Remove(legacyExe); err != nil {
		t.Fatal(err)
	}
	startAppExe(t, filepath.Join(target, "aether", "desktop", filepath.Base(legacyExe)))
	if !waitFor(func() bool { return DesktopAppRunning("linux", filepath.Join(target, "aether", "desktop")) }) {
		t.Fatal("the legacy app did not start")
	}
	if err := RetireLegacyDesktop("linux", f.user(), f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustExist(t, legacyExe)
}

// A Windows app that could not be removed keeps its Start Menu shortcut, so
// it can still be started.
func TestRetireLegacyDesktopKeepsTheShortcutOfAnAppStillInstalled(t *testing.T) {
	renameProduct(t)
	f := newRetireFixture(t, "windows")
	if err := os.Remove(DesktopAppExe("windows", f.legacy[0])); err != nil {
		t.Fatal(err)
	}
	if err := RetireLegacyDesktop("windows", f.user(), f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustExist(t, f.legacy[0])
	mustExist(t, f.shortcut)
}
