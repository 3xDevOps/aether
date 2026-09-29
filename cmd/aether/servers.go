package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

func init() {
	register(command{
		name:  "servers",
		short: "list the servers your edge account can link",
		run:   runServers,
	})
}

func runServers(args []string) error {
	fs := flag.NewFlagSet("servers", flag.ExitOnError)
	edgeURL := fs.String("edge", "", edgeFlagUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether servers [--edge <url>]")
	}
	client, err := edgeClient(*edgeURL)
	if err != nil {
		return err
	}
	servers, err := client.Servers(context.Background())
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		fmt.Printf("no servers yet on %s; claim one with: aether link --claim <code>\n", client.Host())
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tROLE\tONLINE\tKIND\tACCESS")
	for _, s := range servers {
		online := "no"
		if s.Online {
			online = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s: %s\n", s.ID, s.Name, s.Role, online, s.Kind, s.AccessPolicy, policyMeaning(s.AccessPolicy))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Println("link by the id the server's admin gave you, or that aether-server edge status printed: aether link <id>")
	fmt.Println("link from this list, which the edge supplies: aether link --from-edge <id>")
	return nil
}

// policyMeaning says in a few words what an access policy means for a
// device that connects for the first time.
func policyMeaning(p edgeproto.AccessPolicy) string {
	if p == edgeproto.PolicyAccount {
		return "signing in is enough"
	}
	return "a new device waits for approval"
}
