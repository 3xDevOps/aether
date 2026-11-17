package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "reopen",
		short:   "reopen a finished session that still keeps its sandbox",
		usage:   "cloudfield reopen <session-id>",
		example: "cloudfield reopen run-d6ay38tj8j",
		run:     runReopen,
	})
}

func runReopen(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: cloudfield reopen <session-id>")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: args[0]}, &res); err != nil {
			return runNotFound(args[0], err)
		}
		fmt.Printf("session %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}
