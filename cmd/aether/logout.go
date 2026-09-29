package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

func init() {
	register(command{
		name:  "logout",
		short: "revoke this device's edge sign-in, or delete the edge account",
		run:   runLogout,
	})
}

func runLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ExitOnError)
	edgeURL := fs.String("edge", "", edgeFlagUsage)
	deleteAccount := fs.Bool("delete-account", false, "delete the signed-in account at the edge, after listing what it touches and asking you to type its name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether logout [--edge <url>] [--delete-account]")
	}
	client, err := edgeClient(*edgeURL)
	if err != nil {
		return err
	}
	if *deleteAccount {
		return deleteEdgeAccount(context.Background(), client, os.Stdin, os.Stdout)
	}
	if err := client.Logout(context.Background()); err != nil {
		return err
	}
	fmt.Printf("signed out of %s: the device token is revoked at the edge and deleted here; the device key and your links stay\n", client.Host())
	return nil
}

// deleteEdgeAccount shows what deleting the signed-in account touches,
// asks for its login or email, typed, and deletes it.
func deleteEdgeAccount(ctx context.Context, client *edgeclient.Client, in io.Reader, out io.Writer) error {
	sum, err := client.Account(ctx)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "account %s at %s\n", accountName(sum.Account.Account), client.Host())
	printServers(out, "servers it owns (each becomes ownerless: transfer first with `aether member transfer <member id>` on that server)", sum.Owned)
	printServers(out, "servers it is a member of", sum.Member)
	_, _ = fmt.Fprint(out, "deleting it ends its sign-ins and device tokens, and each server above removes this account's identity and\n"+
		"its edge devices. No server loses a member, a role or data: an SSH key or tailnet identity of yours keeps\n"+
		"working there until an admin removes it. A server left without an owner is claimed again with a code from\n"+
		"`aether-server edge claim-code` on its machine.\n")
	_, _ = fmt.Fprintf(out, "type %s to delete the account: ", sum.Confirm)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("delete account: no confirmation read: %w", err)
	}
	if strings.TrimSpace(line) == "" {
		return fmt.Errorf("delete account: cancelled")
	}
	if err := client.DeleteAccount(ctx, line); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "deleted account %s at %s; this machine's token for it is deleted\n", accountName(sum.Account.Account), client.Host())
	return nil
}

func printServers(out io.Writer, title string, servers []edgeproto.ServerInfo) {
	_, _ = fmt.Fprintf(out, "%s:", title)
	if len(servers) == 0 {
		_, _ = fmt.Fprintln(out, " none")
		return
	}
	_, _ = fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, s := range servers {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", s.Name, s.ID, s.Role)
	}
	_ = tw.Flush()
}
