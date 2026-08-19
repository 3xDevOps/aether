package main

import (
	"flag"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "workspace",
		short: "manage workspaces",
		run:   runWorkspace,
	})
}

func runWorkspace(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether workspace add <name> --image <image>")
	}
	switch args[0] {
	case "add":
		return workspaceAdd(args[1:])
	default:
		return fmt.Errorf("unknown workspace command %q", args[0])
	}
}

func workspaceAdd(args []string) error {
	fs := flag.NewFlagSet("workspace add", flag.ExitOnError)
	image := fs.String("image", "", "container image for runs")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" || *image == "" {
		return fmt.Errorf("usage: aether workspace add <name> --image <image>")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.WorkspaceAddResult
		if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{
			Name:  name,
			Image: *image,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("workspace %s %s\n", res.Workspace.ID, res.Workspace.Name)
		return nil
	})
}
