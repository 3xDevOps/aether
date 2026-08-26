package main

import (
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// resolveWorkspace maps a user-supplied workspace name or ID to the ID every
// scoped command sends on the wire. An empty input is the common case: one
// workspace resolves implicitly, so `--workspace` is only ever typed by
// someone who actually has more than one.
func resolveWorkspace(c *protocol.Client, idOrName string) (string, error) {
	var wl protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &wl); err != nil {
		return "", err
	}
	if idOrName == "" {
		switch len(wl.Workspaces) {
		case 0:
			return "", fmt.Errorf("no workspaces; create one with aether workspace init")
		case 1:
			return wl.Workspaces[0].ID, nil
		default:
			return "", fmt.Errorf("--workspace is required when more than one workspace exists")
		}
	}
	return workspaceIDIn(wl.Workspaces, idOrName)
}

// workspaceIDIn maps a workspace name or ID to the ID. Git remote URLs and
// wire calls carry IDs only; names are user-facing sugar.
//
// IDs win outright, and only then are names considered: names are not
// unique, so a name shared by two workspaces is an ambiguity the caller
// has to break with an ID rather than a coin flip over map order.
func workspaceIDIn(list []protocol.Workspace, idOrName string) (string, error) {
	for _, w := range list {
		if w.ID == idOrName {
			return w.ID, nil
		}
	}
	var matched []string
	for _, w := range list {
		if w.Name == idOrName {
			matched = append(matched, w.ID)
		}
	}
	switch len(matched) {
	case 0:
		return "", fmt.Errorf("workspace %q not found", idOrName)
	case 1:
		return matched[0], nil
	default:
		return "", fmt.Errorf("workspace name %q is ambiguous (%s); use the ID",
			idOrName, strings.Join(matched, ", "))
	}
}
