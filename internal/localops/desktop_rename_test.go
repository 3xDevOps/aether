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

	"gopkg.in/yaml.v3"

	"github.com/3xDevOps/cloudfield/desktop"
)

// legacyProduct builds as an app whose electron-builder.yml still names it
// Aether, as every release before the rename did.
func legacyProduct(t *testing.T) {
	t.Helper()
	previous := productName
	productName = legacyProductName
	t.Cleanup(func() { productName = previous })
}

func TestAppPathsNamedForEitherName(t *testing.T) {
	both := func(goos, home, xdg, local string) []string {
		return append(appPathsNamed(goos, "Cloudfield", home, xdg, local), appPathsNamed(goos, "Aether", home, xdg, local)...)
	}
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
		if got := both(goos, "/home/u", "/data/xdg", ""); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s = %q, want %q", goos, got, want)
		}
	}
	local := t.TempDir()
	want := []string{filepath.Join(local, "Programs", "Cloudfield Desktop"), filepath.Join(local, "Programs", "Aether Desktop")}
	if got := both("windows", "", "", local); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("windows = %q, want %q", got, want)
	}
}

// An app installed before the rename is still found, so `aether update`
// and the dashboard's Update rebuild it; once the renamed app is in place,
// that one is.
func TestInstalledDesktopAppFindsEitherName(t *testing.T) {
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
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	data := filepath.Join(home, ".local", "share")
	legacy := filepath.Join(data, "aether", "desktop", "aether-desktop")
	launcher := filepath.Join(data, "applications", "aether-desktop.desktop")
	for _, file := range []string{legacy, launcher} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
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
	// The one launcher now starts the renamed app, so the menu lists one.
	entry, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Name=Cloudfield\n", "Exec=\"" + filepath.Join(app.App, "aether-desktop") + "\"", "MimeType=x-scheme-handler/aether;x-scheme-handler/cloudfield;\n"} {
		if !strings.Contains(string(entry), want) {
			t.Errorf("launcher missing %q:\n%s", want, entry)
		}
	}
	if got, err := os.ReadFile(legacy); err != nil || string(got) != "old" {
		t.Errorf("the legacy app = %q, %v; want it left in place", got, err)
	}
}

func TestInstallDesktopDarwinPutsARenamedAppBesideTheLegacyOne(t *testing.T) {
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
	dist := t.TempDir()
	want := filepath.Join(dist, "mac-arm64", "Cloudfield.app")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := builtDesktopApp("darwin", dist); err != nil || got != want {
		t.Fatalf("builtDesktopApp = %q, %v; want %q", got, err, want)
	}
}

// startAppExe runs a copy of this test binary as the app's executable at
// exe, the way the desktop starts it, until the test ends. A copy of the
// system's sleep is no stand-in: uutils coreutils, Ubuntu's since 25.10, is
// one binary that picks the utility from argv[0] and exits at once under any
// other name.
func startAppExe(t *testing.T, exe string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperAppProcess$")
	cmd.Env = append(os.Environ(), "LOCALOPS_HELPER_APP=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
}

// TestHelperAppProcess is the app process startAppExe runs; it does nothing
// in a normal test run.
func TestHelperAppProcess(t *testing.T) {
	if os.Getenv("LOCALOPS_HELPER_APP") == "" {
		t.Skip("runs only as startAppExe's app process")
	}
	time.Sleep(time.Minute)
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

// The linux launcher file, the name Electron registers as the link handler
// (package.json desktopName, which extraMetadata sets) and the executable
// the launcher runs all come from electron-builder.yml, so a rename that
// changes package.json's name or the productName cannot split them.
func TestLinuxLauncherFollowsElectronBuilder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linux layout uses unix permissions")
	}
	raw, err := desktop.Source.ReadFile("electron-builder.yml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		ExtraMetadata map[string]string `yaml:"extraMetadata"`
		Linux         map[string]any    `yaml:"linux"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	desktopName := cfg.ExtraMetadata["desktopName"]
	executable, _ := cfg.Linux["executableName"].(string)
	if desktopName == "" || executable == "" {
		t.Fatalf("electron-builder.yml sets desktopName %q and linux.executableName %q; it must set both", desktopName, executable)
	}
	for _, legacy := range []bool{false, true} {
		if legacy {
			legacyProduct(t)
		}
		home := t.TempDir()
		t.Setenv("XDG_DATA_HOME", "")
		built := t.TempDir()
		if err := os.WriteFile(filepath.Join(built, executable), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		app, err := InstallDesktop("linux", home, DesktopFrom(built), "/opt/aether/aether", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := filepath.Base(app.Launcher); got != desktopName {
			t.Errorf("%s: launcher = %q, want desktopName %q", productName, got, desktopName)
		}
		entry, err := os.ReadFile(app.Launcher)
		if err != nil {
			t.Fatal(err)
		}
		if want := "Exec=\"" + filepath.Join(app.App, executable) + "\" "; !strings.Contains(string(entry), want) {
			t.Errorf("%s: launcher lacks %q:\n%s", productName, want, entry)
		}
	}
}

func TestDesktopAppExePerOS(t *testing.T) {
	for _, tc := range []struct{ goos, app, want string }{
		{"darwin", "/Applications/Cloudfield.app", "/Applications/Cloudfield.app/Contents/MacOS/Cloudfield"},
		{"darwin", "/Applications/Aether.app", "/Applications/Aether.app/Contents/MacOS/Aether"},
		{"linux", "/home/u/.local/share/cloudfield/desktop", "/home/u/.local/share/cloudfield/desktop/aether-desktop"},
		{"windows", "/l/Programs/Cloudfield Desktop", "/l/Programs/Cloudfield Desktop/aether-desktop.exe"},
	} {
		if got := DesktopAppExe(tc.goos, filepath.FromSlash(tc.app)); got != filepath.FromSlash(tc.want) {
			t.Errorf("%s %s = %q, want %q", tc.goos, tc.app, got, tc.want)
		}
	}
}

// The Windows helper starts the app the swap put in place: beside a legacy
// app, the renamed one.
func TestSwappedInAppIsTheRenamedAppBesideTheLegacyOne(t *testing.T) {
	programs := filepath.Join(t.TempDir(), "Programs")
	renamed := filepath.Join(programs, "Cloudfield Desktop")
	for _, current := range []string{filepath.Join(programs, "Aether Desktop"), renamed} {
		if got := swappedInApp(current); got != renamed {
			t.Errorf("swappedInApp(%q) = %q, want %q", current, got, renamed)
		}
	}
}
