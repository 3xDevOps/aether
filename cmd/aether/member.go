package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "member",
		short: "list, approve, color, set the git identity of, change the role of, link, list or unlink the edge accounts of, transfer server ownership to, or remove members",
		run:   runMember,
	})
}

func runMember(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether member <list|approve|color|git|role|link|identities|unlink|transfer|remove>")
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
	case "git":
		return memberGit(args[1:])
	case "role":
		return memberRole(args[1:])
	case "link":
		return memberLink(args[1:])
	case "identities":
		if len(args) != 2 {
			return fmt.Errorf("usage: aether member identities <member-id>")
		}
		return memberIdentities(args[1])
	case "unlink":
		return memberUnlink(args[1:])
	case "transfer":
		return memberTransfer(args[1:])
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

// memberLink binds a GitHub account at the edge to your own member, which
// must be an admin: that account connects through the edge as you and can
// claim this server with a claim code.
func memberLink(args []string) error {
	fs := flag.NewFlagSet("member link", flag.ExitOnError)
	github := fs.String("github", "", "your GitHub login")
	email := fs.String("email", "", "the verified primary email of your GitHub account")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*github == "") == (*email == "") || fs.NArg() != 0 {
		return fmt.Errorf("usage: aether member link --github <login> | --email <address>\nadmins only; anyone else is invited by an admin with aether invite --github or --email")
	}
	params := protocol.MemberIdentityLinkParams{Login: *github, Email: *email}
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberInvitationResult
		if err := c.Call(protocol.MethodMemberIdentityLink, params, &res); err != nil {
			return err
		}
		fmt.Printf("%s can link to member %s until %s: sign in with aether login and connect through the edge\n",
			inviteeOf(res.Invitation), res.Invitation.MemberID, res.Invitation.ExpiresAt)
		return nil
	})
}

// memberIdentities lists a member's edge accounts and every device of the
// member with the account it signed in as.
func memberIdentities(id string) error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberIdentityListResult
		if err := c.Call(protocol.MethodMemberIdentityList, protocol.MemberIdentityListParams{MemberID: id}, &res); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "IDENTITY\tLOGIN\tEMAIL\tLINKED")
		for _, i := range res.Identities {
			_, _ = fmt.Fprintf(tw, "%s:%s\t%s\t%s\t%s\n", i.Provider, i.Subject, i.Login, i.Email, i.CreatedAt)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Println()
		return printDevices(res.Devices)
	})
}

// memberUnlink removes one edge account from a member: its devices are
// revoked and their connections closed. The member, its role, its other
// accounts, SSH key and tailnet identity stay.
func memberUnlink(args []string) error {
	const usage = "usage: aether member unlink <member-id> <provider>:<subject>\n" +
		"the identity as aether member identities prints it; the member or an admin"
	if len(args) != 2 {
		return errors.New(usage)
	}
	provider, subject, ok := strings.Cut(args[1], ":")
	if !ok || provider == "" || subject == "" {
		return errors.New(usage)
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberIdentityRemoveResult
		params := protocol.MemberIdentityRemoveParams{MemberID: args[0], Provider: provider, Subject: subject}
		if err := c.Call(protocol.MethodMemberIdentityRemove, params, &res); err != nil {
			return err
		}
		fmt.Printf("unlinked %s:%s from member %s; revoked %d device(s) that signed in with it\n", provider, subject, args[0], len(res.Revoked))
		for _, d := range res.Revoked {
			fmt.Printf("  %s %q %s\n", d.ID, d.Label, d.Fingerprint)
		}
		return nil
	})
}

// memberTransfer makes another admin the server's owner at its edge,
// through their GitHub identity. The server reports it to the edge; the
// edge records it only from that report.
func memberTransfer(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: aether member transfer <member-id>\nadmins only; the new owner must already be an admin with a linked GitHub account")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.ServerOwnerTransferResult
		if err := c.Call(protocol.MethodServerOwnerTransfer, protocol.ServerOwnerTransferParams{MemberID: args[0]}, &res); err != nil {
			return err
		}
		who := res.Login
		if who == "" {
			who = res.Email
		}
		if who == "" {
			who = res.Subject
		}
		fmt.Printf("member %s now owns this server at its edge, as %s account %s\n", res.MemberID, res.Provider, who)
		return nil
	})
}
