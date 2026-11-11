package main

import (
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "subsessions",
		short: "turn subsessions, or one agent for them, on or off, or list a session's subsessions",
		usage: "aether subsessions [on|off] [--agent <agent> [--account <member-id>]]\n" +
			"aether subsessions <session-id>",
		example: "aether subsessions off\n" +
			"aether subsessions off --agent codex\n" +
			"aether subsessions off --agent claude --account mem-3j2sxtk2zk\n" +
			"aether subsessions run-d6ay38tj8j",
		run: runChildren,
	})
}

func runChildren(args []string) error {
	if len(args) == 1 && strings.HasPrefix(args[0], "run-") {
		return listChildren(args[0])
	}
	var on *bool
	if len(args) > 0 && (args[0] == "on" || args[0] == "off") {
		v := args[0] == "on"
		on, args = &v, args[1:]
	}
	fs := newFlagSet("subsessions")
	agent := fs.String("agent", "", "turn only this agent on or off for subsessions")
	account := fs.String("account", "", "with --agent, only on this account (member ID); every account when omitted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*account != "" && *agent == "") || (*agent != "" && on == nil) {
		return fmt.Errorf("usage: aether subsessions [on|off] [--agent <agent> [--account <member-id>]]\n   or: aether subsessions <session-id>")
	}
	var p protocol.ChildSettingsParams
	if *agent != "" {
		p.Choice = &protocol.ChildChoiceSetting{Harness: *agent, AccountMemberID: *account, On: *on}
	} else {
		p.Enabled = on
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
		printChildChoices(out)
		return nil
	})
}

// printChildChoices lists each installed agent and account with whether
// subsessions may use it, and the agents turned off that none lists.
func printChildChoices(s protocol.ChildSettings) {
	off := make(domain.ChildChoicesOff, 0, len(s.Off))
	for _, c := range s.Off {
		off = append(off, domain.ChildChoice{Harness: c.Harness, Account: domain.MemberID(c.AccountMemberID)})
	}
	listed := map[string]bool{}
	for _, c := range s.Choices {
		if c.Status == "not-installed" {
			continue
		}
		listed[c.Harness] = true
		use := "on"
		if off.Covers(c.Harness, domain.MemberID(c.AccountMemberID)) {
			use = "off"
		}
		fmt.Printf("  %-3s  %-10s  %s's account (%s)  %s\n", use, c.Harness, c.AccountName, c.AccountMemberID, c.Status)
	}
	for _, c := range s.Off {
		if c.AccountMemberID == "" && !listed[c.Harness] {
			fmt.Printf("  off  %-10s  every account\n", c.Harness)
		}
	}
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
			line := fmt.Sprintf("%s  %-12s  %s  %s  account %s", ch.RunID, ch.State, agent, ch.Purpose, ch.AccountMemberID)
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
