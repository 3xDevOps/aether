package main

import (
	"fmt"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "resume",
		short: "resume a paused run",
		run:   runResume,
	})
}

func runResume(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether resume <run-id>")
	}
	return withControl(func(c *protocol.Client) error {
		return actOnRun(c, os.Stdout, protocol.MethodRunResume, args[0])
	})
}
