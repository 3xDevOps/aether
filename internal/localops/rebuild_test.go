package localops

import (
	"errors"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRealUserPrefersTheSudoUser(t *testing.T) {
	got, err := realUser("alice",
		func(name string) (*user.User, error) {
			if name != "alice" {
				t.Fatalf("looked up %q, want alice", name)
			}
			return &user.User{Username: "alice", HomeDir: "/home/alice"}, nil
		},
		func() (string, error) { return "/root", nil })
	if err != nil {
		t.Fatal(err)
	}
	want := RealUser{Name: "alice", Home: "/home/alice", ViaSudo: true}
	if got != want {
		t.Fatalf("realUser = %+v, want %+v", got, want)
	}
}

// A root login shell and `sudo -u root` both leave nobody to drop back to.
func TestRealUserIgnoresSudoUserRoot(t *testing.T) {
	got, err := realUser("root",
		func(string) (*user.User, error) { t.Fatal("root needs no lookup"); return nil, nil },
		func() (string, error) { return "/root", nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.ViaSudo || got.Home != "/root" {
		t.Fatalf("realUser = %+v, want the process's own home", got)
	}
}

// Building as the wrong user would put the app and its caches in the wrong
// home, owned by the wrong account, so a lookup failure stops the rebuild.
func TestRealUserReportsAnUnknownSudoUser(t *testing.T) {
	_, err := realUser("ghost",
		func(string) (*user.User, error) { return nil, errors.New("unknown user") },
		func() (string, error) { return "/root", nil })
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want it to name the account", err)
	}
}

// The unix layouts are the same wherever the question is asked, so these
// are written as the literal paths a linux or macOS box would carry. They
// held backslashes and dropped the XDG candidate when this test ran on the
// Windows runner, which is the whole reason the function stopped joining
// unix paths with the host's own separator.
func TestDesktopAppPathsPerOS(t *testing.T) {
	linux := desktopAppPaths("linux", "/home/u", "/data/xdg", "")
	want := []string{"/data/xdg/aether/desktop", "/home/u/.local/share/aether/desktop"}
	if len(linux) != 2 || linux[0] != want[0] || linux[1] != want[1] {
		t.Fatalf("linux = %v, want %v", linux, want)
	}
	// A relative XDG_DATA_HOME is invalid per the spec; only the default
	// location is left.
	if got := desktopAppPaths("linux", "/home/u", "relative", ""); len(got) != 1 {
		t.Fatalf("linux with a relative XDG_DATA_HOME = %v, want one path", got)
	}
	// Both Applications folders: which one desktopLayout picked depends on
	// whether the account could write /Applications at install time.
	mac := desktopAppPaths("darwin", "/Users/u", "", "")
	if len(mac) != 2 || mac[0] != "/Applications/Aether.app" || mac[1] != "/Users/u/Applications/Aether.app" {
		t.Fatalf("darwin = %v", mac)
	}
	// The windows branch reads LOCALAPPDATA with the host's own filepath,
	// so this case is written with a path the host calls absolute: a real
	// C:\ one on the Windows runner that actually ships this client, and a
	// stand-in elsewhere. What is asserted either way is the Programs\Aether
	// tail and that a relative LOCALAPPDATA leaves nowhere to look.
	local := filepath.Join(t.TempDir(), "AppData", "Local")
	win := desktopAppPaths("windows", filepath.Join("home", "u"), "", local)
	if len(win) != 1 || win[0] != filepath.Join(local, "Programs", "Aether") {
		t.Fatalf("windows = %v", win)
	}
	if got := desktopAppPaths("windows", "/home/u", "", "AppData"); got != nil {
		t.Fatalf("windows with a relative LOCALAPPDATA = %v", got)
	}
	if got := desktopAppPaths("plan9", "/home/u", "", ""); got != nil {
		t.Fatalf("plan9 = %v, want no desktop app", got)
	}
}

func TestInstalledDesktopAppFindsTheDefaultLinuxLocation(t *testing.T) {
	// The linux layout is slash-joined whatever the host is, so a home in
	// that form keeps the whole path uniform - which matters only on
	// Windows, where the runner's temp directory is backslashed and this
	// test asks for the linux layout anyway.
	home := filepath.ToSlash(t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	app := path.Join(home, ".local", "share", "aether", "desktop")
	if _, ok := InstalledDesktopApp("linux", RealUser{Home: home}); ok {
		t.Fatal("reported an app before one was installed")
	}
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := InstalledDesktopApp("linux", RealUser{Home: home})
	if !ok || got != app {
		t.Fatalf("InstalledDesktopApp = %q, %v; want %q", got, ok, app)
	}
}

// Under sudo the environment carries root's XDG_DATA_HOME, not the user's,
// so detection has to ignore it and use the account's own home.
func TestInstalledDesktopAppIgnoresXDGUnderSudo(t *testing.T) {
	home := filepath.ToSlash(t.TempDir())
	rootData := filepath.ToSlash(t.TempDir())
	t.Setenv("XDG_DATA_HOME", rootData)
	if err := os.MkdirAll(path.Join(rootData, "aether", "desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := path.Join(home, ".local", "share", "aether", "desktop")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := InstalledDesktopApp("linux", RealUser{Name: "alice", Home: home, ViaSudo: true})
	if !ok || got != app {
		t.Fatalf("InstalledDesktopApp = %q, %v; want the user's own %q", got, ok, app)
	}
}

func TestRebuildAppArgv(t *testing.T) {
	plain := RebuildAppArgv("/usr/local/bin/aether", RealUser{Home: "/home/u"}, false)
	if strings.Join(plain, " ") != "/usr/local/bin/aether gui build" {
		t.Fatalf("argv = %v", plain)
	}
	// The gateway parses the phase lines; a terminal wants the build output.
	withJSON := RebuildAppArgv("/usr/local/bin/aether", RealUser{Home: "/home/u"}, true)
	if strings.Join(withJSON, " ") != "/usr/local/bin/aether gui build --json" {
		t.Fatalf("argv = %v", withJSON)
	}
	// -H so npm, the Electron cache and the app land in alice's home.
	sudo := RebuildAppArgv("/usr/local/bin/aether", RealUser{Name: "alice", Home: "/home/alice", ViaSudo: true}, true)
	if strings.Join(sudo, " ") != "sudo -u alice -H /usr/local/bin/aether gui build --json" {
		t.Fatalf("argv = %v", sudo)
	}
}

func TestDesktopBuildErrorSurvivesTheProcessThatSawIt(t *testing.T) {
	cacheHome(t)
	if got := LastDesktopBuildError(); got != "" {
		t.Fatalf("LastDesktopBuildError = %q before any build", got)
	}
	if err := RecordDesktopBuildError("localops: npm install: exit status 1\n"); err != nil {
		t.Fatal(err)
	}
	if got := LastDesktopBuildError(); got != "localops: npm install: exit status 1" {
		t.Fatalf("LastDesktopBuildError = %q", got)
	}
	ClearDesktopBuildError()
	if got := LastDesktopBuildError(); got != "" {
		t.Fatalf("LastDesktopBuildError = %q after a build that worked", got)
	}
}

// cacheHome points os.UserCacheDir at a temporary directory, so the build
// error record never touches the developer's real cache.
func cacheHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LocalAppData", dir)
	case "darwin":
		t.Setenv("HOME", dir)
	default:
		t.Setenv("XDG_CACHE_HOME", dir)
	}
}
