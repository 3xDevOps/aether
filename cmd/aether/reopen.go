package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "reopen",
		short: "reopen a finished run that still keeps its container",
		run:   runReopen,
	})
}

func runReopen(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether reopen <run-id>")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: args[0]}, &res); err != nil {
			return err
		}
		fmt.Printf("run %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}
