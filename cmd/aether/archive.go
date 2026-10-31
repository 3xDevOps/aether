package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "archive",
		short:   "hide a finished session from the board without deleting its history",
		usage:   "aether archive <session-id>",
		example: "aether archive run-d6ay38tj8j",
		run:     func(args []string) error { return setArchived(args, true) },
	})
	register(command{
		name:    "unarchive",
		short:   "restore an archived session to the board and default aether sessions",
		usage:   "aether unarchive <session-id>",
		example: "aether unarchive run-d6ay38tj8j",
		run:     func(args []string) error { return setArchived(args, false) },
	})
}

func setArchived(args []string, archive bool) error {
	verb := "archive"
	if !archive {
		verb = "unarchive"
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: aether %s <session-id>", verb)
	}
	runID := args[0]
	return withControl(func(c *protocol.Client) error {
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunArchive, protocol.RunArchiveParams{
			RunID:    runID,
			Archived: archive,
		}, &res); err != nil {
			return fmt.Errorf("%s session %q: %w", verb, runID, err)
		}
		if archive {
			fmt.Printf("archived %s: off the board; restore with: aether unarchive %s\n",
				res.Run.ID, res.Run.ID)
		} else {
			fmt.Printf("%s is not archived: it shows on the board and in aether sessions\n", res.Run.ID)
		}
		return nil
	})
}
