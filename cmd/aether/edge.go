package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "edge",
		short: "connect your GitHub account to the linked server's edge",
		run:   runEdge,
	})
}

const edgeUsage = "usage: aether edge connect"

func runEdge(args []string) error {
	if len(args) != 1 || args[0] != "connect" {
		return errors.New(edgeUsage)
	}
	var code, edgeURL, edgeHost, account string
	err := withControl(func(c *protocol.Client) error {
		var edge protocol.ServerEdgeResult
		if err := c.Call(protocol.MethodServerEdge, struct{}{}, &edge); err != nil {
			return err
		}
		done, err := edgeConnected(edge)
		if err != nil || done != "" {
			fmt.Print(done)
			return err
		}
		client, err := edgeClient(edge.EdgeURL)
		if err != nil {
			return err
		}
		session, err := client.Session()
		if err != nil {
			return fmt.Errorf("edge connect through %s: %w", client.Host(), err)
		}
		edgeHost, account = client.Host(), accountName(session.Account.Account)
		fmt.Printf("connecting server %s as %s, the account %s reported when this device signed in\n", edge.ServerID, account, edgeHost)
		var res protocol.ServerEdgeClaimCodeResult
		if err := c.Call(protocol.MethodServerEdgeClaimCode, struct{}{}, &res); err != nil {
			return err
		}
		code, edgeURL = res.Code, client.URL()
		return nil
	})
	if err != nil || code == "" {
		return err
	}
	prev, err := cli.Load()
	if err != nil {
		return err
	}
	result, err := cli.Link(cli.LinkOptions{Addr: prev.Addr, EdgeURL: edgeURL, Claim: code}, prev)
	if err != nil {
		return err
	}
	defer func() { _ = result.Conn.Close() }()
	cfg := result.Config
	if err = cli.Save(cfg); err != nil {
		return err
	}
	who := result.Info.Member.DisplayName
	if term.IsTerminal(int(os.Stdout.Fd())) {
		who = attribution.Sprint(result.Info.Member.Color, who)
	}
	fmt.Printf("connected server %s to %s as %s; this device is approved\n", cfg.ServerID, edgeHost, account)
	fmt.Printf("linked to %s as %s (%s)\n", linkTarget(cfg), who, result.Info.Member.Role)
	url, err := localops.MoveRemote(prev, cfg, os.Stdout, os.Stderr)
	if err != nil {
		return fmt.Errorf("the link is saved, but the clone's aether remote was not updated: %w", err)
	}
	if url != "" {
		fmt.Printf("git remote aether -> %s\n", url)
	}
	return nil
}

func edgeConnected(edge protocol.ServerEdgeResult) (string, error) {
	switch {
	case edge.EdgeURL == "":
		return "", errors.New("the server's edge is off: edge-url is empty on the server")
	case edge.Owner != nil:
		return fmt.Sprintf("this server is already connected to %s as %s %s\n", edge.EdgeURL, edge.Owner.Provider, edge.Owner.Login), nil
	}
	return "", nil
}
