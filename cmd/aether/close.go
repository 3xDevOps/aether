package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "close",
		short:   "close a run as merged or abandoned",
		usage:   "aether close <run-id> --outcome <merged|abandoned>",
		example: "aether close run-d6ay38tj8j --outcome merged",
		flags:   true,
		run:     runClose,
	})
}

func runClose(args []string) error {
	fs := newFlagSet("close")
	outcome := fs.String("outcome", "", "merged or abandoned")
	runID, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\nusage: aether close <run-id> --outcome merged|abandoned", err)
	}
	if runID == "" || (*outcome != "merged" && *outcome != "abandoned") {
		return fmt.Errorf("usage: aether close <run-id> --outcome merged|abandoned")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunClose, protocol.RunCloseParams{
			RunID:   runID,
			Outcome: *outcome,
		}, &res); err != nil {
			return runNotFound(runID, err)
		}
		fmt.Printf("run %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}
