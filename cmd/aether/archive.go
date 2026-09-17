package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "archive",
		short: "hide a finished run from the board; the server deletes it on the printed date",
		run:   func(args []string) error { return setArchived(args, true) },
	})
	register(command{
		name:  "unarchive",
		short: "restore an archived run to the board and default aether runs",
		run:   func(args []string) error { return setArchived(args, false) },
	})
}

func setArchived(args []string, archive bool) error {
	verb := "archive"
	if !archive {
		verb = "unarchive"
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: aether %s <run-id>", verb)
	}
	runID := args[0]
	return withControl(func(c *protocol.Client) error {
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunArchive, protocol.RunArchiveParams{
			RunID:    runID,
			Archived: archive,
		}, &res); err != nil {
			return fmt.Errorf("%s run %q: %w", verb, runID, err)
		}
		if archive {
			if res.Run.DeletesAt == nil {
				return fmt.Errorf("archive run %q: server reply carries no deletes_at", runID)
			}
			fmt.Printf("archived %s: off the board until the server deletes it on %s; restore with: aether unarchive %s\n",
				res.Run.ID, *res.Run.DeletesAt, res.Run.ID)
		} else {
			fmt.Printf("%s is not archived: it shows on the board and in aether runs\n", res.Run.ID)
		}
		return nil
	})
}
