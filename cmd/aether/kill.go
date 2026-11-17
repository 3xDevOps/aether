package main

import (
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "kill",
		short:   "kill a session",
		usage:   "cloudfield kill <session-id>",
		example: "cloudfield kill run-d6ay38tj8j",
		run:     runKill,
	})
}

func runKill(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: cloudfield kill <session-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return actOnRun(c, os.Stdout, protocol.MethodRunKill, args[0])
	})
}
