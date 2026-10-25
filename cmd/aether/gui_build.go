package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/3xDevOps/Aether/desktop"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/version"
)

// buildEvent is one line of `aether gui build --json`: the phase that just
// started, plus the install path on "done" and the failure on "error". The
// local gateway spawns the build that way and turns these into the
// dashboard's progress line, so the shape is a contract
// (docs/local-gateway.md).
type buildEvent struct {
	Phase string `json:"phase"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

// Downloading and done are absent: their steps already print a line.
var phaseNotes = map[string]string{
	localops.PhaseUnpacking:    "unpacking the desktop app sources",
	localops.PhaseFetchingNode: "checking for Node.js",
	localops.PhaseDependencies: "running npm install; a first build downloads packages and can take a few minutes",
	localops.PhasePackaging:    "packaging the app with electron-builder; a first build downloads Electron (about 100 MB)",
	localops.PhaseInstalling:   "installing the app",
}

// guiBuild installs the desktop app where the desktop lists applications,
// so the dashboard opens as a native window without a source checkout. On
// Windows it downloads the release's prebuilt app; elsewhere it packages
// the embedded Electron shell on this machine.
func guiBuild(args []string) error {
	return guiBuildTo(args, os.Stdout)
}

// guiBuildTo is guiBuild with its --json stream injected, so a test can
// read the phase lines without taking over the process's stdout.
func guiBuildTo(args []string, events io.Writer) error {
	fs := newFlagSet("gui build")
	buildDir := fs.String("build-dir", "", "where to download the app, or unpack the shell sources and run npm (default: the user cache directory)")
	jsonOut := fs.Bool("json", false, "print one JSON line per build phase on stdout; the build's own output goes to stderr")
	source := fs.Bool("source", false, "build the app on this machine instead of downloading the release's prebuilt Windows app")
	zipPath := fs.String("zip", "", "install the Windows desktop app from this local zip instead of downloading it; no signed manifest vouches for a local zip, so only its layout is checked")
	stage := fs.Bool("stage", false, "download or build the app into the build directory without installing it; update.apply uses this while the app still runs (Windows)")
	staged := fs.Bool("staged", false, "install the app --stage left in the build directory once the --after-exit processes have exited, then start the app; update.apply starts this detached (Windows)")
	afterExit := fs.String("after-exit", "", "with --staged: comma-separated process ids to wait for before the swap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	pids, pidErr := localops.ParseExitPIDs(*afterExit)
	if pidErr != nil {
		return pidErr
	}
	switch {
	case *stage && *staged:
		return errors.New("--stage and --staged are mutually exclusive")
	case (*stage || *staged) && *zipPath != "":
		return errors.New("--zip installs at once; it cannot be staged")
	case *staged && *source:
		return errors.New("--staged installs what --stage built; --source does not apply")
	case (*stage || *staged) && runtime.GOOS != "windows":
		return fmt.Errorf("--stage and --staged are for the Windows app swap; on %s the app is replaced while it runs", runtime.GOOS)
	case len(pids) > 0 && !*staged:
		return errors.New("--after-exit needs --staged")
	}
	if *zipPath != "" && *source {
		return errors.New("--zip and --source are mutually exclusive")
	}
	if *zipPath != "" && runtime.GOOS != "windows" {
		return fmt.Errorf("--zip installs the prebuilt Windows desktop app; on %s, run aether gui build without it", runtime.GOOS)
	}
	// With --json stdout carries the phase lines and nothing else, so the
	// build's own output and every note move to stderr.
	notes := io.Writer(os.Stdout)
	if *jsonOut {
		notes = os.Stderr
	}
	emit := func(ev buildEvent) {
		if !*jsonOut {
			if note, ok := phaseNotes[ev.Phase]; ok {
				_, _ = fmt.Fprintln(notes, note)
			}
			return
		}
		line, err := json.Marshal(ev)
		if err != nil {
			return
		}
		// One line, flushed as it happens: the gateway reads these to
		// drive a progress banner, not after the build is over.
		_, _ = fmt.Fprintln(events, string(line))
		if f, ok := events.(*os.File); ok {
			_ = f.Sync()
		}
	}
	err := buildAndInstall(*buildDir, *zipPath, *source, buildMode{stage: *stage, staged: *staged, pids: pids}, notes, emit)
	if err != nil {
		emit(buildEvent{Phase: localops.PhaseError, Error: err.Error()})
	}
	return err
}

// buildMode selects the two halves of the Windows app swap, which cannot
// rename the app's directory while it runs: stage does the downloading and
// building and installs nothing, staged installs that result after the
// processes in pids have exited.
type buildMode struct {
	stage, staged bool
	pids          []int
}

// buildAndInstall is guiBuild's work, split out so every failure reaches
// the one place that reports it as an error event.
func buildAndInstall(buildDir, zipPath string, source bool, mode buildMode, notes io.Writer, emit func(buildEvent)) error {
	if buildDir == "" {
		dir, err := localops.DesktopBuildDir()
		if err != nil {
			return err
		}
		buildDir = dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// Require an installed CLI before building: a transient go-run binary
	// may disappear from the path recorded in the desktop app.
	installDirs := "/usr/local/bin or ~/.local/bin"
	if runtime.GOOS == "windows" {
		installDirs = `%LOCALAPPDATA%\Programs\Aether`
	}
	found, _ := localops.DesktopFindsCLI(home)
	if !found {
		if explicit := os.Getenv("AETHER_BIN"); explicit != "" {
			return fmt.Errorf("aether CLI is required to build the desktop app: AETHER_BIN %q is not an installed executable; install the CLI or set AETHER_BIN to its path (see docs/install.md)", explicit)
		}
		return fmt.Errorf("aether CLI is required to build the desktop app but was not found (PATH or %s); install the CLI first (see docs/install.md)", installDirs)
	}
	cliPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the CLI for the desktop app: %w", err)
	}
	if cliPath, err = filepath.EvalSymlinks(cliPath); err != nil {
		return fmt.Errorf("resolve the CLI for the desktop app: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopSignals := cancelOnSignal(cancel)
	defer stopSignals()

	phase := func(name string) { emit(buildEvent{Phase: name}) }
	var src localops.DesktopSource
	var release func()
	if mode.staged {
		src, release, err = stagedDesktop(ctx, buildDir)
	} else {
		src, release, err = prebuiltDesktop(ctx, runtime.GOOS, version.Version, zipPath, source, buildDir, notes, phase)
	}
	defer release()
	if err != nil {
		return desktopBuildError(stopSignals(), err)
	}
	if src == nil {
		_, _ = fmt.Fprintf(notes, "building the desktop app in %s\n", buildDir)
		built, buildErr := localops.BuildDesktop(ctx, desktop.Source, buildDir, version.Version, cliPath, notes, os.Stderr, phase)
		if buildErr != nil {
			return desktopBuildError(stopSignals(), buildErr)
		}
		src = localops.DesktopFrom(built)
	}
	if ctx.Err() != nil {
		return desktopBuildError(stopSignals(), ctx.Err())
	}
	if mode.stage {
		staged := filepath.Join(buildDir, localops.StagedAppDir)
		if err = os.RemoveAll(staged); err == nil {
			err = src(staged)
		}
		if err != nil {
			return desktopBuildError(stopSignals(), err)
		}
		emit(buildEvent{Phase: localops.PhaseDone, Path: staged})
		return nil
	}
	icon, err := desktop.Source.ReadFile("build/icons/256x256.png")
	if err != nil {
		return desktopBuildError(stopSignals(), err)
	}
	if ctx.Err() != nil {
		return desktopBuildError(stopSignals(), ctx.Err())
	}
	phase(localops.PhaseInstalling)
	install := func() (localops.DesktopApp, error) {
		return localops.InstallDesktop(runtime.GOOS, home, src, cliPath, icon)
	}
	var app localops.DesktopApp
	if mode.staged {
		app, err = finishStaged(home, mode.pids, install)
	} else {
		app, err = install()
	}
	if err != nil {
		return desktopBuildError(stopSignals(), err)
	}
	if ctx.Err() != nil {
		return desktopBuildError(stopSignals(), ctx.Err())
	}
	// Stop the watcher before reporting success. A signal racing with the
	// final install must become an error, never a successful done event.
	if sig := stopSignals(); sig != nil {
		return desktopBuildError(sig, ctx.Err())
	}
	// This build worked, so whatever the last one recorded is history; the
	// dashboard must not keep showing an error the user has now fixed.
	localops.ClearDesktopBuildError()
	emit(buildEvent{Phase: localops.PhaseDone, Path: app.App})
	_, _ = fmt.Fprintf(notes, "installed %s\n", app.App)
	switch runtime.GOOS {
	case "darwin":
		if strings.HasPrefix(app.App, home+string(filepath.Separator)) {
			// The per-user folder is hidden in Finder, so the sidebar's
			// Applications entry would show nothing.
			_, _ = fmt.Fprintln(notes, "open it from Spotlight as Aether (~/Applications is hidden in Finder; this account cannot write to /Applications)")
		} else {
			_, _ = fmt.Fprintln(notes, "open it from your Applications folder or Spotlight as Aether")
		}
	case "windows":
		_, _ = fmt.Fprintf(notes, "launcher %s\nopen it from the Start Menu as Aether\n", app.Launcher)
	default:
		_, _ = fmt.Fprintf(notes, "launcher %s\nopen it from your application menu as Aether\n", app.Launcher)
	}
	return nil
}

// desktopReleases is the repository `gui build` downloads the prebuilt app
// from; tests serve their own.
var desktopReleases = "https://github.com/" + selfupdate.Repo

// prebuiltDesktop picks the app `gui build` installs as it is: the local
// zip at zipPath, or on a Windows release build the prebuilt app the
// release's signed manifest lists. A nil source means build from source,
// which is what every other platform, a development build, --source and a
// release without the prebuilt app get. A prebuilt app holds the build
// directory's lock from download to install, as BuildDesktop does for its
// own build, so two runs cannot swap the app at once; release deletes the
// download, drops the lock, and is never nil.
func prebuiltDesktop(ctx context.Context, goos, tag, zipPath string, source bool, buildDir string, notes io.Writer, phase func(string)) (src localops.DesktopSource, release func(), err error) {
	release = func() {}
	switch {
	case zipPath == "" && (source || goos != "windows"):
		return nil, release, nil
	case zipPath == "" && !selfupdate.ValidTag(tag):
		_, _ = fmt.Fprintf(notes, "this aether is a development build (%s) with no release to download the desktop app from; building it from source\n", tag)
		return nil, release, nil
	}
	unlock, err := localops.LockDesktopBuild(ctx, buildDir)
	if err != nil {
		return nil, release, err
	}
	if zipPath != "" {
		return localops.DesktopFromZip(zipPath), unlock, nil
	}
	phase(localops.PhaseDownloading)
	archive, err := localops.DownloadDesktop(ctx, desktopReleases, tag, runtime.GOARCH, buildDir, notes)
	switch {
	case errors.Is(err, localops.ErrNoPrebuiltDesktop):
		unlock()
		_, _ = fmt.Fprintf(notes, "release %s does not publish %s; building the desktop app from source\n", tag, selfupdate.DesktopAssetName(runtime.GOARCH))
		return nil, release, nil
	case err != nil:
		unlock()
		return nil, release, fmt.Errorf("%w; aether gui build --source builds the app on this machine instead", err)
	}
	return localops.DesktopFromZip(archive), func() { _ = os.Remove(archive); unlock() }, nil
}

// stagedDesktop is the app `--stage` left in buildDir. The build directory's
// lock is held until release, which also deletes the staged copy.
func stagedDesktop(ctx context.Context, buildDir string) (src localops.DesktopSource, release func(), err error) {
	unlock, err := localops.LockDesktopBuild(ctx, buildDir)
	if err != nil {
		return nil, func() {}, err
	}
	staged := filepath.Join(buildDir, localops.StagedAppDir)
	if _, err = os.Stat(staged); err != nil {
		unlock()
		return nil, func() {}, fmt.Errorf("no staged desktop app: %w", err)
	}
	return localops.DesktopFrom(staged), func() { _ = os.RemoveAll(staged); unlock() }, nil
}

// finishStaged is localops.FinishDesktopUpdate for the app installed for
// this account. install runs once pids have exited; whichever app is on
// disk afterwards is started.
func finishStaged(home string, pids []int, install func() (localops.DesktopApp, error)) (localops.DesktopApp, error) {
	current, ok := localops.InstalledDesktopApp(runtime.GOOS, localops.RealUser{Home: home})
	if !ok {
		return localops.DesktopApp{}, errors.New("no desktop app is installed to update")
	}
	var app localops.DesktopApp
	err := localops.FinishDesktopUpdate(current, pids, func() (err error) {
		app, err = install()
		return err
	})
	return app, err
}

func desktopBuildError(sig os.Signal, err error) error {
	if sig == nil {
		var child *exec.ExitError
		if errors.As(err, &child) {
			switch child.ExitCode() {
			case 130:
				sig = os.Interrupt
			case 143:
				sig = syscall.SIGTERM
			default:
				if status, ok := child.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					sig = status.Signal()
				}
			}
		}
	}
	code := 1
	switch sig {
	case os.Interrupt:
		code = 130
	case syscall.SIGTERM:
		code = 143
	}
	if code == 1 {
		return err
	}
	if err == nil {
		err = context.Canceled
	}
	return &exitStatusError{code: code, err: err}
}
