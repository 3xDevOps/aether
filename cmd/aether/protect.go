package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "protect",
		short: "let only a run's owner and admins steer or kill it",
		run:   func(args []string) error { return setProtected(args, true) },
	})
	register(command{
		name:  "unprotect",
		short: "lift a run's protection",
		run:   func(args []string) error { return setProtected(args, false) },
	})
}

// setProtected implements `aether protect|unprotect <run-id>` over
// run.protect. The server limits the call to the run's owner and admins
// and stamps the change into the workspace timeline.
func setProtected(args []string, protect bool) error {
	verb := "protect"
	if !protect {
		verb = "unprotect"
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: aether %s <run-id>", verb)
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunProtect, protocol.RunProtectParams{
			RunID:     args[0],
			Protected: protect,
		}, &res); err != nil {
			return err
		}
		if res.Run.Protected {
			fmt.Printf("protected %s: only its owner and admins may steer or kill it\n", res.Run.ID)
		} else {
			fmt.Printf("unprotected %s: the workspace's steering policy applies again\n", res.Run.ID)
		}
		return nil
	})
}
