package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	"github.com/3xDevOps/Aether/internal/localops"
)

// The localops calls the rebuild makes, behind variables so a test can
// stand in for an installed app and a real Electron build.
var (
	lookupRealUser      = localops.LookupRealUser
	installedDesktopApp = localops.InstalledDesktopApp
	rebuildAppArgv      = localops.RebuildAppArgv
	desktopAppRunning   = localops.DesktopAppRunning
)

// rebuildDesktopApp rebuilds an installed desktop app with the CLI that
// `aether update` just installed. The dashboard ships inside the binary and
// the Electron shell around it does not, so an update that stopped at the
// binaries would leave the app on the old shell.
//
// A machine with no app installed - every server box - builds nothing and
// downloads nothing. newBin is the updated binary: building with the
// running process would install the shell sources it is being replaced for.
//
// The build gets its own context rather than the download's: a first build
// also fetches Node.js and the Electron runtime, which can outlast any
// timeout that suits a release download. Ctrl-C stops it.
func rebuildDesktopApp(newBin string) error {
	return rebuildDesktopAppTo(newBin, os.Stdout)
}

// rebuildDesktopAppTo is rebuildDesktopApp with its output stream
// injected, so a test can read what the command told the user.
func rebuildDesktopAppTo(newBin string, out io.Writer) error {
	who, err := lookupRealUser()
	if err != nil {
		return err
	}
	app, ok := installedDesktopApp(runtime.GOOS, who)
	if !ok {
		return nil
	}
	// Sampled before the build, not after: the install swap renames the
	// running app's directory aside, so by the time the build returns the
	// process no longer looks like it is running out of the app path.
	wasRunning := desktopAppRunning(runtime.GOOS, app)
	argv := rebuildAppArgv(newBin, who, false)
	_, _ = fmt.Fprintf(out, "rebuilding the desktop app at %s\n", app)
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals...)
	defer stop()
	// Ctrl-C at a terminal signals the whole foreground process group, so
	// it reaches the build either way. Killing this process alone under
	// sudo stops the sudo wrapper only: sudo does not forward the signal,
	// and the build runs on to completion.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// The CLI half already succeeded. Saying so is the difference
		// between "my update broke" and "my app is one command behind".
		return fmt.Errorf("aether is updated, but the desktop app was not rebuilt: %w\nrerun it with: %s",
			err, strings.Join(argv, " "))
	}
	if wasRunning {
		_, _ = fmt.Fprintln(out, "restart the Aether app to use the new version")
	}
	return nil
}
