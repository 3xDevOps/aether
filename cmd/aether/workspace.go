package main

import (
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "workspace",
		short: "manage workspaces: init, add, import, delete, list, settings, origin, env, mirror",
		usage: "aether workspace init <name> [--base <branch>]\n" +
			"aether workspace add <name> [--base <branch>]\n" +
			"aether workspace import <path|url> [--name <name>]\n" +
			"aether workspace delete <name> --yes\n" +
			"aether workspace list\n" +
			"aether workspace settings [--workspace <name>] [--message-others everyone|admins-only] [--child-max-live <n>] [--child-max-depth <n>] [--child-max-total <n>]\n" +
			"aether workspace origin [--workspace <name>] [<url>|--clear]\n" +
			"aether workspace env list [--workspace <name>]\n" +
			"aether workspace env set [--workspace <name>] <NAME>=<value>...\n" +
			"aether workspace env set [--workspace <name>] --secret <NAME>\n" +
			"aether workspace env unset [--workspace <name>] <NAME>...\n" +
			"aether workspace env import [--workspace <name>] [--plain] <file|->\n" +
			"aether workspace env script [--workspace <name>] [--file <path|->|--clear]\n" +
			"aether workspace mirror status [--workspace <name>]\n" +
			"aether workspace mirror configure --source <url> [--workspace <name>] [--branch <branch>]\n" +
			"    [--auth public|deploy-key|github] [--github-account-id <id>] [--known-hosts-file <path>]\n" +
			"aether workspace mirror refresh --workspace <name>\n" +
			"aether workspace mirror adopt --workspace <name> --generation <n> --commit <sha> --yes\n" +
			"aether workspace mirror disable --workspace <name> --yes",
		example: "aether workspace list\n" +
			"aether workspace import ~/src/app\n" +
			"aether workspace import https://github.com/acme/app\n" +
			"aether workspace env set --workspace app API_URL=https://api.example.com\n" +
			"aether workspace env set --workspace app --secret DATABASE_PASSWORD < secret.txt\n" +
			"aether workspace mirror status --workspace app",
		run: runWorkspace,
	})
}

func runWorkspace(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether workspace <init|add|import|delete|list|settings|origin|env|mirror>")
	}
	switch args[0] {
	case "init":
		return workspaceInit(args[1:])
	case "add":
		return workspaceAdd(args[1:])
	case "import":
		return workspaceImport(args[1:])
	case "delete":
		return workspaceDelete(args[1:])
	case "list":
		return workspaceList(args[1:])
	case "settings":
		return workspaceSettings(args[1:])
	case "origin":
		return workspaceOrigin(args[1:])
	case "env":
		return workspaceEnv(args[1:])
	case "mirror":
		return workspaceMirror(args[1:])
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
	fs := newFlagSet("workspace init")
	base := fs.String("base", "", "branch new run worktrees are cut from (default main)")
	name, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\nusage: aether workspace init <name> [--base <branch>]", err)
	}
	if name == "" {
		return fmt.Errorf("usage: aether workspace init <name> [--base <branch>]")
	}
	return createWorkspace(workspaceCreateOptions{name: name, base: *base})
}

func workspaceAdd(args []string) error {
	fs := newFlagSet("workspace add")
	base := fs.String("base", "", "branch new run worktrees are cut from (default main)")
	name, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\nusage: aether workspace add <name> [--base <branch>]", err)
	}
	if name == "" {
		return fmt.Errorf("usage: aether workspace add <name> [--base <branch>]")
	}
	return createWorkspace(workspaceCreateOptions{name: name, base: *base})
}

// createWorkspace is the wire call shared by init and add.
func createWorkspace(opts workspaceCreateOptions) error {
	return withControl(func(c *protocol.Client) error {
		var list protocol.WorkspaceListResult
		if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			return err
		}
		if ws, ok := workspaceNamed(list.Workspaces, opts.name); ok {
			return fmt.Errorf("workspace %q already exists as %s; choose another name", opts.name, ws.ID)
		}
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

func workspaceNamed(list []protocol.Workspace, name string) (protocol.Workspace, bool) {
	for _, ws := range list {
		if ws.Name == name {
			return ws, true
		}
	}
	return protocol.Workspace{}, false
}

func workspaceDelete(args []string) error {
	fs := newFlagSet("workspace delete")
	yes := fs.Bool("yes", false, "confirm permanent deletion of the workspace and all its data")
	name, err := parseLeadingArg(fs, args)
	if err != nil {
		return fmt.Errorf("%w\nusage: aether workspace delete <name-or-id> --yes", err)
	}
	if name == "" {
		return fmt.Errorf("usage: aether workspace delete <name-or-id> --yes")
	}
	if !*yes {
		return fmt.Errorf("--yes is required because workspace deletion permanently removes its runs, repository and data")
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, name, false)
		if err != nil {
			return err
		}
		var result protocol.WorkspaceDeleteResult
		if err := c.Call(protocol.MethodWorkspaceDelete, protocol.WorkspaceDeleteParams{WorkspaceID: ws.ID}, &result); err != nil {
			return err
		}
		fmt.Printf("deleted workspace %s %s\n", ws.ID, ws.Name)
		return nil
	})
}
