package localops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

func TestInstallDesktopLinuxRegistersLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linux layout uses unix permissions")
	}
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	built := t.TempDir()
	if err := os.WriteFile(filepath.Join(built, "aether-desktop"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(built, "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(built, "resources", "app.asar"), []byte("asar"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A leftover from an earlier install must be replaced, not merged.
	stale := filepath.Join(home, ".local", "share", "aether", "desktop", "old.so")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	app, err := InstallDesktop("linux", home, built, []byte("png"))
	if err != nil {
		t.Fatalf("InstallDesktop: %v", err)
	}
	wantApp := filepath.Join(home, ".local", "share", "aether", "desktop")
	if app.App != wantApp {
		t.Fatalf("App = %q, want %q", app.App, wantApp)
	}
	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Fatalf("stale file survived: %v", statErr)
	}
	info, err := os.Stat(filepath.Join(app.App, "aether-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("aether-desktop lost its execute bit: %v", info.Mode())
	}
	if _, statErr := os.Stat(filepath.Join(app.App, "resources", "app.asar")); statErr != nil {
		t.Fatal(statErr)
	}
	entry, err := os.ReadFile(app.Launcher)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Exec=\"" + filepath.Join(wantApp, "aether-desktop") + "\" --no-sandbox %U\n",
		"Icon=" + filepath.Join(wantApp, desktopIcon) + "\n",
		"MimeType=x-scheme-handler/aether;\n",
	} {
		if !strings.Contains(string(entry), want) {
			t.Errorf("launcher missing %q:\n%s", want, entry)
		}
	}
}

func TestDesktopLayoutHonorsXDGDataHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/data is not an absolute path on windows")
	}
	t.Setenv("XDG_DATA_HOME", "/data")
	app, err := desktopLayout("linux", "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if app.App != filepath.Join("/data", "aether", "desktop") || app.Launcher != filepath.Join("/data", "applications", "aether-desktop.desktop") {
		t.Fatalf("layout = %+v", app)
	}
}

func TestDesktopLayoutDarwinPrefersSystemApplications(t *testing.T) {
	system := t.TempDir()
	setMacSystemApplications(t, system)
	app, err := desktopLayout("darwin", "/Users/u")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(system, "Aether.app")
	if app.App != want || app.Launcher != want || app.Superseded != filepath.Join("/Users/u", "Applications", "Aether.app") {
		t.Fatalf("layout = %+v, want %s", app, want)
	}
	if entries, _ := os.ReadDir(system); len(entries) != 0 {
		t.Fatalf("probe left %v behind", entries)
	}
}

func TestDesktopLayoutDarwinFallsBackToHomeApplications(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes are not enforced on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	system := t.TempDir()
	if err := os.Chmod(system, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(system, 0o755) })
	setMacSystemApplications(t, system)
	app, err := desktopLayout("darwin", "/Users/u")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/Users/u", "Applications", "Aether.app")
	if app.App != want || app.Launcher != want || app.Superseded != filepath.Join(system, "Aether.app") {
		t.Fatalf("layout = %+v, want %s", app, want)
	}
}

// builtBundle is a minimal .app for InstallDesktop to copy.
func builtBundle(t *testing.T) string {
	t.Helper()
	built := t.TempDir()
	if err := os.MkdirAll(filepath.Join(built, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(built, "Contents", "MacOS", "Aether"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return built
}

func TestInstallDesktopDarwinReplacesTheHomeCopy(t *testing.T) {
	system := t.TempDir()
	setMacSystemApplications(t, system)
	home := t.TempDir()
	stale := filepath.Join(home, "Applications", "Aether.app", "Contents", "old")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}

	app, err := InstallDesktop("darwin", home, builtBundle(t), nil)
	if err != nil {
		t.Fatalf("InstallDesktop: %v", err)
	}
	if want := filepath.Join(system, "Aether.app"); app.App != want {
		t.Fatalf("App = %q, want %q", app.App, want)
	}
	if _, statErr := os.Stat(filepath.Join(app.App, "Contents", "MacOS", "Aether")); statErr != nil {
		t.Fatal(statErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, "Applications", "Aether.app")); !os.IsNotExist(statErr) {
		t.Fatalf("the ~/Applications copy survived: %v", statErr)
	}
}

func TestInstallDesktopDarwinReportsAnUnremovableSystemCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes are not enforced on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	system := t.TempDir()
	stale := filepath.Join(system, "Aether.app")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(system, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(system, 0o755) })
	setMacSystemApplications(t, system)
	home := t.TempDir()

	_, err := InstallDesktop("darwin", home, builtBundle(t), nil)
	if err == nil {
		t.Fatal("a surviving second Aether.app was not reported")
	}
	for _, want := range []string{stale, "sudo rm -rf"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// The install itself went through: the fallback bundle is complete.
	if _, statErr := os.Stat(filepath.Join(home, "Applications", "Aether.app", "Contents", "MacOS", "Aether")); statErr != nil {
		t.Fatal(statErr)
	}
}

func setMacSystemApplications(t *testing.T, dir string) {
	t.Helper()
	previous := macSystemApplications
	macSystemApplications = dir
	t.Cleanup(func() { macSystemApplications = previous })
}

func TestDesktopLayoutRejectsUnknownOS(t *testing.T) {
	if _, err := desktopLayout("plan9", "/h"); err == nil {
		t.Fatal("plan9 accepted")
	}
}

func TestDesktopExecQuoteEscapesReservedCharacters(t *testing.T) {
	got := desktopExecQuote(`/home/o"neil/$HOME/50%off/back\slash`)
	want := `"/home/o\"neil/\$HOME/50%%off/back\\slash"`
	if got != want {
		t.Fatalf("quote = %s, want %s", got, want)
	}
}

func TestDesktopLayoutIgnoresRelativeXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", ".")
	app, err := desktopLayout("linux", "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/u", ".local", "share", "aether", "desktop"); app.App != want {
		t.Fatalf("App = %q, want %q", app.App, want)
	}
}

func TestDesktopLayoutWindowsRejectsRelativeAppData(t *testing.T) {
	t.Setenv("LOCALAPPDATA", "Programs")
	t.Setenv("APPDATA", `C:\Users\u\AppData\Roaming`)
	if _, err := desktopLayout("windows", `C:\Users\u`); err == nil {
		t.Fatal("relative LOCALAPPDATA accepted")
	}
}

func TestBuiltDesktopAppFindsArchSuffixedOutput(t *testing.T) {
	dist := t.TempDir()
	for _, dir := range []string{"linux-arm64-unpacked", filepath.Join("mac-arm64", "Aether.app"), "win-unpacked"} {
		if err := os.MkdirAll(filepath.Join(dist, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for goos, want := range map[string]string{
		"linux":   "linux-arm64-unpacked",
		"darwin":  filepath.Join("mac-arm64", "Aether.app"),
		"windows": "win-unpacked",
	} {
		got, err := builtDesktopApp(goos, dist)
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if got != filepath.Join(dist, want) {
			t.Errorf("%s = %q, want %q", goos, got, want)
		}
	}
	if _, err := builtDesktopApp("linux", t.TempDir()); err == nil {
		t.Fatal("empty dist accepted")
	}
}

func TestWriteTreeOverwritesExistingSources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.js"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := fstest.MapFS{
		"main.js":            {Data: []byte("new")},
		"build/icons/16.png": {Data: []byte("png")},
	}
	if err := writeTree(dir, src); err != nil {
		t.Fatalf("writeTree: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "main.js"))
	if err != nil || string(got) != "new" {
		t.Fatalf("main.js = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "build", "icons", "16.png")); err != nil {
		t.Fatal(err)
	}
}

func TestStampShellVersion(t *testing.T) {
	manifest := `{"name":"aether-desktop","version":"0.1.0","main":"main.js"}`
	read := func(t *testing.T, dir string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("parse stamped manifest: %v", err)
		}
		return out
	}

	for _, tc := range []struct {
		name    string
		cli     string
		version string
	}{
		{"release tag", "v1.2.3", "1.2.3"},
		{"prerelease tag", "v1.2.3-rc1", "1.2.3-rc1"},
		// A dev build has no version electron-builder would accept, so the
		// manifest keeps its own and the dashboard comparison never matches.
		{"dev build", "dev", "0.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := stampShellVersion(dir, tc.cli); err != nil {
				t.Fatalf("stampShellVersion: %v", err)
			}
			got := read(t, dir)
			if got["version"] != tc.version {
				t.Fatalf("version = %v, want %s", got["version"], tc.version)
			}
			if got["main"] != "main.js" {
				t.Fatalf("main = %v, want main.js (other fields must survive)", got["main"])
			}
		})
	}
}
