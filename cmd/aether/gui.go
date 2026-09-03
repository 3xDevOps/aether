package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localgw"
	"github.com/3xDevOps/Aether/internal/selfupdate"
)

func init() {
	register(command{
		name:  "gui",
		short: "serve the dashboard locally with full SSH authority; `gui build` installs the desktop app",
		run:   runGUI,
	})
}

func runGUI(args []string) error {
	if len(args) > 0 && args[0] == "build" {
		return guiBuild(args[1:])
	}
	fs := flag.NewFlagSet("gui", flag.ExitOnError)
	port := fs.Int("port", 0, "loopback port to bind (0 picks an ephemeral one)")
	urlOnly := fs.Bool("url", false, "print the gateway URL instead of opening a browser")
	jsonOut := fs.Bool("json", false, "print one JSON line with the URL and address, then keep serving")
	server := fs.String("server", "", "named server profile from `aether link --name` (default: the top-level link)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	if *server != "" {
		named, ok := cfg.Named(*server)
		if !ok {
			names := make([]string, len(cfg.Links))
			for i, l := range cfg.Links {
				names[i] = l.Name
			}
			if len(names) == 0 {
				return fmt.Errorf("no server named %q; no named links saved (aether link --name)", *server)
			}
			return fmt.Errorf("no server named %q; available: %s", *server, strings.Join(names, ", "))
		}
		cfg = named
	}
	// One checker serves both the startup nudge and the update verbs, so
	// the dashboard reads the answer the banner already paid for.
	checker := selfupdate.DefaultChecker()
	gw, err := localgw.New(localgw.Config{
		Port:    *port,
		Backend: localgw.NewSSHBackend(cfg),
		CLI:     cfg,
		Update:  checker,
		// The desktop shell spawns `aether gui --json` and restarts it,
		// so an update applied from the dashboard can exit the process.
		Supervised: *jsonOut,
	})
	if err != nil {
		return err
	}
	if err := gw.Start(context.Background()); err != nil {
		return err
	}
	defer func() { _ = gw.Close() }()
	url := "http://" + gw.Addr() + "/?token=" + gw.Token()
	if *jsonOut {
		// One machine-readable line for the desktop shell sidecar, which
		// parses it and renders the SPA itself.
		line, err := json.Marshal(struct {
			URL  string `json:"url"`
			Addr string `json:"addr"`
		}{URL: url, Addr: gw.Addr()})
		if err != nil {
			return err
		}
		fmt.Println(string(line))
		_ = os.Stdout.Sync()
	} else {
		fmt.Println(url)
		if !*urlOnly {
			openBrowser(url)
		}
	}
	go nudgeUpdate(checker)
	waitForExit(gw.Exit())
	if code := gw.ExitCode(); code != 0 {
		// localgw.ExitRelaunch: update.apply rebuilt the desktop app, so
		// the shell has to relaunch itself instead of respawning this
		// sidecar into the window running the old app. The deferred Close
		// does not run past os.Exit, so drain the gateway first.
		_ = gw.Close()
		os.Exit(code)
	}
	return nil
}

// nudgeUpdate prints one line naming the command when a newer release
// exists. It writes to stderr because stdout carries the --json handshake
// the desktop shell parses. A failed check has no reader to report to and
// no bearing on serving the dashboard, so it stays silent; `aether update
// --check` reports the same failure with an exit status.
func nudgeUpdate(c *selfupdate.Checker) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	check, err := c.Check(ctx)
	if err != nil || !check.UpdateAvailable {
		return
	}
	fmt.Fprintf(os.Stderr, "update available: %s (running %s); run: aether update\n", check.Latest, check.Version)
}

// waitForExit blocks until the process is told to stop. SIGTERM belongs
// here with Ctrl-C: without it a `kill` or a systemd stop skips the
// deferred gateway shutdown and leaves the listener live. SIGHUP covers
// a closed terminal window the same way. The gateway asks for the same
// stop through exit after update.apply replaces this binary.
func waitForExit(exit <-chan struct{}) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, append([]os.Signal{syscall.SIGHUP}, terminationSignals...)...)
	select {
	case <-ch:
	case <-exit:
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
