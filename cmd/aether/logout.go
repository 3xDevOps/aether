package main

import (
	"context"
	"flag"
	"fmt"
)

func init() {
	register(command{
		name:  "logout",
		short: "revoke this device's edge sign-in",
		run:   runLogout,
	})
}

func runLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ExitOnError)
	edgeURL := fs.String("edge", "", edgeFlagUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether logout [--edge <url>]")
	}
	client, err := edgeClient(*edgeURL)
	if err != nil {
		return err
	}
	if err := client.Logout(context.Background()); err != nil {
		return err
	}
	fmt.Printf("signed out of %s; its device token is revoked\n", client.Host())
	return nil
}
