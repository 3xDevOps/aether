package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/3xDevOps/Aether/desktop"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/version"
)

// guiBuild packages the embedded Electron shell for this machine and
// installs it where the desktop lists applications, so the dashboard opens
// as a native window without a source checkout.
func guiBuild(args []string) error {
	fs := flag.NewFlagSet("gui build", flag.ExitOnError)
	buildDir := fs.String("build-dir", "", "where to unpack the shell sources and run npm (default: the user cache directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *buildDir == "" {
		dir, err := localops.DesktopBuildDir()
		if err != nil {
			return err
		}
		*buildDir = dir
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

	fmt.Printf("building the desktop app in %s\n", *buildDir)
	built, err := localops.BuildDesktop(ctx, desktop.Source, *buildDir, version.Version, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	icon, err := desktop.Source.ReadFile("build/icons/256x256.png")
	if err != nil {
		return err
	}
	app, err := localops.InstallDesktop(runtime.GOOS, home, built, icon)
	if err != nil {
		return err
	}
	fmt.Printf("installed %s\n", app.App)
	switch runtime.GOOS {
	case "darwin":
		if strings.HasPrefix(app.App, home+string(filepath.Separator)) {
			// The per-user folder is hidden in Finder, so the sidebar's
			// Applications entry would show nothing.
			fmt.Println("open it from Spotlight as Aether (~/Applications is hidden in Finder; this account cannot write to /Applications)")
		} else {
			fmt.Println("open it from your Applications folder or Spotlight as Aether")
		}
	case "windows":
		fmt.Println("open it from the Start Menu as Aether")
	default:
		fmt.Printf("launcher %s\nopen it from your application menu as Aether\n", app.Launcher)
	}
	return nil
}
