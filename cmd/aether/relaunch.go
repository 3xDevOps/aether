package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "relaunch",
		short: "relaunch a terminal run as a new run",
		run:   runRelaunch,
	})
}

func runRelaunch(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether relaunch <run-id>")
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
