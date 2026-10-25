package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
	"github.com/3xDevOps/Aether/internal/version"
)

func init() {
	register(command{
		name:  "status",
		short: "show this computer's link, edge sign-ins, server, workspaces and agents, and the next setup step",
		usage: "aether status [--json]",
		example: "aether status\n" +
			"aether status --json",
		flags: true,
		run:   runStatus,
	})
}

// statusTimeout bounds the dial and the calls status makes; a call takes
// no context, so closing the connection is what ends a stalled one.
const statusTimeout = 30 * time.Second

// Workspaces and Agents are nil when the server was not asked, and empty
// when it has none.
type statusReport struct {
	ClientVersion string                                    `json:"client_version"`
	Config        string                                    `json:"config"`
	Edges         []statusEdge                              `json:"edges"`
	Link          *statusLink                               `json:"link"`
	Repos         []statusRepo                              `json:"repos"`
	Server        *protocol.ServerInfoResult                `json:"server"`
	Workspaces    []protocol.Workspace                      `json:"workspaces"`
	Agents        []protocol.AgentInfo                      `json:"agents"`
	Mirrors       map[string]protocol.WorkspaceMirrorResult `json:"mirrors,omitempty"`
	Next          string                                    `json:"next"`
	Error         string                                    `json:"error,omitempty"`
}

type statusLink struct {
	Addr     string `json:"addr,omitempty"`
	ServerID string `json:"server_id,omitempty"`
	Edge     string `json:"edge,omitempty"`
}

// Error is set only for the clone a config written before per-workspace
// clones names, when it could not be read.
type statusRepo struct {
	cli.LinkedRepo
	Error string `json:"error,omitempty"`
}

// statusEdge mirrors an entry of the gateway's edge.status; it never
// carries the device token.
type statusEdge struct {
	Edge string `json:"edge"`
	*edgeclient.Session
	Error string `json:"error,omitempty"`
}

func runStatus(args []string) error {
	fs := newFlagSet("status")
	jsonOut := fs.Bool("json", false, "print the status as one JSON object")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: aether status [--json]")
	}
	report, err := gatherStatus()
	if !*jsonOut {
		printStatus(os.Stdout, report)
		return err
	}
	if err != nil {
		report.Error = err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if encodeErr := enc.Encode(report); encodeErr != nil {
		return encodeErr
	}
	return err
}

func gatherStatus() (statusReport, error) {
	r := statusReport{ClientVersion: version.String()}
	path, err := cli.Path()
	if err != nil {
		return r, err
	}
	r.Config = path
	dir := filepath.Dir(path)
	origins, err := edgeclient.SignedIn(dir)
	if err != nil {
		return r, err
	}
	r.Edges = make([]statusEdge, 0, len(origins))
	for _, origin := range origins {
		e := statusEdge{Edge: origin}
		client, edgeErr := edgeclient.New(dir, origin)
		var session edgeclient.Session
		if edgeErr == nil {
			session, edgeErr = client.Session()
		}
		if edgeErr != nil {
			e.Error = edgeErr.Error()
		} else {
			e.Session = &session
		}
		r.Edges = append(r.Edges, e)
	}
	cfg, err := cli.Load()
	if errors.Is(err, cli.ErrNotLinked) {
		r.Next = "aether link --claim <code>, or aether link <host:port>"
		if len(r.Edges) == 0 {
			r.Next = "aether login, then aether link --claim <code>; or aether link <host:port>"
		}
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.Link = &statusLink{Addr: cfg.Addr, ServerID: cfg.ServerID, Edge: cfg.EdgeURL}
	repos, repoErr := localops.LinkedRepos(cfg)
	r.Repos = make([]statusRepo, 0, len(repos)+1)
	for _, repo := range repos {
		r.Repos = append(r.Repos, statusRepo{LinkedRepo: repo})
	}
	if repoErr != nil {
		r.Repos = append(r.Repos, statusRepo{LinkedRepo: cli.LinkedRepo{Path: cfg.Repo}, Error: repoErr.Error()})
	}

	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	conn, err := cli.DialContext(ctx, cfg)
	if err != nil {
		return r, err
	}
	defer func() { _ = conn.Close() }()
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()
	c, err := conn.Control()
	if err != nil {
		return r, err
	}
	defer func() { _ = c.Close() }()
	call := func(method string, result any) error {
		callErr := c.Call(method, struct{}{}, result)
		if callErr != nil && ctx.Err() != nil {
			return fmt.Errorf("%s: %w", method, ctx.Err())
		}
		return olderServer(callErr)
	}

	var info protocol.ServerInfoResult
	if err = call(protocol.MethodServerInfo, &info); err != nil {
		return r, err
	}
	r.Server = &info
	if perr := cli.UpgradeRequired(info); perr != nil {
		r.Next = protocol.UpdateCommand
		return r, perr
	}
	if info.Member.Pending {
		r.Next = "an admin runs: aether member approve " + info.Member.ID
		return r, nil
	}
	var workspaces protocol.WorkspaceListResult
	if err = call(protocol.MethodWorkspaceList, &workspaces); err != nil {
		return r, err
	}
	r.Workspaces = append([]protocol.Workspace{}, workspaces.Workspaces...)
	var agents protocol.AgentListResult
	if err = call(protocol.MethodAgentList, &agents); err != nil {
		return r, err
	}
	r.Agents = append([]protocol.AgentInfo{}, agents.Agents...)
	r.Mirrors = map[string]protocol.WorkspaceMirrorResult{}
	for _, ws := range r.Workspaces {
		var m protocol.WorkspaceMirrorResult
		if err = c.Call(protocol.MethodWorkspaceMirrorStatus, protocol.WorkspaceMirrorParams{WorkspaceID: ws.ID}, &m); err != nil {
			return r, fmt.Errorf("%s for workspace %s: %w", protocol.MethodWorkspaceMirrorStatus, ws.ID, err)
		}
		r.Mirrors[ws.ID] = m
	}
	unseeded := map[string]string{}
	for _, ws := range r.Workspaces {
		if r.Mirrors[ws.ID].Enabled {
			continue
		}
		err := c.Call(protocol.MethodFilesTree, protocol.FilesTreeParams{WorkspaceID: ws.ID}, &protocol.FilesTreeResult{})
		switch {
		case err == nil:
		case strings.Contains(err.Error(), "workspace has no repository yet"):
			clone, _ := localops.RepoFor(cfg, ws.ID)
			unseeded[ws.ID] = clone
		default:
			return r, fmt.Errorf("%s for workspace %s: %w", protocol.MethodFilesTree, ws.ID, err)
		}
	}
	r.Next = nextStep(info.Member, r.Workspaces, r.Agents, r.Mirrors, unseeded)
	return r, nil
}

// An agent that declares no login path counts as logged in: nothing says
// it is not. Only a device login runs without a terminal, so any other
// login is named as the human's step.
func nextStep(m protocol.Member, workspaces []protocol.Workspace, agents []protocol.AgentInfo, mirrors map[string]protocol.WorkspaceMirrorResult, unseeded map[string]string) string {
	if len(workspaces) == 0 {
		if m.Role != string(domain.RoleAdmin) {
			return "an admin runs: aether workspace import <path|url>"
		}
		return "aether workspace import <path|url>"
	}
	installed := slices.DeleteFunc(slices.Clone(agents), func(a protocol.AgentInfo) bool { return !a.Installed })
	if len(installed) == 0 {
		return "aether agent add <name>"
	}
	if slices.ContainsFunc(installed, func(a protocol.AgentInfo) bool { return a.LoginFound == nil || *a.LoginFound }) {
		return mirrorStep(m.Role == string(domain.RoleAdmin), workspaces, mirrors, unseeded)
	}
	agent := installed[0]
	switch {
	case agent.LoginCommand == "":
		return "a human runs in a terminal: aether terminal, then " + agent.Name + "'s own sign-in"
	case agent.LoginKind != "device":
		return "a human runs in a terminal: aether agent login " + agent.Name
	}
	return "aether agent login " + agent.Name
}

// A mirrored workspace cannot launch a run until a human adopts a fetched
// commit as its accepted base. A local one cannot until its base branch
// has a commit; unseeded maps such a workspace to its linked clone, or "".
func mirrorStep(admin bool, workspaces []protocol.Workspace, mirrors map[string]protocol.WorkspaceMirrorResult, unseeded map[string]string) string {
	who := "an admin runs: "
	if admin {
		who = ""
	}
	for _, ws := range workspaces {
		m := mirrors[ws.ID]
		clone, isUnseeded := unseeded[ws.ID]
		switch {
		case isUnseeded && clone != "":
			return fmt.Sprintf("git -C %s push aether %s", shellquote.Quote(clone), shellquote.Quote(ws.BaseBranch))
		case isUnseeded:
			return who + "aether workspace import <path>"
		case !m.Enabled || m.AcceptedCommit != "":
		case m.ObservedCommit != "":
			if admin {
				who = "a human runs: "
			}
			return fmt.Sprintf("%saether workspace mirror adopt --workspace %s --generation %d --commit %s --yes", who, ws.ID, m.Generation, m.ObservedCommit)
		default:
			return who + "aether workspace mirror refresh --workspace " + ws.ID
		}
	}
	return ""
}

func printStatus(w io.Writer, r statusReport) {
	line := func(label, format string, args ...any) {
		_, _ = fmt.Fprintf(w, "%-10s  %s\n", label, fmt.Sprintf(format, args...))
	}
	line("client", "%s", r.ClientVersion)
	if r.Config != "" {
		line("config", "%s", r.Config)
	}
	if r.Edges != nil && len(r.Edges) == 0 {
		line("edge", "not signed in")
	}
	for _, e := range r.Edges {
		if e.Session == nil {
			line("edge", "%s: %s", e.Edge, e.Error)
			continue
		}
		line("edge", "%s: signed in as %s, device %q (%s)", e.Edge, accountName(e.Account.Account), e.Device.Label, e.Device.ID)
	}
	switch {
	case r.Link != nil:
		line("link", "%s", linkTarget(cli.Config{Addr: r.Link.Addr, ServerID: r.Link.ServerID, EdgeURL: r.Link.Edge}))
	case r.Next != "":
		line("link", "not linked")
	}
	if r.Server != nil {
		line("server", "%s", r.Server.ServerVersion)
		m := r.Server.Member
		if m.Pending {
			line("member", "%s (%s), %s, pending approval", m.DisplayName, m.ID, m.Role)
		} else {
			line("member", "%s (%s), %s", m.DisplayName, m.ID, m.Role)
		}
	}
	if r.Workspaces != nil && len(r.Workspaces) == 0 {
		line("workspace", "none")
	}
	for _, ws := range r.Workspaces {
		line("workspace", "%s (%s)", ws.Name, ws.ID)
	}
	if r.Repos != nil && len(r.Repos) == 0 {
		line("repo", "none linked")
	}
	for _, repo := range r.Repos {
		ws, onServer := workspaceByID(r.Workspaces, repo.WorkspaceID)
		switch {
		case repo.Error != "":
			line("repo", "%s: %s", repo.Path, repo.Error)
		case onServer:
			line("repo", "%s -> workspace %s (%s)", repo.Path, ws.Name, ws.ID)
		case r.Workspaces != nil:
			line("repo", "%s -> workspace %s, not on this server", repo.Path, repo.WorkspaceID)
		default:
			line("repo", "%s -> workspace %s", repo.Path, repo.WorkspaceID)
		}
	}
	for _, a := range r.Agents {
		switch {
		case !a.Installed:
			line("agent", "%s: not installed", a.Name)
		case a.LoginFound == nil:
			line("agent", "%s: installed, declares no login file", a.Name)
		case *a.LoginFound:
			line("agent", "%s: installed, login file found", a.Name)
		default:
			line("agent", "%s: installed, no login file found", a.Name)
		}
	}
	switch {
	case r.Next != "":
		line("next", "%s", r.Next)
	case r.Agents != nil:
		line("next", "none")
	}
}
