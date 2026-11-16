package localops

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// renameProduct builds as an app whose electron-builder.yml names it
// Cloudfield.
func renameProduct(t *testing.T) {
	t.Helper()
	previous := productName
	productName = "Cloudfield"
	t.Cleanup(func() { productName = previous })
}

func TestEmbeddedProductNameIsReadFromElectronBuilder(t *testing.T) {
	if got := embeddedProductName(); got != productName || got == "" {
		t.Fatalf("embeddedProductName = %q, productName = %q", got, productName)
	}
}

func TestDesktopAppPathsListTheRenamedAppThenTheLegacyOne(t *testing.T) {
	renameProduct(t)
	setMacSystemApplications(t, "/Applications")
	for goos, want := range map[string][]string{
		"linux": {
			"/data/xdg/cloudfield/desktop", "/home/u/.local/share/cloudfield/desktop",
			"/data/xdg/aether/desktop", "/home/u/.local/share/aether/desktop",
		},
		"darwin": {
			"/Applications/Cloudfield.app", "/home/u/Applications/Cloudfield.app",
			"/Applications/Aether.app", "/home/u/Applications/Aether.app",
		},
	} {
		if got := desktopAppPaths(goos, "/home/u", "/data/xdg", ""); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s = %q, want %q", goos, got, want)
		}
	}
	local := t.TempDir()
	want := []string{filepath.Join(local, "Programs", "Cloudfield Desktop"), filepath.Join(local, "Programs", "Aether Desktop")}
	if got := desktopAppPaths("windows", "", "", local); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("windows = %q, want %q", got, want)
	}
}

// An app installed before the rename is still found, so `aether update`
// and the dashboard's Update rebuild it; once the renamed app is in place,
// that one is.
func TestInstalledDesktopAppFindsEitherName(t *testing.T) {
	renameProduct(t)
	home := filepath.ToSlash(t.TempDir())
	system := filepath.ToSlash(t.TempDir())
	setMacSystemApplications(t, system)
	local := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("LOCALAPPDATA", local)
	for _, tc := range []struct{ goos, legacy, renamed string }{
		{"linux", path.Join(home, ".local", "share", "aether", "desktop"), path.Join(home, ".local", "share", "cloudfield", "desktop")},
		{"darwin", path.Join(system, "Aether.app"), path.Join(home, "Applications", "Cloudfield.app")},
		{"windows", filepath.Join(local, "Programs", "Aether Desktop"), filepath.Join(local, "Programs", "Cloudfield Desktop")},
	} {
		if err := os.MkdirAll(tc.legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if got, ok := InstalledDesktopApp(tc.goos, RealUser{Home: home}); !ok || got != tc.legacy {
			t.Errorf("%s with the legacy app = %q, %v; want %q", tc.goos, got, ok, tc.legacy)
		}
		if err := os.MkdirAll(tc.renamed, 0o755); err != nil {
			t.Fatal(err)
		}
		if got, ok := InstalledDesktopApp(tc.goos, RealUser{Home: home}); !ok || got != tc.renamed {
			t.Errorf("%s with both apps = %q, %v; want %q", tc.goos, got, ok, tc.renamed)
		}
	}
}

func TestInstallDesktopLinuxPutsARenamedAppBesideTheLegacyOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linux layout uses unix permissions")
	}
	renameProduct(t)
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	data := filepath.Join(home, ".local", "share")
	legacy := map[string]string{
		filepath.Join(data, "aether", "desktop", "aether-desktop"):    "old app",
		filepath.Join(data, "applications", "aether-desktop.desktop"): "old launcher",
	}
	for file, contents := range legacy {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	built := t.TempDir()
	if err := os.WriteFile(filepath.Join(built, "aether-desktop"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	app, err := InstallDesktop("linux", home, DesktopFrom(built), "/opt/aether/aether", []byte("png"))
	if err != nil {
		t.Fatalf("InstallDesktop: %v", err)
	}
	if want := filepath.Join(data, "cloudfield", "desktop"); app.App != want {
		t.Fatalf("App = %q, want %q", app.App, want)
	}
	entry, err := os.ReadFile(filepath.Join(data, "applications", "cloudfield-desktop.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Name=Cloudfield\n", "Exec=\"" + filepath.Join(app.App, "aether-desktop") + "\"", "MimeType=x-scheme-handler/aether;x-scheme-handler/cloudfield;\n"} {
		if !strings.Contains(string(entry), want) {
			t.Errorf("launcher missing %q:\n%s", want, entry)
		}
	}
	for file, want := range legacy {
		if got, err := os.ReadFile(file); err != nil || string(got) != want {
			t.Errorf("the legacy %s = %q, %v; want it left in place", file, got, err)
		}
	}
}

func TestInstallDesktopDarwinPutsARenamedAppBesideTheLegacyOne(t *testing.T) {
	renameProduct(t)
	system := t.TempDir()
	setMacSystemApplications(t, system)
	home := t.TempDir()
	legacy := filepath.Join(home, "Applications", "Aether.app", "Contents", "MacOS", "Aether")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("old app"), 0o755); err != nil {
		t.Fatal(err)
	}

	app, err := InstallDesktop("darwin", home, DesktopFrom(builtBundle(t)), "/opt/aether/aether", nil)
	if err != nil {
		t.Fatalf("InstallDesktop: %v", err)
	}
	if want := filepath.Join(system, "Cloudfield.app"); app.App != want {
		t.Fatalf("App = %q, want %q", app.App, want)
	}
	if got, err := os.ReadFile(legacy); err != nil || string(got) != "old app" {
		t.Fatalf("the legacy bundle = %q, %v; want it left in place", got, err)
	}
}

func TestDesktopLayoutWindowsNamesTheRenamedApp(t *testing.T) {
	renameProduct(t)
	local := t.TempDir()
	programs := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	setWindowsProgramsResolver(t, func() (string, error) { return programs, nil })
	app, err := desktopLayout("windows", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(local, "Programs", "Cloudfield Desktop"); app.App != want {
		t.Errorf("App = %q, want %q", app.App, want)
	}
	if want := filepath.Join(programs, "Cloudfield.lnk"); app.Launcher != want {
		t.Errorf("Launcher = %q, want %q", app.Launcher, want)
	}
}

func TestBuiltDesktopAppFindsTheRenamedBundle(t *testing.T) {
	renameProduct(t)
	dist := t.TempDir()
	want := filepath.Join(dist, "mac-arm64", "Cloudfield.app")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := builtDesktopApp("darwin", dist); err != nil || got != want {
		t.Fatalf("builtDesktopApp = %q, %v; want %q", got, err, want)
	}
}

// startAppExe runs a copy of sleep as the app's executable at exe, the way
// the desktop starts it, until the test ends.
func startAppExe(t *testing.T, exe string) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	raw, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
}

// `aether update` tells the user to restart an app that is running, whichever
// name it was installed under.
func TestDesktopAppRunningUnderEitherName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("DesktopAppRunning answers false on windows")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("no pgrep")
	}
	renameProduct(t)
	for _, name := range []string{"Aether", "Cloudfield"} {
		bundle := filepath.Join(t.TempDir(), name+".app")
		if DesktopAppRunning("darwin", bundle) {
			t.Fatalf("darwin %s reported running before it started", name)
		}
		startAppExe(t, filepath.Join(bundle, "Contents", "MacOS", name))
		if !waitFor(func() bool { return DesktopAppRunning("darwin", bundle) }) {
			t.Errorf("darwin %s running = false", name)
		}
		if runtime.GOOS != "linux" {
			continue
		}
		dir := filepath.Join(t.TempDir(), strings.ToLower(name), "desktop")
		startAppExe(t, filepath.Join(dir, "aether-desktop"))
		if !waitFor(func() bool { return DesktopAppRunning("linux", dir) }) {
			t.Errorf("linux %s running = false", name)
		}
	}
}

func waitFor(cond func() bool) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return false
}
