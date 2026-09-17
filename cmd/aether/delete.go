package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "delete",
		short: "delete a run, its transcript, and its evidence; a published branch stays",
		run:   runDelete,
	})
}

func runDelete(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: aether delete <run-id>")
	}
	return withControl(func(c *protocol.Client) error {
		if err := c.Call(protocol.MethodRunDelete, protocol.RunIDParams{RunID: args[0]}, nil); err != nil {
			return fmt.Errorf("delete run %q: %w", args[0], err)
		}
		fmt.Printf("deleted %s: its checkout, transcript, evidence, and run records are gone; a published run branch stays in the workspace repo\n", args[0])
		return nil
	})
}
