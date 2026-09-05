package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "workspace",
		short: "manage workspaces: init, add, list, settings",
		run:   runWorkspace,
	})
}

func runWorkspace(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether workspace <init|add|list|settings>")
	}
	switch args[0] {
	case "init":
		return workspaceInit(args[1:])
	case "add":
		return workspaceAdd(args[1:])
	case "list":
		return workspaceList(args[1:])
	case "settings":
		return workspaceSettings(args[1:])
	default:
		return fmt.Errorf("unknown workspace command %q", args[0])
	}
}

func workspaceList(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: aether workspace list")
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.WorkspaceListResult
		if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			return err
		}
		return printWorkspaces(os.Stdout, list.Workspaces)
	})
}

// printWorkspaces emits one "workspace <id> <name>" line per workspace, the
// same shape init and add print, so IDs are easy to copy into git remotes.
func printWorkspaces(w io.Writer, workspaces []protocol.Workspace) error {
	if len(workspaces) == 0 {
		_, err := fmt.Fprintln(w, "no workspaces")
		return err
	}
	for _, ws := range workspaces {
		if _, err := fmt.Fprintf(w, "workspace %s %s\n", ws.ID, ws.Name); err != nil {
			return err
		}
	}
	return nil
}

type workspaceCreateOptions struct {
	name string
	base string
}

func workspaceInit(args []string) error {
	fs := flag.NewFlagSet("workspace init", flag.ContinueOnError)
	base := fs.String("base", "", "branch new run worktrees are cut from (default main)")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" {
		return fmt.Errorf("usage: aether workspace init <name> [--base <branch>]")
	}
	return createWorkspace(workspaceCreateOptions{name: name, base: *base})
}

func workspaceAdd(args []string) error {
	fs := flag.NewFlagSet("workspace add", flag.ContinueOnError)
	base := fs.String("base", "", "branch new run worktrees are cut from (default main)")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" {
		return fmt.Errorf("usage: aether workspace add <name> [--base <branch>]")
	}
	return createWorkspace(workspaceCreateOptions{name: name, base: *base})
}

// createWorkspace is the wire call shared by init and add.
func createWorkspace(opts workspaceCreateOptions) error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.WorkspaceAddResult
		if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{
			Name:        opts.name,
			Environment: protocol.WorkspaceEnvironment{},
			BaseBranch:  opts.base,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("workspace %s %s\n", res.Workspace.ID, res.Workspace.Name)
		return nil
	})
}

func resolveWorkspaceSelector(c *protocol.Client, input string) (protocol.WorkspaceSelector, error) {
	var list protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
		return protocol.WorkspaceSelector{}, err
	}
	if input == "" {
		switch len(list.Workspaces) {
		case 0:
			return protocol.WorkspaceSelector{}, fmt.Errorf("no workspaces available; specify --workspace")
		case 1:
			return protocol.WorkspaceSelector{ID: list.Workspaces[0].ID}, nil
		default:
			return protocol.WorkspaceSelector{}, fmt.Errorf("multiple workspaces available; specify --workspace")
		}
	}
	for _, workspace := range list.Workspaces {
		if workspace.ID == input || workspace.Name == input {
			return protocol.WorkspaceSelector{ID: workspace.ID}, nil
		}
	}
	return protocol.WorkspaceSelector{}, fmt.Errorf("workspace %q not found", input)
}
