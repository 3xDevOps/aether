package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "member",
		short: "list, approve, color, change the role of, or remove members",
		run:   runMember,
	})
}

func runMember(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether member <list|approve|color|role|remove>")
	}
	switch args[0] {
	case "list":
		return memberList()
	case "approve":
		if len(args) < 2 {
			return fmt.Errorf("usage: aether member approve <member-id>")
		}
		return memberApprove(args[1])
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: aether member remove <member-id>")
		}
		return memberRemove(args[1])
	case "color":
		return memberColor(args[1:])
	case "role":
		return memberRole(args[1:])
	default:
		return fmt.Errorf("unknown member command %q", args[0])
	}
}

func memberList() error {
	return withControl(func(c *protocol.Client) error {
		var ml protocol.MemberListResult
		if err := c.Call(protocol.MethodMemberList, struct{}{}, &ml); err != nil {
			return err
		}
		color := term.IsTerminal(int(os.Stdout.Fd()))
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tNAME\tROLE\tPENDING")
		for _, m := range ml.Members {
			pending := ""
			if m.Pending {
				pending = "pending"
			}
			name := m.DisplayName
			if color {
				name = attribution.Sprint(m.Color, name)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.ID, name, m.Role, pending)
		}
		return tw.Flush()
	})
}

func memberApprove(id string) error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberApproveResult
		if err := c.Call(protocol.MethodMemberApprove, protocol.MemberApproveParams{MemberID: id}, &res); err != nil {
			return err
		}
		fmt.Printf("approved %s %s\n", res.Member.ID, res.Member.DisplayName)
		return nil
	})
}

func memberRemove(id string) error {
	return withControl(func(c *protocol.Client) error {
		if err := c.Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: id}, nil); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", id)
		return nil
	})
}
