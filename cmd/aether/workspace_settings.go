package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// workspaceSettings implements `aether workspace settings`: without flags
// it shows the workspace's settings; --steer-others changes the steering
// policy over workspace.settings, and --image changes the container image
// over workspace.image. Both methods are limited to admins by the server.
func workspaceSettings(args []string) error {
	fs := flag.NewFlagSet("workspace settings", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	steer := fs.String("steer-others", "", "who may steer and kill other members' runs: everyone or admins-only")
	image := fs.String("image", "", "custom container image for runs")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("usage: aether workspace settings [--workspace <name-or-id>] [--steer-others everyone|admins-only] [--image <image>]")
	}
	change := false
	imageChange := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "steer-others":
			change = true
		case "image":
			imageChange = true
		}
	})
	policy, err := parseSteerOthers(*steer)
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
		if imageChange {
			var res protocol.WorkspaceImageResult
			if err := c.Call(protocol.MethodWorkspaceImage, protocol.WorkspaceImageParams{
				WorkspaceID: wsID,
				Image:       *image,
			}, &res); err != nil {
				return err
			}
		}
		printWorkspaceSettings(ws)
		var imageRes protocol.WorkspaceImageResult
		if err := c.Call(protocol.MethodWorkspaceImage, protocol.WorkspaceImageParams{
			WorkspaceID: wsID,
		}, &imageRes); err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) && pe.Code == protocol.CodeMethodNotFound {
				return nil
			}
			return err
		}
		return writeWorkspaceImage(os.Stdout, imageRes)
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

// parseSteerOthers maps the flag's spellings to the wire value: "" is the
// permissive default on the wire, so "everyone" has to be spelled out by
// the user and an empty flag means "no change".
func parseSteerOthers(v string) (string, error) {
	switch v {
	case "", "everyone":
		return "", nil
	case "admins-only", domain.SteerOthersAdminsOnly:
		return domain.SteerOthersAdminsOnly, nil
	default:
		return "", fmt.Errorf("invalid --steer-others %q: want everyone or admins-only", v)
	}
}

// describeSteerOthers spells a wire value out the way the flag takes it,
// with what it means in the same breath.
func describeSteerOthers(v string) string {
	if v == domain.SteerOthersAdminsOnly {
		return "admins-only (only a run's owner and admins may steer or kill it)"
	}
	return "everyone (collaborators may steer and kill each other's runs)"
}

func printWorkspaceSettings(ws protocol.Workspace) {
	fmt.Printf("workspace %s %s\n", ws.ID, ws.Name)
	fmt.Printf("base branch   %s\n", ws.BaseBranch)
	fmt.Printf("steer others  %s\n", describeSteerOthers(ws.SteerOthers))
}

func writeWorkspaceImage(w io.Writer, result protocol.WorkspaceImageResult) error {
	_, err := fmt.Fprintf(w, "image  %s\n", result.Image)
	return err
}
