package main

import (
	"flag"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "session",
		short: "manage sessions",
		run:   runSession,
	})
}

func runSession(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether session new <name> --workspace <id> [--base]")
	}
	switch args[0] {
	case "new":
		return sessionNew(args[1:])
	default:
		return fmt.Errorf("unknown session command %q", args[0])
	}
}

func sessionNew(args []string) error {
	fs := flag.NewFlagSet("session new", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace ID")
	base := fs.String("base", "main", "base branch")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" || *workspace == "" {
		return fmt.Errorf("usage: aether session new <name> --workspace <id> [--base]")
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		var res protocol.SessionNewResult
		if err := c.Call(protocol.MethodSessionNew, protocol.SessionNewParams{
			WorkspaceID: wsID,
			Name:        name,
			BaseBranch:  *base,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("session %s %s\n", res.Session.ID, res.Session.Name)
		return nil
	})
}

func resolveWorkspace(c *protocol.Client, idOrName string) (string, error) {
	var wl protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &wl); err != nil {
		return "", err
	}
	for _, w := range wl.Workspaces {
		if w.ID == idOrName || w.Name == idOrName {
			return w.ID, nil
		}
	}
	return "", fmt.Errorf("workspace %q not found", idOrName)
}
