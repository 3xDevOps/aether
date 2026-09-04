package localops

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// RealUser is the account a command acts for. Under sudo that is the
// invoking user, not root: the desktop app, the build directory and the
// npm and Electron caches all belong in that account's home, owned by that
// account, or the next build as that user cannot read them.
type RealUser struct {
	// Name is the account name, set only when sudo is in play.
	Name string
	// Home is that account's home directory.
	Home string
	// ViaSudo marks a root process running for another account, which has
	// to drop back to it before writing that account's files.
	ViaSudo bool
}

// LookupRealUser resolves the account this process acts for from SUDO_USER
// and the process's own home directory.
func LookupRealUser() (RealUser, error) {
	return realUser(os.Getenv("SUDO_USER"), user.Lookup, os.UserHomeDir)
}

// realUser is LookupRealUser with its two lookups injected, so a test can
// name an account that does not exist on the machine running it.
func realUser(sudoUser string, lookup func(string) (*user.User, error), homeDir func() (string, error)) (RealUser, error) {
	// `sudo -u root` (or a root login shell) leaves nobody to drop back to.
	if sudoUser != "" && sudoUser != "root" {
		u, err := lookup(sudoUser)
		if err != nil {
			return RealUser{}, fmt.Errorf("localops: look up SUDO_USER %q: %w", sudoUser, err)
		}
		if u.HomeDir == "" {
			return RealUser{}, fmt.Errorf("localops: SUDO_USER %q has no home directory", sudoUser)
		}
		return RealUser{Name: sudoUser, Home: u.HomeDir, ViaSudo: true}, nil
	}
	home, err := homeDir()
	if err != nil {
		return RealUser{}, fmt.Errorf("localops: home directory: %w", err)
	}
	return RealUser{Home: home}, nil
}

// InstalledDesktopApp reports the desktop app installed for ru, if any.
// It looks at every location desktopLayout can pick rather than only the
// one it would pick now: on darwin that choice depends on whether the
// account can write /Applications, and under sudo the environment carries
// root's XDG_DATA_HOME, not the user's.
func InstalledDesktopApp(goos string, ru RealUser) (string, bool) {
	xdg := os.Getenv("XDG_DATA_HOME")
	if ru.ViaSudo {
		xdg = ""
	}
	for _, candidate := range desktopAppPaths(goos, ru.Home, xdg, os.Getenv("LOCALAPPDATA")) {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// desktopAppPaths lists the candidate install locations for goos, most
// likely first. It mirrors desktopLayout; an empty list means this OS has
// no desktop app at all.
//
// The unix branches join with path and test for a leading slash
// themselves, because path/filepath answers for the machine running the
// code and this function is asked about a named OS. In production goos is
// always runtime.GOOS and the two agree byte for byte, so nothing about an
// install changes; what changes is that the linux and darwin layouts are
// still the linux and darwin layouts when a Windows runner asks. The
// windows branch keeps filepath, which is that OS's own rule on the only
// machine where those paths can exist.
func desktopAppPaths(goos, home, xdgData, localAppData string) []string {
	switch goos {
	case "linux":
		var paths []string
		// A relative XDG_DATA_HOME is invalid per the spec and treated as
		// unset, the same way desktopLayout treats it.
		if strings.HasPrefix(xdgData, "/") {
			paths = append(paths, path.Join(xdgData, "aether", "desktop"))
		}
		return append(paths, path.Join(home, ".local", "share", "aether", "desktop"))
	case "darwin":
		return []string{
			path.Join(macSystemApplications, "Aether.app"),
			path.Join(home, "Applications", "Aether.app"),
		}
	case "windows":
		if !filepath.IsAbs(localAppData) {
			return nil
		}
		return []string{filepath.Join(localAppData, "Programs", "Aether")}
	default:
		return nil
	}
}

// RebuildAppArgv is the command that rebuilds the installed desktop app.
// bin is the *updated* aether binary, not the running one: the shell
// sources and the dashboard both ship inside the binary, so building with
// the process that is about to be replaced would install the old app
// again. Under sudo it drops back to the real user with -H, so npm, the
// Electron cache and the app itself land in that account's home owned by
// that account. jsonOut asks for the machine-readable phase lines the
// gateway parses; a terminal wants the build's own output instead.
func RebuildAppArgv(bin string, ru RealUser, jsonOut bool) []string {
	argv := []string{bin, "gui", "build"}
	if jsonOut {
		argv = append(argv, "--json")
	}
	if ru.ViaSudo {
		return append([]string{"sudo", "-u", ru.Name, "-H"}, argv...)
	}
	return argv
}

// desktopBuildErrorFile records why the last desktop rebuild failed. The
// gateway that started the build exits so the shell can respawn it, so the
// failure has to outlive the process that saw it: the next gateway reads
// this file and the dashboard shows the error.
const desktopBuildErrorFile = "last-error.txt"

// DesktopBuildErrorPath is where that record lives, inside the build
// directory so removing the build directory removes it too.
func DesktopBuildErrorPath() (string, error) {
	dir, err := DesktopBuildDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, desktopBuildErrorFile), nil
}

// RecordDesktopBuildError persists msg for the next gateway to report.
func RecordDesktopBuildError(msg string) error {
	path, err := DesktopBuildErrorPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("localops: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(msg)+"\n"), 0o644); err != nil {
		return fmt.Errorf("localops: write %s: %w", path, err)
	}
	return nil
}

// LastDesktopBuildError reads that record, empty when the last build
// worked or none has run. A missing or unreadable file is the same answer:
// there is no failure to report.
func LastDesktopBuildError() string {
	path, err := DesktopBuildErrorPath()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// ClearDesktopBuildError drops the record after a build that worked.
func ClearDesktopBuildError() {
	if path, err := DesktopBuildErrorPath(); err == nil {
		_ = os.Remove(path)
	}
}

// DesktopAppRunning reports whether the app installed at path has a live
// process, which decides whether the user has to restart it to see the
// build that just replaced it. It answers false on Windows, where `aether
// update` refuses before it ever gets here.
func DesktopAppRunning(goos, app string) bool {
	switch goos {
	case "linux":
		return procExeUnder(app)
	case "darwin":
		// The bundle's executable, the argv[0] LaunchServices starts it with.
		return pgrepMatches(filepath.Join(app, "Contents", "MacOS", "Aether"))
	default:
		return false
	}
}

// procExeUnder reports whether any process in /proc runs a binary inside
// dir. Reading another account's /proc/<pid>/exe is denied, which is the
// right answer anyway: that app is not the one this user has to restart.
func procExeUnder(dir string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	prefix := dir + string(filepath.Separator)
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil {
			continue
		}
		if strings.HasPrefix(exe, prefix) {
			return true
		}
	}
	return false
}

// pgrepMatches reports whether pgrep finds a process whose command line
// matches pattern. darwin has no /proc, and pgrep ships with the system.
// The pattern is an install path, so the regex characters it can contain
// (a dot in Aether.app) only ever widen the match to itself.
func pgrepMatches(pattern string) bool {
	// pgrep prints the pids it found; this caller wants the exit status.
	_, err := exec.Command("pgrep", "-f", pattern).Output()
	return err == nil
}
