package main

import (
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const runSwitchUsage = "usage: aether session switch <session-id> --mode standard|enhanced"

func runSwitch(args []string) error {
	runID := args[0]
	fs := newFlagSet("session switch")
	mode := fs.String("mode", "", "standard or enhanced")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	wire, err := parseLaunchMode(*mode)
	if err != nil || wire == "headless" || fs.NArg() != 0 {
		return errors.New(runSwitchUsage)
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunModeSwitch, protocol.RunModeSwitchParams{RunID: runID, Mode: wire}, &res); err != nil {
			return err
		}
		fmt.Printf("session %s switched to %s\n", res.Run.ID, *mode)
		return nil
	})
}
