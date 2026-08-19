package main

import (
	"flag"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "close",
		short: "close a needs-attention run",
		run:   runClose,
	})
}

func runClose(args []string) error {
	fs := flag.NewFlagSet("close", flag.ExitOnError)
	outcome := fs.String("outcome", "", "merged or abandoned")
	runID, err := parseLeadingArg(fs, args)
	if err != nil || runID == "" || (*outcome != "merged" && *outcome != "abandoned") {
		return fmt.Errorf("usage: aether close <run-id> --outcome merged|abandoned")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunClose, protocol.RunCloseParams{
			RunID:   runID,
			Outcome: *outcome,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("run %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}
