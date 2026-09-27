package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
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
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tROLE\tONLINE")
	for _, s := range servers {
		online := "no"
		if s.Online {
			online = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.ID, s.Name, s.Role, online)
	}
	return tw.Flush()
}
