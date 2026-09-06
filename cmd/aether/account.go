package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "account",
		short: "share agent accounts and list accounts available to use",
		run:   runAccount,
	})
}

func runAccount(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: aether account <list|share|revoke>")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: aether account list")
		}
		return accountList()
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
