package localops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// DesktopApp is an installed desktop shell.
type DesktopApp struct {
	// App is the installed application: an unpacked directory on linux and
	// windows, the .app bundle on darwin.
	App string
	// Launcher is what the desktop environment lists: the .desktop entry
	// on linux, the Start Menu shortcut on windows, the bundle itself on
	// darwin (Finder and Spotlight index the Applications folders directly).
	Launcher string
	// Superseded is where an earlier install of the same app may sit when
	// the layout has more than one candidate location (the other
	// Applications folder on darwin); empty otherwise. InstallDesktop
	// removes it once the new app is in place so the desktop lists one
	// Aether.
	Superseded string
}

// macSystemApplications is the machine-wide Applications folder, the one
// Finder's sidebar opens and a new user means by "my Applications folder".
// ~/Applications is hidden in Finder by default, so an app there looks
// missing even though Spotlight finds it. Administrators can write here
// without sudo; anyone else falls back to ~/Applications. A variable so
// tests can point it at a temporary directory.
var macSystemApplications = "/Applications"

// desktopIcon is the icon copied beside the unpacked linux app; the
// .desktop entry points at it by absolute path so no icon theme cache
// needs refreshing.
const desktopIcon = "aether-desktop.png"

// DesktopBuildDir is where `aether gui build` unpacks the shell sources and
// runs npm: node_modules and the electron-builder output persist there so
// a rebuild skips most of the download.
func DesktopBuildDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("localops: cache dir: %w", err)
	}
	return filepath.Join(cache, "aether", "desktop-build"), nil
}

// The phases a desktop build reports, in the order BuildDesktop and
// `aether gui build` run them. `--json` prints one line per phase and the
// gateway turns them into the dashboard's progress line, so these strings
// are a contract (docs/local-gateway.md).
const (
	PhaseUnpacking    = "unpacking"
	PhaseFetchingNode = "fetching node"
	PhaseDependencies = "installing dependencies"
	PhasePackaging    = "packaging"
	PhaseInstalling   = "installing"
	PhaseDone         = "done"
	PhaseError        = "error"
)

// BuildDesktop packages the Electron shell in src for this machine: it
// writes the sources into buildDir, stamps cliVersion into the shell's
// package.json, installs the npm dependencies, and runs electron-builder's
// unpacked (--dir) target. npm and electron-builder output stream to stdout
// and stderr. phase, when non-nil, is called with each Phase* constant as
// that step starts. It returns the unpacked app: a directory on linux and
// windows, the .app bundle on darwin.
func BuildDesktop(ctx context.Context, src fs.FS, buildDir, cliVersion string, stdout, stderr io.Writer, phase func(string)) (string, error) {
	if phase == nil {
		phase = func(string) {}
	}
	phase(PhaseUnpacking)
	if err := writeTree(buildDir, src); err != nil {
		return "", fmt.Errorf("localops: write shell sources: %w", err)
	}
	if err := stampShellVersion(buildDir, cliVersion); err != nil {
		return "", err
	}
	// Stale output from an earlier build must not be mistaken for this one.
	dist := filepath.Join(buildDir, "dist")
	if err := os.RemoveAll(dist); err != nil {
		return "", fmt.Errorf("localops: clear %s: %w", dist, err)
	}

	phase(PhaseFetchingNode)
	nodeRoot, err := nodeCacheDir()
	if err != nil {
		return "", err
	}
	node, err := ensureNode(ctx, nodeRoot, stdout)
	if err != nil {
		return "", err
	}

	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = buildDir
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		// A local build never has a certificate to find; electron-builder.yml
		// signs ad hoc instead. Skipping discovery keeps macOS builds quiet.
		// npm's electron postinstall would download the runtime zip once
		// more than electron-builder does for itself, so skip it.
		cmd.Env = append(cmd.Environ(), "CSC_IDENTITY_AUTO_DISCOVERY=false", "ELECTRON_SKIP_BINARY_DOWNLOAD=1")
		// npm and npx are scripts that look up node on PATH, and
		// electron-builder spawns node again, so a downloaded Node has to
		// lead this build's PATH. Only these two commands see it: nothing
		// on the machine, and no shell profile, is changed.
		if node.pathDir != "" {
			cmd.Env = append(cmd.Env, "PATH="+node.pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("localops: %s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
		}
		return nil
	}
	phase(PhaseDependencies)
	if err := run(node.npm, "install", "--no-audit", "--no-fund"); err != nil {
		return "", err
	}
	phase(PhasePackaging)
	if err := run(node.npx, "electron-builder", "--dir", "--publish", "never"); err != nil {
		return "", err
	}
	return builtDesktopApp(runtime.GOOS, dist)
}

// builtDesktopApp finds the one unpacked app electron-builder wrote under
// dist. The directory carries an arch suffix only when the arch is not the
// platform default, hence the globs.
func builtDesktopApp(goos, dist string) (string, error) {
	var pattern string
	switch goos {
	case "linux":
		pattern = "linux*-unpacked"
	case "windows":
		pattern = "win*-unpacked"
	case "darwin":
		pattern = filepath.Join("mac*", "Aether.app")
	default:
		return "", fmt.Errorf("localops: no desktop app target for %s", goos)
	}
	matches, err := filepath.Glob(filepath.Join(dist, pattern))
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("localops: expected one %s under %s, found %d", pattern, dist, len(matches))
	}
	return matches[0], nil
}

// writeTree copies src into dir, overwriting files that already exist.
// os.CopyFS refuses to overwrite, and the build directory keeps the
// previous run's sources.
func writeTree(dir string, src fs.FS) error {
	return fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(src, name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// shellSemver matches the versions electron-builder accepts in
// package.json. A release tag is "v1.2.3"; a local build reports "dev".
var shellSemver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// stampShellVersion records which CLI built this shell in the unpacked
// package.json, which main.js hands to the renderer. The dashboard compares
// it with the CLI serving the gateway and asks for `aether gui build` once
// the two have drifted apart. A version electron-builder would reject - a
// dev build's "dev" - leaves the manifest's own 0.1.0 in place, so a shell
// built by a dev CLI reads as stale against any release, which it is.
func stampShellVersion(buildDir, cliVersion string) error {
	semver := strings.TrimPrefix(cliVersion, "v")
	if !shellSemver.MatchString(semver) {
		return nil
	}
	path := filepath.Join(buildDir, "package.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("localops: read %s: %w", path, err)
	}
	// RawMessage values keep every field the manifest carries verbatim;
	// only the key order changes, which nothing reads.
	var manifest map[string]json.RawMessage
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("localops: parse %s: %w", path, err)
	}
	stamped, err := json.Marshal(semver)
	if err != nil {
		return err
	}
	manifest["version"] = stamped
	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("localops: write %s: %w", path, err)
	}
	return nil
}

// Prefixes for the two directories the install swap uses, both beside the
// installed app so a rename never crosses a filesystem, both dot-prefixed
// so neither shows up in an Applications listing.
const (
	installStagingPrefix = ".aether-staging-"
	installOldPrefix     = ".aether-old-"
)

// InstallDesktop copies the unpacked app at built into the application
// directory desktopLayout picks for goos and registers a launcher so the
// desktop environment lists it. home is the user's home directory; icon is
// the PNG the linux launcher shows.
//
// The new app is staged beside the target and swapped in by rename: the app
// being replaced is often the one the user is looking at, and deleting a
// running Electron's own files takes that window down with it. An earlier
// install at app.Superseded is removed after the new app is in place, so a
// failed copy never costs the last working install.
func InstallDesktop(goos, home, built string, icon []byte) (DesktopApp, error) {
	app, err := desktopLayout(goos, home)
	if err != nil {
		return DesktopApp{}, err
	}
	parent := filepath.Dir(app.App)
	if err = os.MkdirAll(parent, 0o755); err != nil {
		return DesktopApp{}, err
	}
	// Leftovers from an earlier swap whose removal the running app blocked.
	// It may have exited since; if it has not, the next install tries again.
	sweepInstallLeftovers(parent)

	staging, err := os.MkdirTemp(parent, installStagingPrefix+"*")
	if err != nil {
		return DesktopApp{}, fmt.Errorf("localops: stage the app beside %s: %w", app.App, err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	staged := filepath.Join(staging, filepath.Base(app.App))
	if err := os.CopyFS(staged, os.DirFS(built)); err != nil {
		return DesktopApp{}, fmt.Errorf("localops: copy %s to %s: %w", built, staged, err)
	}
	// Everything that belongs inside the app goes in before the swap, so
	// the directory the rename publishes is already complete.
	if goos == "linux" {
		if err := os.WriteFile(filepath.Join(staged, desktopIcon), icon, 0o644); err != nil {
			return DesktopApp{}, err
		}
	}
	if err := swapInstalled(goos, staged, app.App); err != nil {
		return DesktopApp{}, err
	}
	switch goos {
	case "linux":
		if err := os.MkdirAll(filepath.Dir(app.Launcher), 0o755); err != nil {
			return DesktopApp{}, err
		}
		if err := os.WriteFile(app.Launcher, []byte(desktopEntry(app.App)), 0o644); err != nil {
			return DesktopApp{}, err
		}
		// Menus pick the entry up on their own; the database only speeds
		// up the x-scheme-handler lookup for aether:// links. Its absence
		// or failure is not an install failure.
		if updater, err := exec.LookPath("update-desktop-database"); err == nil {
			_ = exec.Command(updater, filepath.Dir(app.Launcher)).Run()
		}
	case "windows":
		if err := os.MkdirAll(filepath.Dir(app.Launcher), 0o755); err != nil {
			return DesktopApp{}, err
		}
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", shortcutScript)
		cmd.Env = append(cmd.Environ(), "AETHER_LNK="+app.Launcher, "AETHER_EXE="+filepath.Join(app.App, "Aether.exe"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return DesktopApp{}, fmt.Errorf("localops: create Start Menu shortcut: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if app.Superseded != "" {
		// RemoveAll succeeds on a missing path, so any error here means
		// the older copy is still listed beside the new one, or worse
		// half-deleted: it is reported, not swallowed.
		if err := os.RemoveAll(app.Superseded); err != nil {
			return DesktopApp{}, fmt.Errorf("localops: %s is installed, but the earlier %s could not be removed%s: %w", app.App, app.Superseded, removeHint(goos, app.Superseded, err), err)
		}
	}
	return app, nil
}

// swapInstalled publishes staged at target with two renames: the installed
// app moves aside, the new one takes its place, and only then is the old
// copy deleted. A rename leaves a running app's open files intact, so the
// window the user is looking at keeps working until they restart it.
//
// A removal that fails - Windows holds a running program's files open -
// leaves a dot-prefixed directory beside the app that the next install
// sweeps up. A failed second rename puts the working install back.
func swapInstalled(goos, staged, target string) error {
	parent := filepath.Dir(target)
	old := ""
	if _, err := os.Lstat(target); err == nil {
		aside, err := os.MkdirTemp(parent, installOldPrefix+"*")
		if err != nil {
			return fmt.Errorf("localops: make room beside %s: %w", target, err)
		}
		old = filepath.Join(aside, filepath.Base(target))
		if err := os.Rename(target, old); err != nil {
			_ = os.RemoveAll(aside)
			return fmt.Errorf("localops: move the installed %s aside%s: %w", target, removeHint(goos, target, err), err)
		}
	}
	if err := os.Rename(staged, target); err != nil {
		if old != "" {
			_ = os.Rename(old, target)
			_ = os.RemoveAll(filepath.Dir(old))
		}
		return fmt.Errorf("localops: install %s: %w", target, err)
	}
	if old != "" {
		_ = os.RemoveAll(filepath.Dir(old))
	}
	return nil
}

// sweepInstallLeftovers deletes the staging and set-aside directories an
// earlier install could not remove. Best effort by design: this is
// housekeeping, and failing an install over it would be worse than the
// megabytes it leaves behind.
func sweepInstallLeftovers(parent string) {
	for _, prefix := range []string{installStagingPrefix, installOldPrefix} {
		matches, err := filepath.Glob(filepath.Join(parent, prefix+"*"))
		if err != nil {
			continue
		}
		for _, dir := range matches {
			_ = os.RemoveAll(dir)
		}
	}
}

// removeHint explains a failed removal of an installed app in the terms
// the user can act on. Windows holds the files of a running program open,
// so there the fix is closing the window; on darwin and linux a running
// app unlinks fine, and a refusal means the bundle belongs to another user.
func removeHint(goos, path string, err error) string {
	switch {
	case goos == "windows":
		return " (is the Aether window still open?)"
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf(" (it belongs to another user; delete it with: sudo rm -rf %q)", path)
	default:
		return ""
	}
}

// DesktopFindsCLI reports whether the shell will locate the aether binary
// at launch, mirroring desktop/main.js: AETHER_BIN, then PATH, then the
// install script's default locations. The shell runs with the desktop
// session's PATH, not this terminal's, so a binary found only through a
// PATH entry outside the defaults is reported in shellOnly: it works from
// here but may not from the application menu.
func DesktopFindsCLI(home string) (found bool, shellOnly string) {
	if explicit := os.Getenv("AETHER_BIN"); explicit != "" {
		_, err := exec.LookPath(explicit)
		return err == nil, ""
	}
	name := "aether"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	defaults := []string{"/usr/local/bin", filepath.Join(home, ".local", "bin")}
	if path, err := exec.LookPath("aether"); err == nil {
		dir := filepath.Dir(path)
		for _, d := range append(defaults, "/usr/bin", "/opt/homebrew/bin") {
			if dir == d {
				return true, ""
			}
		}
		return true, path
	}
	for _, dir := range defaults {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
			return true, ""
		}
	}
	return false, ""
}

// desktopLayout is where the app and its launcher live for goos. Linux and
// windows use the per-user locations their desktops index without
// administrator rights. darwin prefers the machine-wide Applications
// folder, which takes a filesystem probe, and names the per-user folder as
// Superseded (or the reverse when the probe fails) so an older copy there
// is cleaned up.
func desktopLayout(goos, home string) (DesktopApp, error) {
	switch goos {
	case "linux":
		// A relative XDG_DATA_HOME is invalid per the spec and treated as
		// unset; honoring it would install into the working directory.
		data := os.Getenv("XDG_DATA_HOME")
		if !filepath.IsAbs(data) {
			data = filepath.Join(home, ".local", "share")
		}
		return DesktopApp{
			App:      filepath.Join(data, "aether", "desktop"),
			Launcher: filepath.Join(data, "applications", "aether-desktop.desktop"),
		}, nil
	case "darwin":
		system := filepath.Join(macSystemApplications, "Aether.app")
		user := filepath.Join(home, "Applications", "Aether.app")
		app, other := system, user
		if !writableDir(macSystemApplications) {
			app, other = user, system
		}
		if other == app {
			other = ""
		}
		return DesktopApp{App: app, Launcher: app, Superseded: other}, nil
	case "windows":
		local, roaming := os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")
		if !filepath.IsAbs(local) || !filepath.IsAbs(roaming) {
			return DesktopApp{}, errors.New("localops: LOCALAPPDATA and APPDATA must be absolute paths")
		}
		return DesktopApp{
			App:      filepath.Join(local, "Programs", "Aether"),
			Launcher: filepath.Join(roaming, "Microsoft", "Windows", "Start Menu", "Programs", "Aether.lnk"),
		}, nil
	default:
		return DesktopApp{}, fmt.Errorf("localops: no desktop app target for %s", goos)
	}
}

// writableDir reports whether this user can create entries in dir. It
// probes with a temporary file rather than reading permission bits, which
// miss ACLs and group membership; the install writes there anyway.
func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".aether-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// desktopEntry renders the freedesktop launcher for the unpacked app in
// dir. %U hands aether:// links to the app. --no-sandbox matches
// electron-builder's own AppImage default: an unpacked Electron cannot use
// its SUID helper without root, and Ubuntu 24.04+ denies the namespace
// sandbox to unconfined binaries, so without the flag the app refuses to
// start on the most common desktop. The renderer still runs with context
// isolation and no Node access, locked to the loopback gateway.
func desktopEntry(dir string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Aether\n" +
		"Comment=Aether dashboard in its own window\n" +
		"Exec=" + desktopExecQuote(filepath.Join(dir, "aether-desktop")) + " --no-sandbox %U\n" +
		"Icon=" + filepath.Join(dir, desktopIcon) + "\n" +
		"Terminal=false\n" +
		"Categories=Development;\n" +
		"MimeType=x-scheme-handler/aether;\n" +
		"StartupWMClass=Aether\n"
}

// desktopExecQuote quotes one Exec argument per the Desktop Entry spec:
// double quotes with the reserved characters backslash-escaped, and a
// literal % doubled so launchers do not read it as a field code.
func desktopExecQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '`', '$', '\\':
			b.WriteByte('\\')
		case '%':
			b.WriteByte('%')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// shortcutScript is the PowerShell that writes the Start Menu .lnk named
// by $env:AETHER_LNK pointing at $env:AETHER_EXE. The paths travel in the
// environment, never in the script text, so no quoting rule (PowerShell
// also treats typographic quotes as delimiters) can break on a user name.
const shortcutScript = "$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:AETHER_LNK); " +
	"$s.TargetPath = $env:AETHER_EXE; " +
	"$s.WorkingDirectory = (Split-Path -Parent $env:AETHER_EXE); " +
	"$s.Save()"
