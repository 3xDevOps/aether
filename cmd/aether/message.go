package main

import (
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "message",
		short: "send a message to a run's agent",
		run:   runMessage,
	})
}

func runMessage(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: aether message <run-id> <message...>")
	}
	return withControl(func(c *protocol.Client) error {
		return c.Call(protocol.MethodRunInject, protocol.RunInjectParams{
			RunID:          args[0],
			Message:        strings.Join(args[1:], " "),
			IdempotencyKey: cli.NewControlSessionID(),
		}, nil)
	})
}
