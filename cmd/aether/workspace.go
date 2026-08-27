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
		short: "manage workspaces: init, add, list, bootstrap, tools",
		run:   runWorkspace,
	})
}

func runWorkspace(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether workspace <init|add|list|bootstrap|tools>")
	}
	switch args[0] {
	case "init":
		return workspaceInit(args[1:])
	case "add":
		return workspaceAdd(args[1:])
	case "list":
		return workspaceList(args[1:])
	case "bootstrap":
		return workspaceBootstrap(args[1:])
	case "tools":
		return runWorkspaceTools(args[1:])
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

type workspaceBootstrapOptions struct {
	workspace string
	command   string
	resume    bool
	reset     bool
}

func parseWorkspaceBootstrap(args []string) (workspaceBootstrapOptions, error) {
	fs := flag.NewFlagSet("workspace bootstrap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	command := fs.String("command", "", "verification executable")
	resume := fs.Bool("resume", false, "resume pending bootstrap")
	reset := fs.Bool("reset", false, "discard pending bootstrap")
	workspace, err := parseLeadingArg(fs, args)
	if err != nil || workspace == "" {
		return workspaceBootstrapOptions{}, fmt.Errorf("usage: aether workspace bootstrap <workspace> [--command <executable>] [--resume] [--reset]")
	}
	if *resume && *reset {
		return workspaceBootstrapOptions{}, fmt.Errorf("aether workspace bootstrap: --resume and --reset cannot be used together")
	}
	return workspaceBootstrapOptions{workspace: workspace, command: *command, resume: *resume, reset: *reset}, nil
}

func workspaceBootstrap(args []string) error {
	opts, err := parseWorkspaceBootstrap(args)
	if err != nil {
		return err
	}
	return withResolvedWorkspace(opts.workspace, func(selector protocol.WorkspaceSelector) error {
		cols, rows := termSize()
		stream, err := openWorkspaceShell(protocol.WorkspaceShellRequest{
			Workspace:              selector,
			Mode:                   protocol.WorkspaceShellModeBootstrapTools,
			VerificationExecutable: opts.command,
			Resume:                 opts.resume,
			Reset:                  opts.reset,
			Cols:                   cols,
			Rows:                   rows,
		})
		if err != nil {
			return err
		}
		defer func() { _ = stream.Close() }()
		if err := copyRaw(stream); err != nil {
			return err
		}
		fmt.Println("User-local tools persist in the workspace. System packages and container filesystem changes do not. Configure credentials separately with aether setup.")
		return nil
	})
}

func workspaceInit(args []string) error {
	fs := flag.NewFlagSet("workspace init", flag.ContinueOnError)
	image := fs.String("image", "", "custom container image (empty selects the server neutral image)")
	base := fs.String("base", "", "branch new run worktrees are cut from (default main)")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" {
		return fmt.Errorf("usage: aether workspace init <name> [--image <image>] [--base <branch>]")
	}
	return createWorkspace(name, *image, *base)
}

func workspaceAdd(args []string) error {
	fs := flag.NewFlagSet("workspace add", flag.ContinueOnError)
	image := fs.String("image", "", "container image for runs")
	base := fs.String("base", "", "branch new run worktrees are cut from (default main)")
	name, err := parseLeadingArg(fs, args)
	if err != nil || name == "" || *image == "" {
		return fmt.Errorf("usage: aether workspace add <name> --image <image> [--base <branch>]")
	}
	return createWorkspace(name, *image, *base)
}

// createWorkspace is the one wire call behind init and add; the two differ
// only in whether an image is required. An empty base branch lets the
// server apply its default rather than the CLI guessing one.
func createWorkspace(name, image, base string) error {
	return withControl(func(c *protocol.Client) error {
		var res protocol.WorkspaceAddResult
		if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{
			Name:        name,
			Environment: workspaceEnvironment(image),
			BaseBranch:  base,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("workspace %s %s\n", res.Workspace.ID, res.Workspace.Name)
		return nil
	})
}

func workspaceEnvironment(image string) protocol.WorkspaceEnvironment {
	if image == "" {
		return protocol.WorkspaceEnvironment{NeutralImage: true}
	}
	return protocol.WorkspaceEnvironment{CustomImage: image}
}
func withResolvedWorkspace(input string, fn func(protocol.WorkspaceSelector) error) error {
	return withControl(func(c *protocol.Client) error {
		selector, err := resolveWorkspaceSelector(c, input)
		if err != nil {
			return err
		}
		return fn(selector)
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
