package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const memberAssistantUsage = "usage: aether member assistant [--auto-approve on|off]"

// memberAssistant implements `aether member assistant`: without flags it
// shows the caller's assistant settings; --auto-approve changes whether the
// assistant launches, messages and stops runs and saves the environment
// without a card. A member sets only their own.
func memberAssistant(args []string) error {
	fs := flag.NewFlagSet("member assistant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	auto := fs.String("auto-approve", "", "on or off")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
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
		state := "off: launching, messaging and stopping runs and saving the environment wait for your approval"
		if settings.AutoApprove {
			state = "on: the assistant launches, messages and stops runs and saves the environment without asking"
		}
		fmt.Println("auto-approve  " + state)
		return nil
	})
}
