package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "pause",
		short: "pause a run",
		run:   runPause,
	})
}

func runPause(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether pause <run-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return c.Call(protocol.MethodRunPause, protocol.RunIDParams{RunID: args[0]}, nil)
	})
}
