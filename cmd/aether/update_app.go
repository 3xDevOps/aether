package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	"github.com/3xDevOps/Aether/internal/localops"
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
	who, err := localops.LookupRealUser()
	if err != nil {
		return err
	}
	app, ok := localops.InstalledDesktopApp(runtime.GOOS, who)
	if !ok {
		return nil
	}
	argv := localops.RebuildAppArgv(newBin, who, false)
	fmt.Printf("rebuilding the desktop app at %s\n", app)
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals...)
	defer stop()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// The CLI half already succeeded. Saying so is the difference
		// between "my update broke" and "my app is one command behind".
		return fmt.Errorf("aether is updated, but the desktop app was not rebuilt: %w\nrerun it with: %s",
			err, strings.Join(argv, " "))
	}
	if localops.DesktopAppRunning(runtime.GOOS, app) {
		fmt.Println("restart the Aether app to use the new version")
	}
	return nil
}
