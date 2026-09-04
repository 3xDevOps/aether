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
	name     string
	image    string
	base     string
	standard bool
}

func workspaceInit(args []string) error {
	fs := flag.NewFlagSet("workspace init", flag.ContinueOnError)
	image := fs.String("image", "", "custom container image (empty selects the server neutral image)")
	base := fs.String("base", "", "branch new run worktrees are cut from (default main)")
	standard := fs.Bool("standard", false, "use the server's recommended standard environment image")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" {
		return fmt.Errorf("usage: aether workspace init <name> [--standard | --image <image>] [--base <branch>]")
	}
	if *standard && *image != "" {
		return fmt.Errorf("aether workspace init: --standard and --image cannot be used together")
	}
	return createWorkspace(workspaceCreateOptions{name: name, image: *image, base: *base, standard: *standard})
}

func workspaceAdd(args []string) error {
	fs := flag.NewFlagSet("workspace add", flag.ContinueOnError)
	image := fs.String("image", "", "container image for runs")
	base := fs.String("base", "", "branch new run worktrees are cut from (default main)")
	standard := fs.Bool("standard", false, "use the server's recommended standard environment image")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" || (*image == "" && !*standard) {
		return fmt.Errorf("usage: aether workspace add <name> (--standard | --image <image>) [--base <branch>]")
	}
	if *standard && *image != "" {
		return fmt.Errorf("aether workspace add: --standard and --image cannot be used together")
	}
	return createWorkspace(workspaceCreateOptions{name: name, image: *image, base: *base, standard: *standard})
}

// createWorkspace is the one wire call behind init and add; the two differ
// only in whether an image is required. An empty base branch lets the
// server apply its default rather than the CLI guessing one. --standard
// asks the server for its recommended image first, so the workspace is
// created already pinned to that ref.
func createWorkspace(opts workspaceCreateOptions) error {
	return withControl(func(c *protocol.Client) error {
		var info protocol.ServerInfoResult
		if opts.standard {
			if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
				return fmt.Errorf("fetch server info for --standard: %w", err)
			}
		}
		env, err := createEnvironment(opts, info)
		if err != nil {
			return err
		}
		var res protocol.WorkspaceAddResult
		if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{
			Name:        opts.name,
			Environment: env,
			BaseBranch:  opts.base,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("workspace %s %s\n", res.Workspace.ID, res.Workspace.Name)
		return nil
	})
}

// createEnvironment shapes the workspace.add environment. --standard pins
// the ref server.info reported as a plain custom image, so the workspace
// records the ref itself and keeps it across server upgrades.
func createEnvironment(opts workspaceCreateOptions, info protocol.ServerInfoResult) (protocol.WorkspaceEnvironment, error) {
	if !opts.standard {
		return workspaceEnvironment(opts.image), nil
	}
	if info.StandardImage == "" {
		return protocol.WorkspaceEnvironment{}, fmt.Errorf("server does not report a standard image; upgrade aether-server or pass --image")
	}
	return protocol.WorkspaceEnvironment{CustomImage: info.StandardImage}, nil
}

func workspaceEnvironment(image string) protocol.WorkspaceEnvironment {
	if image == "" {
		return protocol.WorkspaceEnvironment{NeutralImage: true}
	}
	return protocol.WorkspaceEnvironment{CustomImage: image}
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
