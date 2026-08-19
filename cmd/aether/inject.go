package main

import (
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "inject",
		short: "inject a steering message into a run",
		run:   runInject,
	})
}

func runInject(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: aether inject <run-id> <message...>")
	}
	return withControl(func(c *protocol.Client) error {
		return c.Call(protocol.MethodRunInject, protocol.RunInjectParams{
			RunID:   args[0],
			Message: strings.Join(args[1:], " "),
		}, nil)
	})
}
