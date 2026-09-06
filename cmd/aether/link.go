package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "link",
		short: "connect to a server and save local config",
		run:   runLink,
	})
}

// absolutePath resolves a path flag against the current directory, because
// the saved config is read again from wherever the next command runs.
func absolutePath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	return filepath.Abs(path)
}

// linkOptions is one parsed `aether link` command line. key is the resolved
// --key path, "auto", or empty (inherit the saved key).
type linkOptions struct {
	addr      string
	repo      string
	key       string
	invite    string
	name      string
	workspace string
}

func parseLinkArgs(args []string) (linkOptions, error) {
	fs := flag.NewFlagSet("link", flag.ExitOnError)
	invite := fs.String("invite", "", "one-time invite code")
	name := fs.String("name", "", "profile label for this link (also the display name when joining via invite)")
	repo := fs.String("repo", "", "local git repository to add the aether remote to")
	workspace := fs.String("workspace", "", "workspace name or id for the git remote")
	keyFlag := fs.String("key", "", "SSH private key to authenticate with (default ~/.ssh/id_ed25519); saved in the config for later commands")
	addr, err := parseLeadingArg(fs, args)
	if err != nil || addr == "" {
		return linkOptions{}, fmt.Errorf("usage: aether link <addr> [--invite] [--key] [--name] [--repo] [--workspace]")
	}
	repoPath, err := absolutePath(*repo)
	if err != nil {
		return linkOptions{}, err
	}
	key := *keyFlag
	if key != "" && key != cli.AutoKey {
		if key, err = filepath.Abs(key); err != nil {
			return linkOptions{}, err
		}
		// Fail on the path the user typed, before a handshake turns it into
		// an authentication failure that names no file.
		if _, err := os.Stat(key); err != nil {
			return linkOptions{}, fmt.Errorf("link --key: %w", err)
		}
	}
	return linkOptions{
		addr:      addr,
		repo:      repoPath,
		key:       key,
		invite:    *invite,
		name:      *name,
		workspace: *workspace,
	}, nil
}

func runLink(args []string) error {
	opts, err := parseLinkArgs(args)
	if err != nil {
		return err
	}
	prev, loadErr := cli.Load()
	if loadErr != nil {
		prev = cli.Config{}
	}
	result, err := cli.Link(cli.LinkOptions{
		Addr:   opts.addr,
		Key:    opts.key,
		Invite: opts.invite,
		Name:   opts.name,
	}, prev)
	if err != nil {
		return err
	}
	defer func() { _ = result.Conn.Close() }()
	cfg := result.Config
	cfg.Repo = opts.repo
	if opts.name != "" {
		for i := range cfg.Links {
			if cfg.Links[i].Name == opts.name {
				cfg.Links[i].Repo = cfg.Repo
				break
			}
		}
	}
	if err = cli.Save(cfg); err != nil {
		return err
	}
	who := result.Info.Member.DisplayName
	if term.IsTerminal(int(os.Stdout.Fd())) {
		who = attribution.Sprint(result.Info.Member.Color, who)
	}
	fmt.Printf("linked to %s as %s (%s)\n", cfg.Addr, who, result.Info.Member.Role)

	if cfg.Repo == "" {
		return nil
	}
	c, err := result.Conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	var wl protocol.WorkspaceListResult
	if callErr := c.Call(protocol.MethodWorkspaceList, struct{}{}, &wl); callErr != nil {
		return callErr
	}
	if len(wl.Workspaces) == 0 {
		fmt.Println("no workspace yet; skip git remote (re-run link --repo after workspace add)")
		return nil
	}
	wsID := opts.workspace
	if wsID == "" {
		if len(wl.Workspaces) > 1 {
			return fmt.Errorf("link --repo: multiple workspaces; pass --workspace <name-or-id>")
		}
		wsID = wl.Workspaces[0].ID
	} else {
		// The git remote URL must carry the workspace ID; sshd resolves
		// the pack path by ID only, so a name here would 128 every push.
		wsID, err = workspaceIDIn(wl.Workspaces, wsID)
		if err != nil {
			return err
		}
	}
	url := cli.GitURL(cfg.User, cfg.Addr, wsID)
	if err := localops.GitRemote(cfg.Repo, url, os.Stdout, os.Stderr); err != nil {
		return err
	}
	fmt.Printf("git remote aether -> %s\n", url)
	return nil
}
