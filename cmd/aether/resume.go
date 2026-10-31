package main

import (
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "resume",
		short:   "resume a paused session",
		usage:   "aether resume <session-id>",
		example: "aether resume run-d6ay38tj8j",
		run:     runResume,
	})
}

func runResume(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether resume <session-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return actOnRun(c, os.Stdout, protocol.MethodRunResume, args[0])
	})
}
