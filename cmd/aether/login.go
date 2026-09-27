package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/edgeclient"
	"github.com/3xDevOps/Aether/internal/edgeproto"
)

func init() {
	register(command{
		name:  "login",
		short: "sign in to an edge to reach servers without SSH or network setup",
		run:   runLogin,
	})
}

const edgeFlagUsage = "edge URL (default: the one edge you are signed in to, else " + edgeclient.DefaultURL + ")"

// edgeClient is the client for the --edge flag's value. Without one it is
// the only edge this machine is signed in to, or the project's edge.
func edgeClient(edgeURL string) (*edgeclient.Client, error) {
	dir, err := cli.Dir()
	if err != nil {
		return nil, err
	}
	return edgeclient.Choose(dir, edgeURL)
}

func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	edgeURL := fs.String("edge", "", edgeFlagUsage)
	label := fs.String("label", "", "name of this device on the edge's Devices page (default: this machine's host name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether login [--edge <url>] [--label <name>]")
	}
	client, err := edgeClient(*edgeURL)
	if err != nil {
		return err
	}
	if *label == "" {
		if *label, err = os.Hostname(); err != nil {
			return fmt.Errorf("login: name this device with --label: %w", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	login, err := client.StartLogin(ctx, *label)
	if err != nil {
		return err
	}
	fmt.Printf("open %s and enter the code %s\n", login.VerificationURI, login.UserCode)
	openBrowser(login.VerificationURI)
	fmt.Println("waiting for you to confirm the code...")
	session, err := client.Wait(ctx, login)
	if err != nil {
		return err
	}
	fmt.Printf("signed in to %s as %s; this device is %q\n", client.Host(), accountName(session.Account), session.Device.Label)
	return nil
}

// accountName is how a person recognizes their own account.
func accountName(a edgeproto.Account) string {
	switch {
	case a.Login != "":
		return a.Login + " (" + a.Provider + ")"
	case a.Email != "":
		return a.Email + " (" + a.Provider + ")"
	default:
		return a.Provider + " user " + a.Subject
	}
}
