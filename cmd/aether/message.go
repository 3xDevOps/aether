package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "message",
		short: "send a message to a run's agent (- reads it from stdin)",
		run:   runMessage,
	})
}

func runMessage(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: aether message <run-id> <message...>|-")
	}
	text, err := stdinText(strings.Join(args[1:], " "), "message", os.Stdin)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		return sendMessage(c, os.Stdout, args[0], text, cli.NewControlSessionID())
	})
}

func sendMessage(c *protocol.Client, w io.Writer, runID, text, key string) error {
	var res protocol.RunRoomPostResult
	if err := c.Call(protocol.MethodRunInject, protocol.RunInjectParams{RunID: runID, Message: text, IdempotencyKey: key}, &res); err != nil {
		return runNotFound(runID, err)
	}
	m := res.Message
	reason := ""
	if m.Failure != nil {
		reason = ": " + m.Failure.Message
	}
	var line string
	switch m.State {
	case protocol.RoomMessageSent:
		line = fmt.Sprintf("message %s delivered to run %s", m.ID, runID)
	case protocol.RoomMessageQueued:
		line = fmt.Sprintf("message %s queued for the member in control of run %s, who can approve or deny it", m.ID, runID)
		if m.DeliverAfter != nil {
			line += "; otherwise it is delivered at " + *m.DeliverAfter
		}
	case protocol.RoomMessageUncertain:
		line = fmt.Sprintf("message %s to run %s: delivery uncertain%s", m.ID, runID, reason)
	default:
		return fmt.Errorf("message %s to run %s was %s%s", m.ID, runID, strings.ReplaceAll(string(m.State), "_", " "), reason)
	}
	_, err := fmt.Fprintln(w, line)
	return err
}
