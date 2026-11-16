package localops

import (
	"errors"
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
	apps := InstalledDesktopApps(goos, ru)
	if len(apps) == 0 {
		return "", false
	}
	return apps[0], true
}

// InstalledDesktopApps is every desktop app installed for ru, most likely
// first. A machine can hold an app under the legacy product name beside one
// under this build's.
func InstalledDesktopApps(goos string, ru RealUser) []string {
	var apps []string
	for _, name := range productNames() {
		for _, candidate := range userAppPaths(goos, ru, name) {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				apps = append(apps, candidate)
			}
		}
	}
	return apps
}

// userAppPaths is appPathsNamed for ru's account.
func userAppPaths(goos string, ru RealUser, name string) []string {
	xdg := os.Getenv("XDG_DATA_HOME")
	if ru.ViaSudo {
		xdg = ""
	}
	return appPathsNamed(goos, name, ru.Home, xdg, os.Getenv("LOCALAPPDATA"))
}

// appPathsNamed lists the candidate install locations of the app named
// name for goos, most likely first. It mirrors desktopLayout; an empty list
// means this OS has no desktop app at all.
//
// The unix branches join with path and test for a leading slash
// themselves, because path/filepath answers for the machine running the
// code and this function is asked about a named OS. In production goos is
// always runtime.GOOS and the two agree byte for byte, so nothing about an
// install changes; what changes is that the linux and darwin layouts are
// still the linux and darwin layouts when a Windows runner asks. The
// windows branch keeps filepath, which is that OS's own rule on the only
// machine where those paths can exist.
func appPathsNamed(goos, name, home, xdgData, localAppData string) []string {
	switch goos {
	case "linux":
		var paths []string
		// A relative XDG_DATA_HOME is invalid per the spec and treated as
		// unset, the same way desktopLayout treats it.
		if strings.HasPrefix(xdgData, "/") {
			paths = append(paths, path.Join(xdgData, linuxAppDir(name), "desktop"))
		}
		return append(paths, path.Join(home, ".local", "share", linuxAppDir(name), "desktop"))
	case "darwin":
		return []string{
			path.Join(macSystemApplications, darwinBundle(name)),
			path.Join(home, "Applications", darwinBundle(name)),
		}
	case "windows":
		if filepath.IsAbs(localAppData) {
			return []string{filepath.Join(localAppData, "Programs", windowsAppDir(name))}
		}
	}
	return nil
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
		return pgrepMatches(DesktopAppExe(goos, app))
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

// ShellExeEnv is set by the desktop shell to its own executable when it
// starts `aether gui`. Shells built before it existed leave it unset.
const ShellExeEnv = "AETHER_SHELL_EXE"

// RetireLegacyDesktop removes the app installed under the legacy product
// name once an app under this build's name has started: shellExe must be
// the executable of an install of this build's name, so a development
// `electron .`, a legacy shell or an unset shellExe removes nothing, and so
// does a process acting for another account under sudo. A legacy location
// is removed only when it is a real directory (not a symlink) holding the
// app's executable, it is not also a location of this build's name, linux
// and darwin do not see it running, and it neither holds nor sits inside
// the running app. The data directory lives elsewhere and is never
// touched. On windows the legacy Start Menu shortcut goes with the app; on
// linux the renamed app's launcher already replaced the legacy one.
func RetireLegacyDesktop(goos, shellExe string) error {
	return retireLegacyDesktop(goos, LookupRealUser, shellExe)
}

func retireLegacyDesktop(goos string, lookup func() (RealUser, error), shellExe string) error {
	if productName == legacyProductName || shellExe == "" {
		return nil
	}
	ru, err := lookup()
	if err != nil || ru.ViaSudo {
		return err
	}
	shell, err := filepath.EvalSymlinks(shellExe)
	if err != nil {
		return nil
	}
	current := userAppPaths(goos, ru, productName)
	shellApp := ""
	for _, app := range current {
		if sameFile(DesktopAppExe(goos, app), shell) {
			shellApp, _ = filepath.EvalSymlinks(app)
			break
		}
	}
	if shellApp == "" {
		return nil
	}
	legacy := userAppPaths(goos, ru, legacyProductName)
	var errs []error
	var asides []string
	for _, app := range legacy {
		asides = append(asides, setAsideCopies(app)...)
		resolved, ok := legacyInstall(goos, app, current, shellApp)
		if !ok {
			continue
		}
		aside, err := moveAside(goos, resolved)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		asides = append(asides, aside)
	}
	if goos == "windows" && len(asides) > 0 && len(errs) == 0 && !anyExists(legacy) {
		// The set-aside copies stay until the shortcut is gone, so the next
		// start finds them and tries the shortcut again.
		if err := removeLegacyShortcut(); err != nil {
			return err
		}
	}
	for _, aside := range asides {
		if err := os.RemoveAll(aside); err != nil {
			errs = append(errs, fmt.Errorf("localops: remove the old app, moved to %s%s: %w", aside, removeHint(goos, aside, err), err))
		}
	}
	return errors.Join(errs...)
}

// legacyInstall reports whether app is an old install to remove, and its
// resolved path, which is the one removed.
func legacyInstall(goos, app string, current []string, shellApp string) (string, bool) {
	info, err := os.Lstat(app)
	if err != nil || !info.Mode().IsDir() {
		return "", false
	}
	exe, err := os.Lstat(DesktopAppExe(goos, app))
	if err != nil || !exe.Mode().IsRegular() {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(app)
	if err != nil || sameFile(resolved, shellApp) || within(goos, resolved, shellApp) || within(goos, shellApp, resolved) {
		return "", false
	}
	for _, c := range current {
		if c == app || sameFile(c, resolved) {
			return "", false
		}
	}
	// /proc reports the resolved executable, pgrep the path it was started by.
	if DesktopAppRunning(goos, app) || DesktopAppRunning(goos, resolved) {
		return "", false
	}
	return resolved, true
}

// moveAside renames app into a new set-aside directory beside it and
// returns that directory, which the caller deletes. Windows refuses to
// rename a directory whose files a running program holds open, and
// DesktopAppRunning cannot tell there, so a running app is left whole
// rather than half deleted.
func moveAside(goos, app string) (string, error) {
	aside, err := os.MkdirTemp(filepath.Dir(app), installOldPrefix+"*")
	if err == nil {
		err = os.Rename(app, filepath.Join(aside, filepath.Base(app)))
		if err != nil {
			_ = os.Remove(aside)
		}
	}
	if err != nil {
		return "", fmt.Errorf("localops: remove the old app %s%s: %w", app, removeHint(goos, app, err), err)
	}
	return aside, nil
}

// setAsideCopies is the set-aside directories of app that an earlier start
// could not delete. It matches only those holding the legacy app's own
// directory name: on windows the parent is shared with the renamed app,
// whose install swap sets its own copy aside under the same prefix.
func setAsideCopies(app string) []string {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(app), installOldPrefix+"*", filepath.Base(app)))
	asides := make([]string, len(matches))
	for i, old := range matches {
		asides[i] = filepath.Dir(old)
	}
	return asides
}

func removeLegacyShortcut() error {
	programs, err := resolveWindowsPrograms()
	if err != nil {
		return fmt.Errorf("localops: resolve Start Menu Programs folder: %w", err)
	}
	link := filepath.Join(programs, legacyProductName+".lnk")
	if info, err := os.Lstat(link); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if err := os.Remove(link); err != nil {
		return fmt.Errorf("localops: remove the old Start Menu shortcut %s: %w", link, err)
	}
	return nil
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// within reports whether p is inside dir. darwin and windows file systems
// ignore case by default.
func within(goos, dir, p string) bool {
	if goos != "linux" {
		dir, p = strings.ToLower(dir), strings.ToLower(p)
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func anyExists(paths []string) bool {
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}
