package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "invite",
		short: "invite a GitHub account through the edge, or mint a one-time invite code",
		usage: "aether invite [--ttl <seconds>] [--workspace <workspace>]...\n" +
			"aether invite --github <login> [--role <admin|collaborator|viewer>] [--workspace <workspace>]...\n" +
			"aether invite --email <address> [--role <admin|collaborator|viewer>] [--workspace <workspace>]...\n" +
			"aether invite list\n" +
			"aether invite revoke <invitation-id>",
		example: "aether invite\n" +
			"aether invite --workspace web\n" +
			"aether invite --github alice --role collaborator --workspace web --workspace api\n" +
			"aether invite list",
		flags: true,
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
	params, ttl, workspaces, err := parseInviteArgs(args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		ids, err := resolveWorkspaces(c, workspaces)
		if err != nil {
			return err
		}
		if params == nil {
			return inviteCode(c, ttl, ids)
		}
		params.WorkspaceIDs = ids
		var res protocol.MemberInvitationResult
		if err := c.Call(protocol.MethodMemberInvitationCreate, params, &res); err != nil {
			return err
		}
		inv := res.Invitation
		printInvitationLink(res)
		fmt.Println("send them the link; they open it, sign in with GitHub and join")
		fmt.Println("Under edge-access approved-devices, the default, their device waits until an admin approves it from the dashboard's Needs you card")
		fmt.Printf("invited %s as %s until %s (invitation %s)\n", inviteeOf(inv), inv.Role, inv.ExpiresAt, inv.ID)
		return nil
	})
}

const inviteUsage = "usage: aether invite [--ttl <seconds>]\n" +
	"       aether invite --github <login> | --email <address> [--role admin|collaborator|viewer]\n" +
	"       aether invite ... [--workspace <workspace>]...\n" +
	"       aether invite list | revoke <invitation-id>"

// parseInviteArgs reads an invite command line: the invitation of an edge
// account, or nil and the lifetime of a one-time invite code. A flag of
// the other kind is refused rather than ignored. The workspaces are the
// names or ids typed, in order, for the caller to resolve.
func parseInviteArgs(args []string) (*protocol.MemberInvitationCreateParams, int, []string, error) {
	fs := newFlagSet("invite")
	ttl := fs.Int("ttl", 86400, "lifetime of a one-time invite code in seconds")
	github := fs.String("github", "", "invite this GitHub login; prints the link they join with")
	email := fs.String("email", "", "invite the GitHub account whose verified primary email this is")
	role := fs.String("role", "collaborator", "role of an invited account: admin, collaborator, or viewer")
	var workspaces stringList
	fs.Var(&workspaces, "workspace", "workspace the invited member is granted, by name or id (repeatable)")
	if err := fs.Parse(args); err != nil {
		return nil, 0, nil, err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case fs.NArg() != 0:
		return nil, 0, nil, errors.New(inviteUsage)
	case *github == "" && *email == "":
		if set["role"] {
			return nil, 0, nil, errors.New("invite: --role applies to --github and --email; a one-time invite code always joins as a collaborator\n" + inviteUsage)
		}
		return nil, *ttl, workspaces, nil
	case set["ttl"]:
		return nil, 0, nil, errors.New("invite: --ttl applies to a one-time invite code; an account invitation expires after 7 days\n" + inviteUsage)
	}
	return &protocol.MemberInvitationCreateParams{Login: *github, Email: *email, Role: *role}, 0, workspaces, nil
}

func inviteCode(c *protocol.Client, ttl int, workspaceIDs []string) error {
	var res protocol.MemberInviteResult
	if err := c.Call(protocol.MethodMemberInvite, protocol.MemberInviteParams{TTLSeconds: ttl, WorkspaceIDs: workspaceIDs}, &res); err != nil {
		return err
	}
	fmt.Println(res.Code)
	fmt.Fprintf(os.Stderr, "expires %s\n", res.ExpiresAt)
	return nil
}

// resolveWorkspaces maps the workspace names or ids of --workspace flags to
// ids.
func resolveWorkspaces(c *protocol.Client, idsOrNames []string) ([]string, error) {
	if len(idsOrNames) == 0 {
		return nil, nil
	}
	var wl protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &wl); err != nil {
		return nil, err
	}
	ids := make([]string, len(idsOrNames))
	for i, n := range idsOrNames {
		id, err := workspaceIDIn(wl.Workspaces, n)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

func inviteList() error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.MemberInvitationListResult
		if err := c.Call(protocol.MethodMemberInvitationList, struct{}{}, &res); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tACCOUNT\tROLE\tWORKSPACES\tLINKS MEMBER\tEXPIRES")
		for _, inv := range res.Invitations {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", inv.ID, inviteeOf(inv), inv.Role, strings.Join(inv.WorkspaceIDs, ","), inv.MemberID, inv.ExpiresAt)
		}
		return tw.Flush()
	})
}

// printInvitationLink prints the join link of a new invitation, the only
// copy of its secret, and the server id it names.
func printInvitationLink(res protocol.MemberInvitationResult) {
	fmt.Printf("invite link: %s\nserver id: %s\n", res.Link, res.ServerID)
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
