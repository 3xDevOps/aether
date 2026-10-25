package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

const workspaceImportUsage = "usage: aether workspace import <path|url> [--name <name>]"

func workspaceImport(args []string) error {
	fs := flag.NewFlagSet("workspace import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "workspace name (default: the clone's directory, or the repository's name)")
	source, err := parseLeadingArg(fs, args)
	if err != nil || source == "" {
		return errors.New(workspaceImportUsage)
	}
	if owner, repo, ok := githubRepository(source); ok {
		return importGitHub(owner, repo, *name)
	}
	if strings.Contains(source, "://") || strings.HasPrefix(source, "git@") {
		return fmt.Errorf("workspace import: %s is not a GitHub repository; from another Git host, run aether workspace add <name> --base <branch>, "+
			"then aether workspace mirror configure --workspace <name> --source <https or ssh URL> --auth public|deploy-key", source)
	}
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("workspace import: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace import: %s is not a directory", source)
	}
	return importClone(source, *name)
}

func importClone(path, name string) error {
	root, branch, err := localops.CloneHead(path)
	if err != nil {
		return fmt.Errorf("workspace import: %w", err)
	}
	if name == "" {
		name = filepath.Base(root)
	}
	remoteWorkspace, err := localops.RemoteWorkspace(root)
	if err != nil {
		return fmt.Errorf("workspace import: %w", err)
	}
	origin, err := localops.OriginURL(root)
	if err != nil {
		return fmt.Errorf("workspace import: %w", err)
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.WorkspaceListResult
		if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			return err
		}
		matched := workspacesWithOrigin(list.Workspaces, origin)
		if ws, ok := workspaceByID(list.Workspaces, remoteWorkspace); ok {
			matched = []protocol.Workspace{ws}
		}
		switch len(matched) {
		case 0:
		case 1:
			ws := matched[0]
			fmt.Printf("already added as workspace %s %s\n", ws.ID, ws.Name)
			if err := linkClone(cfg, ws.ID, root); err != nil {
				return err
			}
			if err := recordWorkspaceOrigin(c, list.Workspaces, ws.ID, root); err != nil {
				return err
			}
			var mirror protocol.WorkspaceMirrorResult
			if err := c.Call(protocol.MethodWorkspaceMirrorStatus, protocol.WorkspaceMirrorParams{WorkspaceID: ws.ID}, &mirror); err != nil {
				return err
			}
			if mirror.Enabled {
				printAdoptStep(os.Stdout, ws, mirror)
				return nil
			}
			tip, err := localops.WorkspaceTip(root, ws.BaseBranch)
			if err != nil || tip != "" {
				return err
			}
			return pushBase(root, ws)
		default:
			return fmt.Errorf("workspace import: workspaces %s all push to %s; link this clone to one of them: %s",
				workspaceIDs(matched), origin, linkRepoCommand(cfg, "<id>", root))
		}
		if ws, ok := workspaceNamed(list.Workspaces, name); ok {
			return fmt.Errorf("workspace import: workspace %q already exists as %s; link this clone to it: %s, or import it under another name with --name <name>",
				name, ws.ID, linkRepoCommand(cfg, ws.ID, root))
		}
		var res protocol.WorkspaceAddResult
		if err := c.Call(protocol.MethodWorkspaceAdd, protocol.WorkspaceAddParams{Name: name, BaseBranch: branch}, &res); err != nil {
			return adminRefusal(err, "; then run this again to link your clone")
		}
		ws := res.Workspace
		fmt.Printf("workspace %s %s\n", ws.ID, ws.Name)
		if err := linkClone(cfg, ws.ID, root); err != nil {
			return err
		}
		if err := recordWorkspaceOrigin(c, []protocol.Workspace{ws}, ws.ID, root); err != nil {
			return err
		}
		return pushBase(root, ws)
	})
}

// A mirrored workspace never gets here: its upstream owns the base.
func pushBase(root string, ws protocol.Workspace) error {
	if _, err := localops.Push(root, ws.BaseBranch); err != nil {
		return fmt.Errorf("workspace import: %w; push the base branch with: git -C %s push -u aether %s, or run this again",
			err, shellquote.Quote(root), shellquote.Quote(ws.BaseBranch))
	}
	fmt.Printf("pushed %s to workspace %s\n", ws.BaseBranch, ws.Name)
	return nil
}

func importGitHub(owner, repo, name string) error {
	source := "https://github.com/" + owner + "/" + repo + ".git"
	if name == "" {
		name = repo
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.WorkspaceListResult
		if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			return err
		}
		if matched := workspacesWithOrigin(list.Workspaces, source); len(matched) > 0 {
			for _, ws := range matched {
				fmt.Printf("already added as workspace %s %s\n", ws.ID, ws.Name)
				var mirror protocol.WorkspaceMirrorResult
				if err := c.Call(protocol.MethodWorkspaceMirrorStatus, protocol.WorkspaceMirrorParams{WorkspaceID: ws.ID}, &mirror); err != nil {
					return err
				}
				printAdoptStep(os.Stdout, ws, mirror)
			}
			return nil
		}
		if ws, ok := workspaceNamed(list.Workspaces, name); ok {
			return fmt.Errorf("workspace import: workspace %q already exists as %s; import under another name with --name <name>", name, ws.ID)
		}
		var res protocol.WorkspaceImportResult
		if err := c.Call(protocol.MethodWorkspaceImport, protocol.WorkspaceImportParams{
			Name: name, SourceURL: source, Origin: source, Auth: string(domain.MirrorAuthGitHub),
		}, &res); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "github") {
				return fmt.Errorf("%w; if your GitHub connection is not set up, run aether github connect, then import again", err)
			}
			return adminRefusal(err, "")
		}
		fmt.Printf("workspace %s %s\n", res.Workspace.ID, res.Workspace.Name)
		if res.Error != "" {
			return fmt.Errorf("workspace import: the workspace exists, but fetching %s failed: %s; fix that, then run: aether workspace mirror refresh --workspace %s",
				source, res.Error, res.Workspace.ID)
		}
		printAdoptStep(os.Stdout, res.Workspace, res.Mirror)
		return nil
	})
}

// An import never adopts the fetched commit on its own.
func printAdoptStep(w io.Writer, ws protocol.Workspace, m protocol.WorkspaceMirrorResult) {
	if m.Generation <= 0 || m.ObservedCommit == "" || m.ObservedCommit == m.AcceptedCommit {
		return
	}
	_, _ = fmt.Fprintf(w, "fetched %s at %s; review it, then make it the base with:\n  aether workspace mirror adopt --workspace %s --generation %d --commit %s --yes\n",
		m.Branch, m.ObservedCommit, ws.ID, m.Generation, m.ObservedCommit)
}

func adminRefusal(err error, then string) error {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr.Code == protocol.CodeDenied {
		return fmt.Errorf("%w; an admin must add the workspace: aether workspace import <path|url>%s", err, then)
	}
	return err
}

func workspacesWithOrigin(list []protocol.Workspace, origin string) []protocol.Workspace {
	if origin == "" {
		return nil
	}
	key := func(o string) string { return strings.TrimSuffix(domain.NormalizeOrigin(o), ".git") }
	var matched []protocol.Workspace
	for _, ws := range list {
		if ws.Origin != "" && key(ws.Origin) == key(origin) {
			matched = append(matched, ws)
		}
	}
	return matched
}

func workspaceIDs(list []protocol.Workspace) string {
	ids := make([]string, len(list))
	for i, ws := range list {
		ids[i] = ws.ID
	}
	return strings.Join(ids, ", ")
}
