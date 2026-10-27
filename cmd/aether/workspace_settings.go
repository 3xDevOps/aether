package main

import (
	"flag"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// workspaceSettings implements `aether workspace settings`: without flags
// it shows the workspace's settings; --message-others changes the messaging
// policy over workspace.settings and --child-max-* the child-run limits
// over workspace.child_limits. The server limits both changes to admins.
func workspaceSettings(args []string) error {
	fs := newFlagSet("workspace settings")
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	messageOthers := fs.String("message-others", "", "who may message, control and kill other members' runs: everyone or admins-only")
	maxLive := fs.Int("child-max-live", 0, "child runs one tree may have busy at once; more wait in a queue")
	maxDepth := fs.Int("child-max-depth", 0, "how deep child runs may nest below the run that started the tree")
	maxTotal := fs.Int("child-max-total", 0, "child runs one tree may start in all")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\nusage: aether workspace settings [--workspace <name-or-id>] [--message-others everyone|admins-only] [--child-max-live <n>] [--child-max-depth <n>] [--child-max-total <n>]", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether workspace settings [--workspace <name-or-id>] [--message-others everyone|admins-only] [--child-max-live <n>] [--child-max-depth <n>] [--child-max-total <n>]")
	}
	change := false
	limits := protocol.WorkspaceChildLimitsParams{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "message-others":
			change = true
		case "child-max-live":
			limits.MaxLive = maxLive
		case "child-max-depth":
			limits.MaxDepth = maxDepth
		case "child-max-total":
			limits.MaxTotal = maxTotal
		}
	})
	policy, err := parseMessageOthers(*messageOthers)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.WorkspaceListResult
		if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			return err
		}
		wsID, err := pickWorkspace(list.Workspaces, *workspace)
		if err != nil {
			return err
		}
		ws, ok := workspaceByID(list.Workspaces, wsID)
		if !ok {
			return fmt.Errorf("workspace %q not found", wsID)
		}
		if change {
			var res protocol.WorkspaceSettingsResult
			if err := c.Call(protocol.MethodWorkspaceSettings, protocol.WorkspaceSettingsParams{
				WorkspaceID: wsID,
				SteerOthers: policy,
			}, &res); err != nil {
				return err
			}
			ws = res.Workspace
		}
		limits.WorkspaceID = wsID
		var child protocol.ChildLimits
		if err := c.Call(protocol.MethodWorkspaceChildLimits, limits, &child); err != nil {
			return err
		}
		printWorkspaceSettings(ws)
		fmt.Printf("child runs      %d busy at once per tree (more queue), %d levels deep, %d per tree in all\n", child.MaxLive, child.MaxDepth, child.MaxTotal)
		return nil
	})
}

func workspaceByID(list []protocol.Workspace, id string) (protocol.Workspace, bool) {
	for _, ws := range list {
		if ws.ID == id {
			return ws, true
		}
	}
	return protocol.Workspace{}, false
}

// parseMessageOthers maps the flag's spellings to the wire value: "" is the
// permissive default on the wire, so "everyone" has to be spelled out by
// the user and an empty flag means "no change".
func parseMessageOthers(v string) (string, error) {
	switch v {
	case "", "everyone":
		return "", nil
	case "admins-only", domain.SteerOthersAdminsOnly:
		return domain.SteerOthersAdminsOnly, nil
	default:
		return "", fmt.Errorf("invalid --message-others %q: want everyone or admins-only", v)
	}
}

// describeMessageOthers spells a wire value out the way the flag takes it,
// with what it means in the same breath.
func describeMessageOthers(v string) string {
	if v == domain.SteerOthersAdminsOnly {
		return "admins-only (only a run's owner and admins may message, control or kill it)"
	}
	return "everyone (collaborators may message, control and kill each other's runs)"
}

func printWorkspaceSettings(ws protocol.Workspace) {
	fmt.Printf("workspace %s %s\n", ws.ID, ws.Name)
	fmt.Printf("base branch     %s\n", ws.BaseBranch)
	fmt.Printf("message others  %s\n", describeMessageOthers(ws.SteerOthers))
}
