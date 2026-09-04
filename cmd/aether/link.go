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

// absolutePath resolves a path flag against the current directory, because
// the saved config is read again from wherever the next command runs.
func absolutePath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	return filepath.Abs(path)
}

// linkOptions is one parsed `aether link` command line: the config the
// link will dial with and save, plus the flags that steer the run itself.
type linkOptions struct {
	cfg       cli.Config
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
	key := fs.String("key", "", "SSH private key to authenticate with (default ~/.ssh/id_ed25519); saved in the config for later commands")
	addr, err := parseLeadingArg(fs, args)
	if err != nil || addr == "" {
		return linkOptions{}, fmt.Errorf("usage: aether link <addr> [--invite] [--key] [--name] [--repo] [--workspace]")
	}
	repoPath, err := absolutePath(*repo)
	if err != nil {
		return linkOptions{}, err
	}
	keyPath, err := absolutePath(*key)
	if err != nil {
		return linkOptions{}, err
	}
	// Fail on the path the user typed, before a handshake turns it into
	// an authentication failure that names no file.
	if keyPath != "" {
		if _, err := os.Stat(keyPath); err != nil {
			return linkOptions{}, fmt.Errorf("link --key: %w", err)
		}
	}
	return linkOptions{
		cfg:       cli.Config{Addr: normalizeAddr(addr), Repo: repoPath, Key: keyPath, User: "aether"},
		invite:    *invite,
		name:      *name,
		workspace: *workspace,
	}, nil
}

// savedKey is the key path a re-link without --key keeps using: the one
// already stored for this profile, else the default link's. Named
// overlays the top-level fields, so an unset profile key falls back on
// its own.
func savedKey(prev cli.Config, name string) string {
	if name != "" {
		if named, ok := prev.Named(name); ok {
			return named.Key
		}
	}
	return prev.Key
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
	cfg := opts.cfg
	if cfg.Key == "" {
		cfg.Key = savedKey(prev, opts.name)
	}
	var conn *cli.Conn
	if opts.invite != "" {
		conn, err = cli.DialInvite(cfg, opts.invite, opts.name)
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

	if err = cli.Save(linkConfig(cfg, prev, opts.name)); err != nil {
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
