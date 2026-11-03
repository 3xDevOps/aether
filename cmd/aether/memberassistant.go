package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	memberAssistantUsage      = "usage: aether member assistant [--auto-approve on|off]\n       aether member assistant rules [--revoke <id>]"
	memberAssistantRulesUsage = "usage: aether member assistant rules [--revoke <id>]"
)

// memberAssistant implements `aether member assistant`: without flags it
// shows the caller's assistant settings; --auto-approve changes whether the
// assistant launches, messages and stops runs and saves the environment
// without a card. A member sets only their own.
func memberAssistant(args []string) error {
	if len(args) > 0 && args[0] == "rules" {
		return memberAssistantRules(args[1:])
	}
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
		fmt.Println("covers        launching sessions, messaging and stopping your own sessions, saving your environment")
		fmt.Println("always asks   messaging or stopping another member's session")
		fmt.Println("not covered   the assistant's own shell and file tools, which never ask")
		return nil
	})
}

// memberAssistantRules implements `aether member assistant rules`: it lists
// the caller's approval rules, or with --revoke deletes one. Rules are
// created only by approving a card with Always approve in the Aether app.
func memberAssistantRules(args []string) error {
	fs := newFlagSet("member assistant rules")
	revoke := fs.String("revoke", "", "the id of the rule to revoke")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, memberAssistantRulesUsage)
	}
	if fs.NArg() > 0 {
		return errors.New(memberAssistantRulesUsage)
	}
	return withControl(func(c *protocol.Client) error { return assistantRules(c, os.Stdout, *revoke) })
}

func assistantRules(c *protocol.Client, w io.Writer, revoke string) error {
	if revoke != "" {
		if err := c.Call(protocol.MethodAssistantRuleRevoke, protocol.AssistantRuleRevokeParams{ID: revoke}, nil); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "revoked %s: the cards it passed ask again\n", revoke)
		return err
	}
	var res protocol.AssistantRulesResult
	if err := c.Call(protocol.MethodAssistantRules, nil, &res); err != nil {
		return err
	}
	if len(res.Rules) == 0 {
		_, err := fmt.Fprintln(w, "no approval rules; approve a card with Always approve in the Aether app to add one")
		return err
	}
	day := func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }
	for _, r := range res.Rules {
		used := "never used"
		if !r.LastUsedAt.IsZero() {
			used = fmt.Sprintf("%d uses, last %s", r.Uses, day(r.LastUsedAt))
		}
		line := fmt.Sprintf("%s  created %s, %s\n  %s\n  from: %s\n", printable(r.ID), day(r.CreatedAt), used, printable(r.Wording), printable(r.Summary))
		if r.Inactive != "" {
			line += "  Inactive: " + printable(r.Inactive) + "\n"
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	return nil
}
