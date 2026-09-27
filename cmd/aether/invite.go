package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "invite",
		short: "invite a GitHub or Google account through the edge, or mint a one-time invite code",
		run:   runInvite,
	})
}

func runInvite(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			if len(args) != 1 {
				return errors.New(inviteUsage)
			}
			return inviteList()
		case "revoke":
			if len(args) != 2 {
				return fmt.Errorf("usage: aether invite revoke <invitation-id>")
			}
			return withControl(func(c *protocol.Client) error {
				if err := c.Call(protocol.MethodMemberInvitationRevoke,
					protocol.MemberInvitationRevokeParams{InvitationID: args[1]}, nil); err != nil {
					return err
				}
				fmt.Printf("revoked invitation %s\n", args[1])
				return nil
			})
		}
	}
	params, ttl, err := parseInviteArgs(args)
	if err != nil {
		return err
	}
	if params == nil {
		return inviteCode(ttl)
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberInvitationResult
		if err := c.Call(protocol.MethodMemberInvitationCreate, params, &res); err != nil {
			return err
		}
		inv := res.Invitation
		fmt.Printf("invited %s as %s until %s (invitation %s)\n", inviteeOf(inv), inv.Role, inv.ExpiresAt, inv.ID)
		fmt.Println("they sign in with aether login, then find this server in aether servers")
		return nil
	})
}

const inviteUsage = "usage: aether invite [--ttl <seconds>]\n" +
	"       aether invite --github <login> | --email <address> [--provider github|google] [--role viewer|collaborator|admin]\n" +
	"       aether invite list | revoke <invitation-id>"

// parseInviteArgs reads an invite command line: the invitation of an edge
// account, or nil and the lifetime of a one-time invite code. A flag of
// the other kind is refused rather than ignored.
func parseInviteArgs(args []string) (*protocol.MemberInvitationCreateParams, int, error) {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	ttl := fs.Int("ttl", 86400, "lifetime of a one-time invite code in seconds")
	github := fs.String("github", "", "invite this GitHub login; it joins on its first connection through the edge")
	email := fs.String("email", "", "invite the account with this provider-verified email")
	provider := fs.String("provider", "", `with --email: accept only "github" or "google" (default either)`)
	role := fs.String("role", "collaborator", "role of an invited account: viewer, collaborator, or admin")
	if err := fs.Parse(args); err != nil {
		return nil, 0, err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case fs.NArg() != 0:
		return nil, 0, errors.New(inviteUsage)
	case *github == "" && *email == "":
		if set["provider"] || set["role"] {
			return nil, 0, errors.New("invite: --provider and --role apply to --github and --email; a one-time invite code always joins as a collaborator\n" + inviteUsage)
		}
		return nil, *ttl, nil
	case set["ttl"]:
		return nil, 0, errors.New("invite: --ttl applies to a one-time invite code; an account invitation expires after 7 days\n" + inviteUsage)
	}
	params := &protocol.MemberInvitationCreateParams{Login: *github, Email: *email, Provider: *provider, Role: *role}
	if *github != "" && *provider == "" {
		params.Provider = edgeproto.ProviderGitHub
	}
	return params, 0, nil
}

func inviteCode(ttl int) error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberInviteResult
		if err := c.Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{TTLSeconds: ttl}, &res); err != nil {
			return err
		}
		fmt.Println(res.Code)
		fmt.Fprintf(os.Stderr, "expires %s\n", res.ExpiresAt)
		return nil
	})
}

func inviteList() error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberInvitationListResult
		if err := c.Call(protocol.MethodMemberInvitationList, struct{}{}, &res); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tACCOUNT\tROLE\tLINKS MEMBER\tEXPIRES")
		for _, inv := range res.Invitations {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", inv.ID, inviteeOf(inv), inv.Role, inv.MemberID, inv.ExpiresAt)
		}
		return tw.Flush()
	})
}

// inviteeOf names the account an invitation is for.
func inviteeOf(inv protocol.Invitation) string {
	if inv.Login != "" {
		return "github:" + inv.Login
	}
	if inv.Provider != "" {
		return inv.Provider + ":" + inv.Email
	}
	return inv.Email
}
