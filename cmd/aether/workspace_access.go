package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// memberNames maps member ids to display names for output.
func memberNames(c *protocol.Client) (map[string]string, error) {
	var ml protocol.MemberListResult
	if err := c.Call(protocol.MethodMemberList, struct{}{}, &ml); err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	names := make(map[string]string, len(ml.Members))
	for _, m := range ml.Members {
		names[m.ID] = m.DisplayName
	}
	return names, nil
}

func nameOrID(names map[string]string, id string) string {
	if n := names[id]; n != "" {
		return n
	}
	return id
}

func workspaceAccess(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: aether workspace access <workspace>")
	}
	return withControl(func(c *protocol.Client) error {
		id, err := resolveWorkspace(c, args[0])
		if err != nil {
			return err
		}
		var res protocol.WorkspaceAccessResult
		if err := c.Call(protocol.MethodWorkspaceAccessList, protocol.WorkspaceAccessParams{WorkspaceID: id}, &res); err != nil {
			return err
		}
		names, err := memberNames(c)
		if err != nil {
			return err
		}
		return printWorkspaceAccess(os.Stdout, res, names)
	})
}

func printWorkspaceAccess(w io.Writer, res protocol.WorkspaceAccessResult, names map[string]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MEMBER\tNAME\tACCESS\tGRANTED BY\tGRANTED")
	_, _ = fmt.Fprintf(tw, "%s\t%s\towner\t\t\n", res.OwnerMemberID, nameOrID(names, res.OwnerMemberID))
	for _, g := range res.Grants {
		granter := ""
		if g.GrantedBy != "" {
			granter = nameOrID(names, g.GrantedBy)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\tgrant\t%s\t%s\n", g.MemberID, nameOrID(names, g.MemberID), granter, g.CreatedAt)
	}
	return tw.Flush()
}

func workspaceGrant(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: aether workspace grant <workspace> <member-id> [<member-id>...]")
	}
	return withControl(func(c *protocol.Client) error {
		id, err := resolveWorkspace(c, args[0])
		if err != nil {
			return err
		}
		if err := c.Call(protocol.MethodWorkspaceAccessGrant, protocol.WorkspaceAccessGrantParams{
			WorkspaceID: id,
			MemberIDs:   args[1:],
		}, nil); err != nil {
			return err
		}
		for _, m := range args[1:] {
			fmt.Printf("granted %s workspace %s\n", m, id)
		}
		return nil
	})
}

func workspaceRevoke(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: aether workspace revoke <workspace> <member-id>")
	}
	return withControl(func(c *protocol.Client) error {
		id, err := resolveWorkspace(c, args[0])
		if err != nil {
			return err
		}
		if err := c.Call(protocol.MethodWorkspaceAccessRevoke, protocol.WorkspaceAccessRevokeParams{
			WorkspaceID: id,
			MemberID:    args[1],
		}, nil); err != nil {
			return err
		}
		fmt.Printf("revoked %s from workspace %s\n", args[1], id)
		return nil
	})
}

func workspaceOwner(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: aether workspace owner <workspace> <member-id>")
	}
	return withControl(func(c *protocol.Client) error {
		id, err := resolveWorkspace(c, args[0])
		if err != nil {
			return err
		}
		if err := c.Call(protocol.MethodWorkspaceOwnerSet, protocol.WorkspaceOwnerSetParams{
			WorkspaceID: id,
			MemberID:    args[1],
		}, nil); err != nil {
			return err
		}
		fmt.Printf("workspace %s is now owned by %s\n", id, args[1])
		return nil
	})
}
