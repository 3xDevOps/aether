package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sysbox"
)

// Seams for setup_test.go and install_test.go: nothing else installs a
// runtime on the machine running the tests.
var (
	sysboxStatus  = sysbox.Status
	sysboxInstall = sysbox.Install
)

const sysboxManual = "sudo aether-server sysbox install"

const sysboxNeedsDocker = "Skipping Docker inside runs (Sysbox), which needs Docker; once Docker answers: " + sysboxManual

// sysboxOn reports whether Docker already has Sysbox. A Docker that cannot
// be asked counts as off so the installer reports the real problem.
func sysboxOn() bool {
	state, err := sysboxStatus(context.Background())
	return err == nil && state == protocol.SysboxOn
}

// installSysbox runs the installer for setup and install. Docker inside
// runs is optional, so neither an unsupported host nor a failure fails the
// command that called it, and Ctrl-C stops only the wait for the installer:
// the files are written by then, and the caller still prints how to start
// them.
func installSysbox(w io.Writer) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := sysboxInstall(ctx, w)
	var unsupported *sysbox.UnsupportedError
	var detached *sysbox.DetachedError
	switch {
	case err == nil:
	case errors.As(err, &detached):
		_, _ = fmt.Fprintf(w, "Stopped waiting for Sysbox: %v\nOnce it has, check Docker inside runs with: aether-server sysbox status\n", err)
	case errors.As(err, &unsupported):
		_, _ = fmt.Fprintf(w, "Docker inside runs is unavailable on this host: %s\n", unsupported.Reason)
	default:
		_, _ = fmt.Fprintf(w, "Docker inside runs is not installed: %v\nInstall it later with: %s\n", err, sysboxManual)
	}
}

func sysboxCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: aether-server sysbox status | install")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch args[0] {
	case "status":
		state, err := sysboxStatus(ctx)
		if err != nil {
			return err
		}
		fmt.Println(state)
		return nil
	case "install":
		if os.Geteuid() != 0 {
			return errors.New("installing Sysbox needs root; re-run with sudo")
		}
		return sysboxInstall(ctx, os.Stdout)
	}
	return fmt.Errorf("unknown sysbox command %q; use status or install", args[0])
}
