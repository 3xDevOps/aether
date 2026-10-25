package main

import (
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const memberAssistantUsage = "usage: aether member assistant [--auto-approve on|off]"

// memberAssistant implements `aether member assistant`: without flags it
// shows the caller's assistant settings; --auto-approve changes whether the
// assistant launches, messages and stops runs and saves the environment
// without a card. A member sets only their own.
func memberAssistant(args []string) error {
	fs := newFlagSet("member assistant")
	auto := fs.String("auto-approve", "", "on or off")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, memberAssistantUsage)
	}
	if fs.NArg() > 0 {
		return errors.New(memberAssistantUsage)
	}
	var p protocol.AssistantSettingsParams
	switch *auto {
	case "":
	case "on", "off":
		on := *auto == "on"
		p.AutoApprove = &on
	default:
		return fmt.Errorf("--auto-approve %q: want on or off", *auto)
	}
	return withControl(func(c *protocol.Client) error {
		var settings protocol.AssistantSettings
		if err := c.Call(protocol.MethodAssistantSettings, p, &settings); err != nil {
			return err
		}
		state := "off: each action below waits for your approval on a card"
		if settings.AutoApprove {
			state = "on: the actions below run without a card, noted in the transcript first"
		}
		fmt.Println("auto-approve  " + state)
		fmt.Println("covers        launching runs, messaging and stopping your own runs, saving your environment")
		fmt.Println("always asks   messaging or stopping another member's run")
		fmt.Println("not covered   the assistant's own shell and file tools, which never ask")
		return nil
	})
}
