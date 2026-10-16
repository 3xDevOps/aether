package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/cli"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
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
// --key path, "auto", or empty (inherit the saved key). addr is the
// positional argument, or the --from-edge value: an address or a server
// id. join is the positional argument when it is an invitation's join
// link. claim, join, fromEdge, yes, edge and direct belong to edge links.
type linkOptions struct {
	addr      string
	repo      string
	key       string
	invite    string
	name      string
	workspace string
	claim     string
	join      string
	fromEdge  bool
	yes       bool
	edge      string
	direct    string
}

const linkUsage = "usage: aether link <addr | server id> [--invite] [--key] [--name] [--repo] [--workspace] [--edge] [--addr]\n" +
	"       aether link <join link> [--addr] [--name] [--repo] [--workspace]\n" +
	"       aether link --from-edge <server id> [--yes] [--edge] [--addr] [--name] [--repo] [--workspace]\n" +
	"       aether link --claim <code> [--edge] [--addr] [--name] [--repo] [--workspace]"

func parseLinkArgs(args []string) (linkOptions, error) {
	fs := flag.NewFlagSet("link", flag.ExitOnError)
	invite := fs.String("invite", "", "one-time invite code")
	name := fs.String("name", "", "profile label for this link (also the display name when joining via invite)")
	repo := fs.String("repo", "", "local git repository to add the aether remote to")
	workspace := fs.String("workspace", "", "workspace name or id for the git remote")
	keyFlag := fs.String("key", "", "SSH private key to authenticate with (default ~/.ssh/id_ed25519); saved in the config for later commands")
	claim := fs.String("claim", "", "claim code printed by aether-server setup; claims the server through the edge and links it")
	fromEdge := fs.String("from-edge", "", "server id from aether servers to link: shows its name and host key and asks before pinning it")
	yes := fs.Bool("yes", false, "with --from-edge: skip the confirmation, having compared the id with the one the server's admin gave you")
	edge := fs.String("edge", "", edgeFlagUsage)
	direct := fs.String("addr", "", "for an edge link: SSH address, host[:port], to try before the edge")
	addr, err := parseLeadingArg(fs, args)
	switch {
	case (*claim != "") != (*fromEdge != "") && addr == "" && fs.NArg() == 0:
		if *fromEdge != "" && !edgeproto.ValidServerID(*fromEdge) {
			return linkOptions{}, fmt.Errorf("link --from-edge: %q is not a server id; aether servers lists them", *fromEdge)
		}
		addr = *fromEdge
	case err != nil || addr == "" || *claim != "" || *fromEdge != "":
		return linkOptions{}, errors.New(linkUsage)
	}
	var join string
	if _, _, _, _, joinErr := edgeproto.ParseJoinLink(addr); joinErr == nil {
		if *invite != "" || *keyFlag != "" || *edge != "" {
			return linkOptions{}, errors.New("link: --invite, --key and --edge do not apply to a join link, which names its edge and authenticates with the device key from aether login")
		}
		join, addr = addr, ""
	}
	if *yes && *fromEdge == "" {
		return linkOptions{}, errors.New("link: --yes applies to --from-edge only")
	}
	if (*claim != "" || *fromEdge != "") && (*invite != "" || *keyFlag != "") {
		return linkOptions{}, fmt.Errorf("link: --invite and --key do not apply to an edge link, which authenticates with the device key from aether login")
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
		claim:     *claim,
		join:      join,
		fromEdge:  *fromEdge != "",
		yes:       *yes,
		edge:      *edge,
		direct:    *direct,
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
	linkOpts, err := edgeLinkOptions(opts)
	if err != nil {
		return err
	}
	if linkOpts.ServerID == "" && linkOpts.Claim == "" && linkOpts.Join == "" {
		if opts.direct != "" || opts.edge != "" {
			return fmt.Errorf("link: --edge and --addr apply to edge links, and %s is not a server id; aether servers lists them", opts.addr)
		}
		linkOpts = cli.LinkOptions{Addr: opts.addr, Key: opts.key, Invite: opts.invite, Name: opts.name}
	}
	result, err := linkWaiting(linkOpts, prev)
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
	switch {
	case opts.claim != "":
		fmt.Printf("claimed server %s\n", cfg.ServerID)
	case opts.join != "":
		fmt.Printf("joined server %s as %s (%s)\n", cfg.ServerID, who, result.Info.Member.Role)
	}
	fmt.Printf("linked to %s as %s (%s)\n", linkTarget(cfg), who, result.Info.Member.Role)
	if perr := cli.UpgradeRequired(result.Info); perr != nil {
		return perr
	}

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
	url := cli.GitURL(cfg.User, cfg.GitHost(), wsID)
	if err := localops.GitRemote(cfg.Repo, url, os.Stdout, os.Stderr); err != nil {
		return err
	}
	fmt.Printf("git remote aether -> %s\n", url)
	return recordWorkspaceOrigin(c, wl.Workspaces, wsID, cfg.Repo)
}

// joinRetry is how often aether link tries a join again while the server
// waits for an admin to approve the joining device.
const joinRetry = 5 * time.Second

// linkWaiting links as cli.Link does. A join refused because its device
// waits for approval is tried again every joinRetry until it is admitted
// or refused otherwise. The refusal is printed as the server sent it when
// it carries an approval code, which it does only when nobody can approve
// the device in the app, or when the join links to an existing member,
// whom the person joining then acts as.
func linkWaiting(opts cli.LinkOptions, prev cli.Config) (cli.LinkResult, error) {
	waiting := false
	for {
		result, err := cli.Link(context.Background(), opts, prev)
		var refused *edgeproto.Refused
		if opts.Join == "" || !errors.As(err, &refused) || refused.Kind != edgeproto.RefusedDevicePending {
			return result, err
		}
		if !waiting {
			if refused.ApprovalCode != "" || (refused.Invitation != "" && refused.MemberID != "") {
				fmt.Println(refused.Error())
			} else {
				fmt.Printf("waiting for an admin of %s to approve this device (%s, %s)…\n", refused.Server, refused.Device, refused.Fingerprint)
			}
			waiting = true
		}
		time.Sleep(joinRetry)
	}
}

// edgeLinkOptions resolves an edge link: a claim, a join, or a server id. The zero
// value means opts.addr is an address, and so is every server name: a
// server's name is whatever its admin chose, and any admin can invite any
// account, so a bare host name such as a tailnet name must never resolve
// to a server the edge lists.
func edgeLinkOptions(opts linkOptions) (cli.LinkOptions, error) {
	if opts.claim != "" {
		_, serverID, err := edgeproto.ParseClaimCode(opts.claim)
		if err != nil {
			return cli.LinkOptions{}, fmt.Errorf("link --claim: %w", err)
		}
		client, err := edgeClient(opts.edge)
		if err != nil {
			return cli.LinkOptions{}, err
		}
		// The claim is made for the account the edge reported at sign-in,
		// so the person sees it before the code is sent.
		session, err := client.Session()
		if err != nil {
			return cli.LinkOptions{}, fmt.Errorf("link --claim through %s: %w", client.Host(), err)
		}
		fmt.Printf("claiming server %s as %s, the account %s reported when this device signed in\n",
			serverID, accountName(session.Account.Account), client.Host())
		return cli.LinkOptions{Addr: opts.direct, Name: opts.name, EdgeURL: client.URL(), Claim: opts.claim}, nil
	}
	if opts.join != "" {
		return cli.LinkOptions{Addr: opts.direct, Name: opts.name, Join: opts.join}, nil
	}
	if opts.invite != "" || opts.key != "" || !edgeproto.ValidServerID(opts.addr) {
		return cli.LinkOptions{}, nil
	}
	client, err := edgeClient(opts.edge)
	if err != nil {
		return cli.LinkOptions{}, err
	}
	if opts.fromEdge {
		interactive := term.IsTerminal(int(os.Stdin.Fd()))
		if err := confirmFromEdge(context.Background(), client, opts.addr, opts.yes, interactive, os.Stdin, os.Stdout); err != nil {
			return cli.LinkOptions{}, err
		}
	}
	return cli.LinkOptions{Addr: opts.direct, Name: opts.name, EdgeURL: client.URL(), ServerID: opts.addr}, nil
}

// errNotConfirmed is a --from-edge link the person did not confirm.
var errNotConfirmed = errors.New("link --from-edge: not linked")

// confirmFromEdge shows what linking serverID from the edge's list pins,
// and asks before it does: the id comes from the edge, and only comparing
// it with the id the server's admin gives rules out an edge that lists a
// false one. Without a terminal to ask on, --yes is required.
func confirmFromEdge(ctx context.Context, client *edgeclient.Client, serverID string, yes, interactive bool, in io.Reader, out io.Writer) error {
	servers, err := client.Servers(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(servers, func(s edgeproto.ServerInfo) bool { return s.ID == serverID })
	if i < 0 {
		return fmt.Errorf("link --from-edge: %s lists no server %s for your account; aether servers lists them", client.Host(), serverID)
	}
	fingerprint, err := cli.HostKeyFingerprint(ctx, client.URL(), serverID)
	if err != nil {
		return err
	}
	s := servers[i]
	_, _ = fmt.Fprintf(out, "server %q\n  id:       %s\n  host key: %s\n  access:   %s: %s\n", s.Name, s.ID, fingerprint, s.AccessPolicy, policyMeaning(s.AccessPolicy))
	_, _ = fmt.Fprintf(out, "This id comes from %s. From now on aether accepts only this host key for the link, directly\n"+
		"and through the edge. An edge that lists a false id can send a first link to another server: compare the\n"+
		"id with the one the server's admin gives you, or that aether-server edge status prints on the server.\n", client.Host())
	if yes {
		return nil
	}
	if !interactive {
		return fmt.Errorf("link --from-edge: no terminal to confirm on; pass --yes once you compared the id, or link by the id the server's admin gave you: aether link %s", serverID)
	}
	_, _ = fmt.Fprint(out, "link and pin this server? [y/N]: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return errNotConfirmed
	}
	return nil
}

// linkTarget names what a link reaches, for the confirmation line.
func linkTarget(cfg cli.Config) string {
	if cfg.ServerID == "" {
		return cfg.Addr
	}
	edge := cfg.EdgeURL
	if u, err := url.Parse(cfg.EdgeURL); err == nil {
		edge = u.Host
	}
	if cfg.Addr != "" {
		return fmt.Sprintf("server %s at %s, then through %s", cfg.ServerID, cfg.Addr, edge)
	}
	return fmt.Sprintf("server %s through %s", cfg.ServerID, edge)
}

// originCaller is the one control call recordWorkspaceOrigin makes,
// narrowed so a test can answer it without a server.
type originCaller interface {
	Call(method string, params, result any) error
}

// recordWorkspaceOrigin teaches the workspace where the linked clone
// pushes, so runs reach the same upstream. It only ever fills a blank: an
// origin the workspace already names is shared by everyone on the server,
// and this clone is one developer's. Recording is a bonus, not the link:
// a viewer is denied it (-32001) and a URL the server will not take is
// refused (-32602), and neither is a reason to fail a link already made.
func recordWorkspaceOrigin(c originCaller, list []protocol.Workspace, wsID, repo string) error {
	ws, ok := workspaceByID(list, wsID)
	if !ok || ws.Origin != "" {
		return nil
	}
	origin, err := localops.OriginURL(repo)
	if err != nil {
		return err
	}
	if origin == "" {
		return nil
	}
	var res protocol.WorkspaceOriginResult
	if err := c.Call(protocol.MethodWorkspaceOrigin, protocol.WorkspaceOriginParams{
		WorkspaceID: wsID, Origin: origin,
	}, &res); err != nil {
		var perr *protocol.Error
		if errors.As(err, &perr) &&
			(perr.Code == protocol.CodeDenied || perr.Code == protocol.CodeInvalidParams) {
			return nil
		}
		return err
	}
	fmt.Printf("workspace origin -> %s\n", res.Workspace.Origin)
	return nil
}
