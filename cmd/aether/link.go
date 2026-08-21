package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "link",
		short: "connect to a server and save local config",
		run:   runLink,
	})
}

// defaultSSHPort is the aether-server default listen port, appended when
// linking by a bare MagicDNS hostname.
const defaultSSHPort = "2222"

// normalizeAddr gives a bare host the default aether-server port so the
// result is always the host:port that cli.Dial hands to the TCP dialer.
// Bare MagicDNS names, FQDNs, IPv4 and IPv6 literals (bracketed or not)
// all pick up ":2222"; anything that already carries a port passes
// through untouched.
func normalizeAddr(addr string) string {
	if addr == "" {
		return addr
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if port != "" {
			return addr
		}
		return net.JoinHostPort(host, defaultSSHPort)
	}
	// No port at all. Unwrap a matched IPv6 bracket pair so JoinHostPort
	// puts back exactly one pair.
	host := addr
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return net.JoinHostPort(host, defaultSSHPort)
}

func absoluteRepo(repo string) (string, error) {
	if repo == "" {
		return "", nil
	}
	return filepath.Abs(repo)
}

func runLink(args []string) error {
	fs := flag.NewFlagSet("link", flag.ExitOnError)
	invite := fs.String("invite", "", "one-time invite code")
	name := fs.String("name", "", "display name when joining via invite")
	repo := fs.String("repo", "", "local git repository to add the aether remote to")
	workspace := fs.String("workspace", "", "workspace name or id for the git remote")
	addr, err := parseLeadingArg(fs, args)
	if err != nil || addr == "" {
		return fmt.Errorf("usage: aether link <addr> [--invite] [--name] [--repo] [--workspace]")
	}
	repoPath, err := absoluteRepo(*repo)
	if err != nil {
		return err
	}
	cfg := cli.Config{Addr: normalizeAddr(addr), Repo: repoPath, User: "aether"}
	var conn *cli.Conn
	if *invite != "" {
		conn, err = cli.DialInvite(cfg, *invite, *name)
	} else {
		conn, err = cli.Dial(cfg)
	}
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	c, err := conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	var info protocol.ServerInfoResult
	if err = c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
		return err
	}
	if info.ProtocolVersion != protocol.Version {
		return fmt.Errorf("protocol version %q is not %q", info.ProtocolVersion, protocol.Version)
	}

	if err = cli.Save(cfg); err != nil {
		return err
	}
	who := info.Member.DisplayName
	if term.IsTerminal(int(os.Stdout.Fd())) {
		who = attribution.Sprint(info.Member.Color, who)
	}
	fmt.Printf("linked to %s as %s (%s)\n", cfg.Addr, who, info.Member.Role)

	if cfg.Repo == "" {
		return nil
	}
	var wl protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &wl); err != nil {
		return err
	}
	if len(wl.Workspaces) == 0 {
		fmt.Println("no workspace yet; skip git remote (re-run link --repo after workspace add)")
		return nil
	}
	wsID := *workspace
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
	if err := gitRemote(cfg.Repo, url); err != nil {
		return err
	}
	fmt.Printf("git remote aether -> %s\n", url)
	return nil
}

func gitRemote(repo, url string) error {
	out, err := exec.Command("git", "-C", repo, "remote").Output()
	if err != nil {
		return fmt.Errorf("git remote: %w", err)
	}
	has := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "aether" {
			has = true
			break
		}
	}
	args := []string{"-C", repo, "remote", "add", "aether", url}
	if has {
		args = []string{"-C", repo, "remote", "set-url", "aether", url}
	}
	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
