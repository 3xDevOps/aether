package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "member",
		short: "list, approve, color, set the git identity of, change the role of, link an edge account to, transfer server ownership to, or remove members",
		run:   runMember,
	})
}

func runMember(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether member <list|approve|color|git|role|link|transfer|remove>")
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

// memberLink binds a GitHub or Google account at the edge to your own
// member, which must be an admin: that account connects through the edge
// as you and can claim this server with a claim code.
func memberLink(args []string) error {
	fs := flag.NewFlagSet("member link", flag.ExitOnError)
	github := fs.String("github", "", "your GitHub login")
	email := fs.String("email", "", "your provider-verified email")
	provider := fs.String("provider", "", `with --email: accept only "github" or "google" (default either)`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*github == "") == (*email == "") || fs.NArg() != 0 {
		return fmt.Errorf("usage: aether member link --github <login> | --email <address> [--provider github|google]\nadmins only; anyone else is invited by an admin with aether invite --github or --email")
	}
	params := protocol.MemberIdentityLinkParams{Login: *github, Email: *email, Provider: *provider}
	if *github != "" && *provider == "" {
		params.Provider = edgeproto.ProviderGitHub
	}
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

// memberTransfer makes another admin the server's owner at its edge,
// through one of their edge identities. The server reports it to the
// edge; the edge records it only from that report.
func memberTransfer(args []string) error {
	fs := flag.NewFlagSet("member transfer", flag.ExitOnError)
	provider := fs.String("provider", "", `the new owner's edge identity to use, "github" or "google", when they have both`)
	memberID, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("usage: aether member transfer <member-id> [--provider github|google]\nadmins only; the new owner must already be an admin with a linked edge account")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.ServerOwnerTransferResult
		if err := c.Call(protocol.MethodServerOwnerTransfer, protocol.ServerOwnerTransferParams{MemberID: memberID, Provider: *provider}, &res); err != nil {
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
