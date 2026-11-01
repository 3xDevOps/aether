package main

import (
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "subsessions",
		short: "turn subsessions on or off, or list a session's subsessions",
		usage: "aether subsessions [on|off]\n" +
			"aether subsessions <session-id>",
		example: "aether subsessions off\n" +
			"aether subsessions run-d6ay38tj8j",
		run: runChildren,
	})
}

func runChildren(args []string) error {
	if len(args) == 1 && strings.HasPrefix(args[0], "run-") {
		return listChildren(args[0])
	}
	var p protocol.ChildSettingsParams
	if len(args) > 0 && (args[0] == "on" || args[0] == "off") {
		on := args[0] == "on"
		p.Enabled, args = &on, args[1:]
	}
	fs := newFlagSet("subsessions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether subsessions [on|off]\n   or: aether subsessions <session-id>")
	}
	return withControl(func(c *protocol.Client) error {
		var out protocol.ChildSettings
		if err := c.Call(protocol.MethodChildSettings, p, &out); err != nil {
			return err
		}
		fmt.Printf("subsessions  %s\n", onOffWord(out.Enabled))
		if !out.Enabled {
			fmt.Println("Your sessions' agents cannot start subsessions. aether session --children on allows one session.")
		}
		return nil
	})
}

func onOffWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func listChildren(run string) error {
	return withControl(func(c *protocol.Client) error {
		var out protocol.RunChildrenResult
		if err := c.Call(protocol.MethodRunChildren, protocol.RunChildrenParams{RunID: run}, &out); err != nil {
			return err
		}
		if len(out.Children) == 0 {
			fmt.Printf("session %s has no subsessions\n", run)
		}
		for _, ch := range out.Children {
			agent := ch.Harness
			if ch.Model != "" {
				agent += " " + ch.Model
			}
			line := fmt.Sprintf("%s  %-9s  %s  %s  account %s", ch.RunID, ch.State, agent, ch.Purpose, ch.AccountMemberID)
			if ch.Detached {
				line += "  detached"
			}
			fmt.Println(line)
		}
		u := out.Usage
		cost := fmt.Sprintf("$%.2f", u.CostUSD)
		if u.UnmeteredRuns > 0 {
			cost = fmt.Sprintf("at least $%.2f (%d of %d sessions report no cost)", u.CostUSD, u.UnmeteredRuns, u.Runs)
		}
		fmt.Printf("usage   %s, %d input and %d output tokens across %d sessions\n", cost, u.InputTokens, u.OutputTokens, u.Runs)
		fmt.Printf("limits  %d busy at once per parent\n", out.Limits.MaxLive)
		return nil
	})
}
