package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:    "delete",
		short:   "delete a session, its transcript, and its captures; a published branch stays",
		usage:   "aether delete <session-id>",
		example: "aether delete run-d6ay38tj8j",
		run:     runDelete,
	})
}

func runDelete(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: aether delete <session-id>")
	}
	return withControl(func(c *protocol.Client) error {
		if err := c.Call(protocol.MethodRunDelete, protocol.RunIDParams{RunID: args[0]}, nil); err != nil {
			return fmt.Errorf("delete session %q: %w", args[0], err)
		}
		fmt.Printf("deleted %s: its checkout, transcript, captures, and session records are gone; a published session branch stays in the workspace repo\n", args[0])
		return nil
	})
}
