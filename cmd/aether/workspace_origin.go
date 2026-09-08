package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// originChange is one parsed `aether workspace origin` command line: the
// workspace to act on, and whether the URL is a change or only a read.
type originChange struct {
	workspace string
	origin    string
	set       bool
}

func parseWorkspaceOriginArgs(args []string) (originChange, error) {
	fs := flag.NewFlagSet("workspace origin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	clear := fs.Bool("clear", false, "forget the workspace's upstream")
	usage := fmt.Errorf("usage: aether workspace origin [--workspace <name-or-id>] [<url>|--clear]")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 {
		return originChange{}, usage
	}
	change := originChange{workspace: *workspace, origin: fs.Arg(0), set: fs.NArg() == 1}
	if *clear {
		if change.set {
			return originChange{}, usage
		}
		change.set = true
	}
	return change, nil
}

// workspaceOrigin implements `aether workspace origin`: without a URL it
// shows where the workspace's runs push, with one it records it, and
// --clear forgets it.
func workspaceOrigin(args []string) error {
	change, err := parseWorkspaceOriginArgs(args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.WorkspaceListResult
		if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			return err
		}
		wsID, err := pickWorkspace(list.Workspaces, change.workspace)
		if err != nil {
			return err
		}
		ws, ok := workspaceByID(list.Workspaces, wsID)
		if !ok {
			return fmt.Errorf("workspace %q not found", wsID)
		}
		if change.set {
			var res protocol.WorkspaceOriginResult
			if err := c.Call(protocol.MethodWorkspaceOrigin, protocol.WorkspaceOriginParams{
				WorkspaceID: wsID,
				Origin:      change.origin,
			}, &res); err != nil {
				return err
			}
			ws = res.Workspace
		}
		printWorkspaceOrigin(ws)
		return nil
	})
}

func printWorkspaceOrigin(ws protocol.Workspace) {
	fmt.Printf("workspace %s %s\n", ws.ID, ws.Name)
	if ws.Origin == "" {
		fmt.Println("origin (none)")
		return
	}
	fmt.Printf("origin %s\n", ws.Origin)
}
