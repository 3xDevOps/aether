package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/3xDevOps/Aether/desktop"
	"github.com/3xDevOps/Aether/internal/localops"
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

// guiBuild packages the embedded Electron shell for this machine and
// installs it where the desktop lists applications, so the dashboard opens
// as a native window without a source checkout.
func guiBuild(args []string) error {
	return guiBuildTo(args, os.Stdout)
}

// guiBuildTo is guiBuild with its --json stream injected, so a test can
// read the phase lines without taking over the process's stdout.
func guiBuildTo(args []string, events io.Writer) error {
	fs := flag.NewFlagSet("gui build", flag.ExitOnError)
	buildDir := fs.String("build-dir", "", "where to unpack the shell sources and run npm (default: the user cache directory)")
	jsonOut := fs.Bool("json", false, "print one JSON line per build phase on stdout; the build's own output goes to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	// With --json stdout carries the phase lines and nothing else, so the
	// build's own output and every note move to stderr.
	notes := io.Writer(os.Stdout)
	if *jsonOut {
		notes = os.Stderr
	}
	emit := func(ev buildEvent) {
		if !*jsonOut {
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
	err := buildAndInstall(*buildDir, notes, emit)
	if err != nil {
		emit(buildEvent{Phase: localops.PhaseError, Error: err.Error()})
	}
	return err
}

// buildAndInstall is guiBuild's work, split out so every failure reaches
// the one place that reports it as an error event.
func buildAndInstall(buildDir string, notes io.Writer, emit func(buildEvent)) error {
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
	// The app finds the CLI the same way the shell does at launch. Refuse
	// early rather than install a window that only shows "aether CLI not
	// found".
	found, shellOnly := localops.DesktopFindsCLI(home)
	if !found {
		return errors.New("aether is not installed where the desktop app looks (PATH, /usr/local/bin, ~/.local/bin); install the CLI there first (see docs/install.md)")
	}
	if shellOnly != "" {
		fmt.Fprintf(os.Stderr, "warning: aether was found at %s through this terminal's PATH; the application menu may not share it. If the window reports \"aether CLI not found\", install the CLI into /usr/local/bin or ~/.local/bin, or set AETHER_BIN.\n", shellOnly)
	}

	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals...)
	defer stop()

	_, _ = fmt.Fprintf(notes, "building the desktop app in %s\n", buildDir)
	phase := func(name string) { emit(buildEvent{Phase: name}) }
	built, err := localops.BuildDesktop(ctx, desktop.Source, buildDir, version.Version, notes, os.Stderr, phase)
	if err != nil {
		return err
	}
	icon, err := desktop.Source.ReadFile("build/icons/256x256.png")
	if err != nil {
		return err
	}
	phase(localops.PhaseInstalling)
	app, err := localops.InstallDesktop(runtime.GOOS, home, built, icon)
	if err != nil {
		return err
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
		_, _ = fmt.Fprintln(notes, "open it from the Start Menu as Aether")
	default:
		_, _ = fmt.Fprintf(notes, "launcher %s\nopen it from your application menu as Aether\n", app.Launcher)
	}
	return nil
}
