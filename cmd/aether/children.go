package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "children",
		short: "turn child runs on or off, or list a run's child runs",
		usage: "aether children [on|off] [--fallback on|off] [--shared-when-missing on|off]\n" +
			"aether children <run-id>",
		example: "aether children on --shared-when-missing on\n" +
			"aether children run-d6ay38tj8j",
		flags: true,
		run:   runChildren,
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
	fs := newFlagSet("children")
	fs.String("fallback", "", "on: an exhausted Claude or Codex account rolls over to your own or a shared account with usage left, same agent and model; off: children stay on their account")
	fs.String("shared-when-missing", "", "on: when you are not signed in to an agent, a child starts on a teammate's account shared with you")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether children [on|off] [--fallback on|off] [--shared-when-missing on|off]\n   or: aether children <run-id>")
	}
	var err error
	fs.Visit(func(f *flag.Flag) {
		if err != nil {
			return
		}
		var v *bool
		if v, err = onOff("--"+f.Name, f.Value.String()); err == nil {
			if f.Name == "fallback" {
				p.Fallback = v
			} else {
				p.SharedWhenMissing = v
			}
		}
	})
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var out protocol.ChildSettings
		if err := c.Call(protocol.MethodChildSettings, p, &out); err != nil {
			return err
		}
		fmt.Printf("child runs            %s\n", onOffWord(out.Enabled))
		fmt.Printf("fallback              %s\n", onOffWord(out.Fallback))
		fmt.Printf("shared when missing   %s\n", onOffWord(out.SharedWhenMissing))
		if !out.Enabled {
			fmt.Println("Your runs' agents cannot start child runs; swarms use their own agent choices. aether run --children on allows one run.")
		}
		return nil
	})
}

func onOff(flagName, v string) (*bool, error) {
	switch v {
	case "on":
		t := true
		return &t, nil
	case "off":
		f := false
		return &f, nil
	}
	return nil, fmt.Errorf("invalid %s %q: want on or off", flagName, v)
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
			fmt.Printf("run %s has no child runs\n", run)
		}
		for _, ch := range out.Children {
			agent := ch.Harness
			if ch.Model != "" {
				agent += " " + ch.Model
			}
			line := fmt.Sprintf("%s%s  %-9s  %s  %s  account %s", strings.Repeat("  ", ch.Depth-1), ch.RunID, ch.State, agent, ch.Purpose, ch.AccountMemberID)
			if ch.Detached {
				line += "  detached"
			}
			if ch.Stale {
				line += "  stale"
			}
			fmt.Println(line)
		}
		u := out.Usage
		cost := fmt.Sprintf("$%.2f", u.CostUSD)
		if u.UnmeteredRuns > 0 {
			cost = fmt.Sprintf("at least $%.2f (%d of %d runs report no cost)", u.CostUSD, u.UnmeteredRuns, u.Runs)
		}
		fmt.Printf("usage   %s, %d input and %d output tokens across %d runs\n", cost, u.InputTokens, u.OutputTokens, u.Runs)
		fmt.Printf("limits  %d busy at once, %d levels deep, %d per tree\n", out.Limits.MaxLive, out.Limits.MaxDepth, out.Limits.MaxTotal)
		return nil
	})
}
