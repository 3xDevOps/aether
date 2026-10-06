package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const runSwitchUsage = "usage: aether run switch <run-id> --mode standard|enhanced"

func runSwitch(args []string) error {
	runID := args[0]
	fs := flag.NewFlagSet("run switch", flag.ExitOnError)
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
		fmt.Printf("run %s switched to %s\n", res.Run.ID, *mode)
		return nil
	})
}
