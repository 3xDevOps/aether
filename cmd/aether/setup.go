package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/cli"
)

func init() {
	register(command{
		name:  "setup",
		short: "interactive harness login on the server",
		run:   runSetup,
	})
}

func runSetup(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether setup <harness>")
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	cols, rows := termSize()
	stream, err := conn.Setup(args[0], "", cols, rows)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	return copyRaw(stream)
}
