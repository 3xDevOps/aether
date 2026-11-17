package localops

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeInstall puts an app's executable and identity under app, so app
// looks like an install of this app.
func fakeInstall(t *testing.T, goos, app string) string {
	t.Helper()
	exe := DesktopAppExe(goos, app)
	identity := filepath.Join(app, filepath.FromSlash(cliBindingFile))
	content := `{"path":"/usr/local/bin/aether"}`
	if goos == "darwin" {
		identity = filepath.Join(app, "Contents", "Info.plist")
		content = infoPlist(builder.AppID)
	}
	for file, data := range map[string]string{exe: "app", identity: content} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return exe
}

func infoPlist(bundleID string) string {
	return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\">\n<dict>\n" +
		"\t<key>CFBundleExecutable</key>\n\t<string>Aether</string>\n" +
		"\t<key>CFBundleIdentifier</key>\n\t<string>" + bundleID + "</string>\n</dict>\n</plist>\n"
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

func (f retireFixture) lookup() (RealUser, error) { return RealUser{Home: f.home}, nil }

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
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f := newRetireFixture(t, goos)
			if err := retireLegacyDesktop(goos, f.lookup, f.renamedExe); err != nil {
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
	legacyProduct(t)
	f := newRetireFixture(t, "linux")
	if err := retireLegacyDesktop("linux", f.lookup, DesktopAppExe("linux", f.legacy[0])); err != nil {
		t.Fatal(err)
	}
	f.assertLegacyKept(t)
}

// Only a shell of an install under the new name removes anything: an old
// shell does not set the variable, a legacy or development shell is not
// such an install, and neither is a path that does not exist.
func TestRetireLegacyDesktopNeedsTheRenamedShell(t *testing.T) {
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
				if err := retireLegacyDesktop(goos, f.lookup, shell); err != nil {
					t.Fatalf("shell %q: %v", shell, err)
				}
				f.assertLegacyKept(t)
			}
		})
	}
}

func TestRetireLegacyDesktopFollowsASymlinkedShell(t *testing.T) {
	f := newRetireFixture(t, "linux")
	link := filepath.Join(t.TempDir(), "cloudfield")
	symlink(t, f.renamedExe, link)
	if err := retireLegacyDesktop("linux", f.lookup, link); err != nil {
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
	t.Run("symlink", func(t *testing.T) {
		f := newRetireFixture(t, "linux")
		if err := os.RemoveAll(f.legacy[0]); err != nil {
			t.Fatal(err)
		}
		elsewhere := t.TempDir()
		fakeInstall(t, "linux", elsewhere)
		symlink(t, elsewhere, f.legacy[0])
		if err := retireLegacyDesktop("linux", f.lookup, f.renamedExe); err != nil {
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
		if err := retireLegacyDesktop("linux", f.lookup, f.renamedExe); err != nil {
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
		symlink(t, filepath.Dir(f.renamed[0]), legacyParent)
		if err := retireLegacyDesktop("linux", f.lookup, f.renamedExe); err != nil {
			t.Fatal(err)
		}
		mustExist(t, f.renamedExe)
	})
}

func TestRetireLegacyDesktopReportsAFailedRemoval(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a unix account that directory permissions apply to")
	}
	f := newRetireFixture(t, "linux")
	parent := filepath.Dir(f.legacy[0])
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	err := retireLegacyDesktop("linux", f.lookup, f.renamedExe)
	if err == nil || !strings.Contains(err.Error(), f.legacy[0]) || !strings.Contains(err.Error(), "delete it by hand") {
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
	f := newRetireFixture(t, "linux")
	target := filepath.Join(t.TempDir(), "share")
	share := filepath.Dir(filepath.Dir(f.legacy[0]))
	if err := os.Rename(share, target); err != nil {
		t.Fatal(err)
	}
	symlink(t, target, share)
	legacyExe := DesktopAppExe("linux", f.legacy[0])
	if err := os.Remove(legacyExe); err != nil {
		t.Fatal(err)
	}
	startAppExe(t, filepath.Join(target, "aether", "desktop", filepath.Base(legacyExe)))
	if !waitFor(func() bool { return DesktopAppRunning("linux", filepath.Join(target, "aether", "desktop")) }) {
		t.Fatal("the legacy app did not start")
	}
	if err := retireLegacyDesktop("linux", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustExist(t, legacyExe)
}

// A Windows app that could not be removed keeps its Start Menu shortcut, so
// it can still be started.
func TestRetireLegacyDesktopKeepsTheShortcutOfAnAppStillInstalled(t *testing.T) {
	f := newRetireFixture(t, "windows")
	if err := os.Remove(DesktopAppExe("windows", f.legacy[0])); err != nil {
		t.Fatal(err)
	}
	if err := retireLegacyDesktop("windows", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustExist(t, f.legacy[0])
	mustExist(t, f.shortcut)
}

// With one product name the legacy and current locations are the same
// list, so an app in one location must not make the other a legacy one.
func TestRetireLegacyDesktopIsANoOpUnderTheLegacyNameInEveryLocation(t *testing.T) {
	legacyProduct(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	system := filepath.Join(root, "Applications")
	setMacSystemApplications(t, system)
	t.Setenv("XDG_DATA_HOME", xdg)
	lookup := func() (RealUser, error) { return RealUser{Home: home}, nil }
	for goos, apps := range map[string][]string{
		"linux":  {filepath.Join(xdg, "aether", "desktop"), filepath.Join(home, ".local", "share", "aether", "desktop")},
		"darwin": {filepath.Join(system, "Aether.app"), filepath.Join(home, "Applications", "Aether.app")},
	} {
		exes := []string{fakeInstall(t, goos, apps[0]), fakeInstall(t, goos, apps[1])}
		for _, shell := range exes {
			if err := retireLegacyDesktop(goos, lookup, shell); err != nil {
				t.Fatalf("%s: %v", goos, err)
			}
			for _, exe := range exes {
				mustExist(t, exe)
			}
		}
	}
}

func TestRetireLegacyDesktopLooksUpTheUserOnlyAfterTheRename(t *testing.T) {
	failing := func() (RealUser, error) { return RealUser{}, errors.New("no home") }
	t.Run("legacy", func(t *testing.T) {
		legacyProduct(t)
		if err := retireLegacyDesktop("linux", failing, "/some/shell"); err != nil {
			t.Fatalf("under the legacy name: %v", err)
		}
	})
	if err := retireLegacyDesktop("linux", failing, ""); err != nil {
		t.Fatalf("without a shell: %v", err)
	}
	if err := retireLegacyDesktop("linux", failing, "/some/shell"); err == nil {
		t.Fatal("a failed lookup after the rename was swallowed")
	}
}

func TestRetireLegacyDesktopRemovesNothingAsRoot(t *testing.T) {
	previous := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = previous })
	f := newRetireFixture(t, "linux")
	if err := retireLegacyDesktop("linux", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	f.assertLegacyKept(t)
}

// Another product called Aether has the same bundle and executable names
// on darwin; only this app's bundle id makes it the old app. Elsewhere the
// CLI binding InstallDesktop writes marks it.
func TestRetireLegacyDesktopKeepsAnotherAppOfTheSameName(t *testing.T) {
	for _, tc := range []struct {
		name, goos, file, content string
	}{
		{"darwin bundle id", "darwin", filepath.Join("Contents", "Info.plist"), infoPlist("com.example.aether")},
		{"darwin binary plist", "darwin", filepath.Join("Contents", "Info.plist"), "bplist00"},
		{"darwin no plist", "darwin", filepath.Join("Contents", "Info.plist"), ""},
		{"linux no binding", "linux", filepath.FromSlash(cliBindingFile), ""},
		{"windows no binding", "windows", filepath.FromSlash(cliBindingFile), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetireFixture(t, tc.goos)
			for _, app := range f.legacy {
				file := filepath.Join(app, tc.file)
				var err error
				if tc.content == "" {
					err = os.Remove(file)
				} else {
					err = os.WriteFile(file, []byte(tc.content), 0o644)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := retireLegacyDesktop(tc.goos, f.lookup, f.renamedExe); err != nil {
				t.Fatal(err)
			}
			f.assertLegacyKept(t)
		})
	}
}

func TestOwnedByUser(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a unix account that does not own /")
	}
	for path, want := range map[string]bool{t.TempDir(): true, "/": false} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := ownedByUser(info); got != want {
			t.Errorf("ownedByUser(%s) = %v, want %v", path, got, want)
		}
	}
}

// pgrep matches the path an app was started by: a darwin app started
// through a symlinked Applications folder is still seen running.
func TestRetireLegacyDesktopKeepsAnOldAppRunningByItsListedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs pgrep")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("no pgrep")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "Applications")
	symlink(t, target, link)
	f := newRetireFixture(t, "darwin")
	setMacSystemApplications(t, link)
	app := filepath.Join(link, "Aether.app")
	fakeInstall(t, "darwin", app)
	startAppExe(t, DesktopAppExe("darwin", app))
	if !waitFor(func() bool { return DesktopAppRunning("darwin", app) }) {
		t.Fatal("the legacy app did not start")
	}
	if err := retireLegacyDesktop("darwin", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustExist(t, DesktopAppExe("darwin", app))
}

// A delete that fails after the app was moved aside names the moved
// directory and is finished at the next start. The shortcut already went:
// it pointed at the app's old place.
func TestRetireLegacyDesktopRetriesAFailedDelete(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a unix account that directory permissions apply to")
	}
	f := newRetireFixture(t, "windows")
	locked := filepath.Join(f.legacy[0], "resources")
	if err := os.WriteFile(filepath.Join(locked, "app.asar"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(filepath.Dir(f.legacy[0]), retiredPrefix+"Aether Desktop")
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(retired, "resources"), 0o755) })
	err := retireLegacyDesktop("windows", f.lookup, f.renamedExe)
	if err == nil || !strings.Contains(err.Error(), retired) || !strings.Contains(err.Error(), "window still open") {
		t.Fatalf("err = %v, want the moved directory and the remove hint", err)
	}
	mustBeGone(t, f.legacy[0])
	mustBeGone(t, f.shortcut)
	if err := os.Chmod(filepath.Join(retired, "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := retireLegacyDesktop("windows", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustBeGone(t, retired)
	mustExist(t, f.renamedExe)
	mustExist(t, f.renamedShortcut)
}

// The renamed app's own set-aside copy from an install swap shares the
// parent on windows and is never touched.
func TestRetireLegacyDesktopKeepsTheRenamedAppsSetAsideCopy(t *testing.T) {
	f := newRetireFixture(t, "windows")
	aside := filepath.Join(filepath.Dir(f.renamed[0]), installOldPrefix+"swap", "Cloudfield Desktop")
	exe := fakeInstall(t, "windows", aside)
	if err := retireLegacyDesktop("windows", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustExist(t, exe)
}

// A machine that never had the legacy app keeps whatever Aether.lnk it has.
func TestRetireLegacyDesktopKeepsAShortcutWithoutAnOldApp(t *testing.T) {
	f := newRetireFixture(t, "windows")
	if err := os.RemoveAll(f.legacy[0]); err != nil {
		t.Fatal(err)
	}
	if err := retireLegacyDesktop("windows", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustExist(t, f.shortcut)
}

// A shortcut that could not be removed is tried again at the next start:
// the moved app stays until it is gone.
func TestRetireLegacyDesktopRetriesTheShortcut(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a unix account that directory permissions apply to")
	}
	f := newRetireFixture(t, "windows")
	if err := os.Chmod(f.programs, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.programs, 0o755) })
	err := retireLegacyDesktop("windows", f.lookup, f.renamedExe)
	if err == nil || !strings.Contains(err.Error(), f.shortcut) {
		t.Fatalf("err = %v, want the shortcut", err)
	}
	retired := filepath.Join(filepath.Dir(f.legacy[0]), retiredPrefix+"Aether Desktop")
	mustBeGone(t, f.legacy[0])
	mustExist(t, retired)
	if err := os.Chmod(f.programs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := retireLegacyDesktop("windows", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustBeGone(t, f.shortcut)
	mustBeGone(t, retired)
	mustExist(t, f.renamedShortcut)
}

// An old app installed again after a removal that did not finish keeps its
// shortcut while the earlier copy is deleted.
func TestRetireLegacyDesktopKeepsTheShortcutOfAReinstalledApp(t *testing.T) {
	f := newRetireFixture(t, "windows")
	retired := filepath.Join(filepath.Dir(f.legacy[0]), retiredPrefix+"Aether Desktop")
	fakeInstall(t, "windows", retired)
	if err := retireLegacyDesktop("windows", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustBeGone(t, retired)
	f.assertLegacyKept(t)
}

// XDG_DATA_HOME inside the old app's folder puts the running renamed app
// inside it; the old folder then stays.
func TestRetireLegacyDesktopKeepsAnOldFolderHoldingTheRunningApp(t *testing.T) {
	f := newRetireFixture(t, "linux")
	xdg := filepath.Join(f.legacy[0], "x")
	t.Setenv("XDG_DATA_HOME", xdg)
	shell := fakeInstall(t, "linux", filepath.Join(xdg, "cloudfield", "desktop"))
	if err := retireLegacyDesktop("linux", f.lookup, shell); err != nil {
		t.Fatal(err)
	}
	mustExist(t, shell)
	f.assertLegacyKept(t)
}

func TestWithin(t *testing.T) {
	folds := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	for _, tc := range []struct {
		dir, p string
		want   bool
	}{
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b", false},
		{"/a/b", "/a/bc", false},
		{"/a/b/c", "/a/b", false},
		{"/A/b", "/a/b/c", folds},
	} {
		dir, p := filepath.FromSlash(tc.dir), filepath.FromSlash(tc.p)
		if got := within(dir, p); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", dir, p, got, tc.want)
		}
	}
}

// A .aether-retired- entry that is not a real directory is not a removal to
// finish: a symlink is neither followed nor removed. Its owner is checked
// too, which a test cannot set up without a second account.
func TestRetireLegacyDesktopRetriesOnlyARealDirectory(t *testing.T) {
	f := newRetireFixture(t, "linux")
	elsewhere := t.TempDir()
	keep := filepath.Join(elsewhere, "keep")
	if err := os.WriteFile(keep, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(filepath.Dir(f.legacy[0]), retiredPrefix+"desktop")
	symlink(t, elsewhere, retired)
	if err := os.RemoveAll(f.legacy[0]); err != nil {
		t.Fatal(err)
	}
	if err := retireLegacyDesktop("linux", f.lookup, f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustExist(t, retired)
	mustExist(t, keep)
}

// RetireLegacyDesktop acts on this process's own home, not on SUDO_USER's.
func TestRetireLegacyDesktopUsesTheProcessHome(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a unix account that is not root")
	}
	f := newRetireFixture(t, "linux")
	t.Setenv("HOME", f.home)
	t.Setenv("SUDO_USER", "no-such-account-for-this-test")
	if err := RetireLegacyDesktop("linux", f.renamedExe); err != nil {
		t.Fatal(err)
	}
	mustBeGone(t, f.legacy[0])
	mustExist(t, f.renamedExe)
}

// symlink skips the test where Windows lets only administrators and
// developer mode create symlinks.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		t.Fatal(err)
	}
}
