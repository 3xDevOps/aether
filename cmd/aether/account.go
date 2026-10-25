package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "account",
		short: "share agent accounts, list accounts available to use, and show their vendor usage",
		run:   runAccount,
	})
}

func runAccount(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: aether account <list|usage|share|revoke>")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: aether account list")
		}
		return accountList()
	case "usage":
		jsonOut := len(args) == 2 && args[1] == "--json"
		if len(args) != 1 && !jsonOut {
			return fmt.Errorf("usage: aether account usage [--json]")
		}
		return accountUsage(jsonOut)
	case "share", "revoke":
		if len(args) != 2 {
			return fmt.Errorf("usage: aether account %s <member-id>", args[0])
		}
		return accountChange(args[0], args[1])
	default:
		return fmt.Errorf("unknown account command %q", args[0])
	}
}

func accountList() error {
	return withControl(func(c *protocol.Client) error {
		var result protocol.AccountListResult
		if err := c.Call(protocol.MethodAccountList, struct{}{}, &result); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "AVAILABLE ACCOUNT\tMEMBER ID")
		for _, member := range result.Accounts {
			_, _ = fmt.Fprintf(tw, "%s\t%s\n", member.DisplayName, member.ID)
		}
		_, _ = fmt.Fprintln(tw, "\nYOUR ACCOUNT IS SHARED WITH\tMEMBER ID")
		for _, member := range result.SharedWith {
			_, _ = fmt.Fprintf(tw, "%s\t%s\n", member.DisplayName, member.ID)
		}
		return tw.Flush()
	})
}

func accountUsage(jsonOut bool) error {
	return withControl(func(c *protocol.Client) error {
		var result protocol.AccountUsageResult
		if err := c.Call(protocol.MethodAccountUsage, protocol.AccountUsageParams{}, &result); err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "PROVIDER\tSTATUS\tDETAIL")
		for _, p := range result.Providers {
			used := make([]string, 0, len(p.Windows))
			for _, w := range p.Windows {
				used = append(used, fmt.Sprintf("%s: %.0f%% used", w.Label, w.UsedPercent))
			}
			detail := strings.Join(used, ", ")
			if p.Error != "" {
				detail = strings.TrimPrefix(detail+"; "+p.Error, "; ")
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Provider, p.Status, detail)
		}
		return tw.Flush()
	})
}

func accountChange(action, member string) error {
	method := protocol.MethodAccountShare
	verb := "shared with"
	if action == "revoke" {
		method = protocol.MethodAccountRevoke
		verb = "revoked from"
	}
	return withControl(func(c *protocol.Client) error {
		if err := c.Call(method, protocol.AccountMemberParams{MemberID: member}, nil); err != nil {
			return err
		}
		fmt.Printf("account access %s %s\n", verb, member)
		return nil
	})
}
