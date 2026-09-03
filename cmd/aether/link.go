package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

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

// savedKey is the private key a previous `aether link` stored for the
// profile being relinked: the named profile's own key, or the top-level
// key for the default link. Relinking without --key keeps it, so a user
// who chose a key once and re-runs link --repo is not silently moved back
// to automatic discovery.
func savedKey(prev cli.Config, name string) string {
	if name == "" {
		return prev.Key
	}
	for _, l := range prev.Links {
		if l.Name == name {
			return l.Key
		}
	}
	return ""
}

// autoKey is the --key value that forgets a saved key. Without it a user
// who once chose a file could only return to agent and default-key
// discovery by editing config.json.
const autoKey = "auto"

// linkKey resolves the --key flag for this link: an explicit path is made
// absolute so the saved config works from any directory and checked
// before anything is dialed; an empty flag keeps the saved key; "auto"
// clears it so this link goes back to automatic discovery.
func linkKey(flag string, prev cli.Config, name string) (string, error) {
	switch flag {
	case "":
		return savedKey(prev, name), nil
	case autoKey:
		return "", nil
	}
	path, err := filepath.Abs(flag)
	if err != nil {
		return "", err
	}
	if err := cli.CheckKey(path); err != nil {
		return "", err
	}
	return path, nil
}

// linkConfig is the config `aether link` saves: the fresh link cfg
// carrying forward previously saved profiles - Save overwrites the whole
// file - plus, when name is non-empty, a snapshot of cfg upserted under
// that name. Without a name the top-level fields change exactly as before
// profiles existed.
func linkConfig(cfg, prev cli.Config, name string) cli.Config {
	cfg.Links = prev.Links
	if name == "" {
		return cfg
	}
	return cli.UpsertLink(cfg, cli.NamedLink{
		Name:       name,
		Addr:       cfg.Addr,
		User:       cfg.User,
		Key:        cfg.Key,
		Repo:       cfg.Repo,
		KnownHosts: cfg.KnownHosts,
	})
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
	name := fs.String("name", "", "profile label for this link (also the display name when joining via invite)")
	key := fs.String("key", "", "SSH private key file to use and remember for this link; \"auto\" forgets a saved key (default: ssh-agent, then ~/.ssh/id_ed25519, id_ecdsa, id_rsa)")
	repo := fs.String("repo", "", "local git repository to add the aether remote to")
	workspace := fs.String("workspace", "", "workspace name or id for the git remote")
	addr, err := parseLeadingArg(fs, args)
	if err != nil || addr == "" {
		return fmt.Errorf("usage: aether link <addr> [--invite] [--name] [--key] [--repo] [--workspace]")
	}
	repoPath, err := absoluteRepo(*repo)
	if err != nil {
		return err
	}
	prev, loadErr := cli.Load()
	if loadErr != nil {
		prev = cli.Config{}
	}
	keyPath, err := linkKey(*key, prev, *name)
	if err != nil {
		return err
	}
	cfg := cli.Config{Addr: normalizeAddr(addr), Repo: repoPath, User: "aether", Key: keyPath}
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

	if err = cli.Save(linkConfig(cfg, prev, *name)); err != nil {
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
	if callErr := c.Call(protocol.MethodWorkspaceList, struct{}{}, &wl); callErr != nil {
		return callErr
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
	if err := localops.GitRemote(cfg.Repo, url, os.Stdout, os.Stderr); err != nil {
		return err
	}
	fmt.Printf("git remote aether -> %s\n", url)
	return nil
}
