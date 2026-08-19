package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "kill",
		short: "kill a run",
		run:   runKill,
	})
}

func runKill(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether kill <run-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return c.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: args[0]}, nil)
	})
}
