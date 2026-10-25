package main

import (
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "kill",
		short:   "kill a run",
		usage:   "aether kill <run-id>",
		example: "aether kill run-d6ay38tj8j",
		run:     runKill,
	})
}

func runKill(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether kill <run-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return actOnRun(c, os.Stdout, protocol.MethodRunKill, args[0])
	})
}
