package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localgw"
)

func init() {
	register(command{
		name:  "gui",
		short: "serve the dashboard locally with full SSH authority",
		run:   runGUI,
	})
}

func runGUI(args []string) error {
	fs := flag.NewFlagSet("gui", flag.ExitOnError)
	port := fs.Int("port", 0, "loopback port to bind (0 picks an ephemeral one)")
	urlOnly := fs.Bool("url", false, "print the gateway URL instead of opening a browser")
	jsonOut := fs.Bool("json", false, "print one JSON line with the URL and address, then keep serving")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	gw, err := localgw.New(localgw.Config{
		Port:    *port,
		Backend: localgw.NewSSHBackend(cfg),
		CLI:     cfg,
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
	waitForExit()
	return nil
}
