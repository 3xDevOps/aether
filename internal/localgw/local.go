package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/overlay"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// localHandlers is the /local/v1 surface: verbs that need the user's
// repository and SSH key, so only the local gateway offers them. The
// capabilities endpoint advertises exactly the verbs dispatched here.
var localHandlers = map[string]func(*Gateway, *http.Request, []byte) (any, *protocol.Error){
	"daemon.install":    (*Gateway).localDaemonInstall,
	"daemon.status":     (*Gateway).localDaemonStatus,
	"env.harnesses":     (*Gateway).localEnvHarnesses,
	"forward.start":     (*Gateway).localForwardStart,
	"forward.status":    (*Gateway).localForwardStatus,
	"forward.stop":      (*Gateway).localForwardStop,
	"git.identity":      (*Gateway).localGitIdentity,
	"link.apply":        (*Gateway).localLinkApply,
	"link.repo":         (*Gateway).localLinkRepo,
	"link.status":       (*Gateway).localLinkStatus,
	"link.switch":       (*Gateway).localLinkSwitch,
	"profile.preview":   (*Gateway).localProfilePreview,
	"profile.push":      (*Gateway).localProfilePush,
	"pull":              (*Gateway).localPull,
	"pull.switch":       (*Gateway).localPullSwitch,
	"repo.fast-forward": (*Gateway).localRepoFastForward,
	"repo.push":         (*Gateway).localRepoPush,
	"repo.sync":         (*Gateway).localRepoSync,
	"sync.start":        (*Gateway).localSyncStart,
	"sync.status":       (*Gateway).localSyncStatus,
	"sync.stop":         (*Gateway).localSyncStop,
	"update.apply":      (*Gateway).localUpdateApply,
	"update.check":      (*Gateway).localUpdateCheck,
	"update.status":     (*Gateway).localUpdateStatus,
}

var localVerbs = slices.Sorted(maps.Keys(localHandlers))

// localState is the mutable client-machine state behind /local/v1 and
// /ws/envscan: the saved link config (link.repo updates it), the
// background sync sessions, and the single environment-scan slot.
type localState struct {
	mu      sync.Mutex
	cfg     cli.Config
	mtime   time.Time
	sync    *localops.SyncManager
	forward *localops.ForwardManager
	// scanActive claims the one-scan-at-a-time slot for /ws/envscan.
	scanActive bool
	// scanArgv overrides the scan's harness command; tests set it to run
	// stub executables.
	scanArgv []string
}

// newLocalState seeds the verb state from the gateway config. It never
// fails: an unlinked (zero) cli.Config simply reports linked:false and
// refuses the verbs that need a repo.
func newLocalState(cfg Config) *localState {
	state := &localState{cfg: cfg.CLI, sync: localops.NewSyncManager(), forward: localops.NewForwardManager()}
	if path, err := cli.Path(); err == nil {
		if info, err := os.Stat(path); err == nil {
			state.mtime = info.ModTime()
		}
	}
	return state
}

// cacheConfig updates the in-memory link and its file timestamp together.
// Callers hold s.mu while changing the cache.
func (s *localState) cacheConfig(cfg cli.Config) {
	path, err := cli.Path()
	if err != nil {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	s.cfg = cfg
	s.mtime = info.ModTime()
}

// snapshot returns the current link config under the lock. The CLI may update
// its config while the gateway is running, so reload it when its mtime changes.
func (s *localState) snapshot() cli.Config {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := cli.Path()
	if err == nil {
		if info, statErr := os.Stat(path); statErr == nil && !info.ModTime().Equal(s.mtime) {
			if cfg, loadErr := cli.Load(); loadErr == nil {
				if s.cfg.Active != "" {
					named, ok := cfg.Named(s.cfg.Active)
					if !ok {
						return s.cfg
					}
					cfg = named
				}
				s.cacheConfig(cfg)
			}
		}
	}
	return s.cfg
}

// handleLocal serves POST /local/v1/{verb}: the client-machine verbs that
// wrap the linked repository and SSH connection. Params arrive as one
// JSON object in the body; failures answer the same error envelope as the
// proxied API.
func (g *Gateway) handleLocal(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r, false) {
		g.deny(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		webgate.WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeParse, Message: "read body: " + err.Error()})
		return
	}
	verb := r.PathValue("verb")
	handler, ok := localHandlers[verb]
	if !ok {
		webgate.WriteError(w, http.StatusNotFound, &protocol.Error{
			Code:    protocol.CodeMethodNotFound,
			Message: "unknown local verb " + verb,
		})
		return
	}
	result, perr := handler(g, r, body)
	if perr != nil {
		webgate.WriteError(w, webgate.StatusFor(perr.Code), perr)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// decodeParams unmarshals the request body into v; an empty body is an
// empty params object.
func decodeParams(body []byte, v any) *protocol.Error {
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: "decode params: " + err.Error()}
	}
	return nil
}

// linkRef is one named server profile as link.status reports it: enough
// for a switcher to list, nothing secret.
type linkRef struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}

// namedLinks projects cfg.Links for link.status; nil when none are saved
// so the JSON omits the key.
func namedLinks(cfg cli.Config) []linkRef {
	if len(cfg.Links) == 0 {
		return nil
	}
	links := make([]linkRef, len(cfg.Links))
	for i, l := range cfg.Links {
		links[i] = linkRef{Name: l.Name, Addr: l.Addr}
	}
	return links
}

func (g *Gateway) localLinkStatus(*http.Request, []byte) (any, *protocol.Error) {
	cfg := g.local.snapshot()
	return struct {
		Linked           bool      `json:"linked"`
		ServerConfigured bool      `json:"server_configured"`
		Addr             string    `json:"addr"`
		User             string    `json:"user"`
		Repo             string    `json:"repo"`
		Links            []linkRef `json:"links,omitempty"`
		Active           string    `json:"active,omitempty"`
	}{Linked: cfg.Repo != "", ServerConfigured: cfg.Addr != "", Addr: cfg.Addr, User: cfg.User, Repo: cfg.Repo,
		Links: namedLinks(cfg), Active: cfg.Active}, nil
}

func (g *Gateway) localLinkApply(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Addr   string `json:"addr"`
		Invite string `json:"invite"`
		Name   string `json:"name"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.Addr == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "addr is required"}
	}
	prev := g.local.snapshot()
	result, err := cli.Link(cli.LinkOptions{
		Addr:   params.Addr,
		Invite: params.Invite,
		Name:   params.Name,
	}, prev)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	cfg := result.Config
	if err := cli.Save(cfg); err != nil {
		_ = result.Conn.Close()
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	g.local.mu.Lock()
	g.local.cacheConfig(cfg)
	g.local.mu.Unlock()
	g.cfg.Backend.Relink(cfg, result.Conn)
	return struct {
		Addr         string          `json:"addr"`
		User         string          `json:"user"`
		Member       protocol.Member `json:"member"`
		KeyGenerated string          `json:"key_generated,omitempty"`
	}{Addr: cfg.Addr, User: cfg.User, Member: result.Info.Member, KeyGenerated: result.KeyGenerated}, nil
}

// localLinkSwitch always refuses. link.apply relinks in place because the
// dashboard has nothing open yet; switching a linked gateway would leave
// every live event stream and attach session pointed at the old host, so
// it stays a process restart. The verb exists so the SPA can probe it and
// render the instruction verbatim.
func (g *Gateway) localLinkSwitch(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Name string `json:"name"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.Name == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "name is required"}
	}
	return nil, &protocol.Error{
		Code:    protocol.CodeInvalidState,
		Message: "restart aether gui --server " + params.Name + " to switch servers",
	}
}

func (g *Gateway) localLinkRepo(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Repo        string `json:"repo"`
		WorkspaceID string `json:"workspace_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.Repo == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "repo is required"}
	}
	// The onboarding wizard names the workspace it just picked; only an
	// unqualified request falls back to the CLI's sole-workspace rule.
	wsID := params.WorkspaceID
	if wsID == "" {
		var perr *protocol.Error
		if wsID, perr = g.resolveWorkspace(r); perr != nil {
			return nil, perr
		}
	}
	g.local.mu.Lock()
	defer g.local.mu.Unlock()
	cfg, url, err := localops.LinkRepo(g.local.cfg, params.Repo, wsID)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	g.local.cacheConfig(cfg)
	origin, perr := g.recordWorkspaceOrigin(r, wsID, cfg.Repo)
	if perr != nil {
		return nil, perr
	}
	return struct {
		Repo   string `json:"repo"`
		Remote string `json:"remote"`
		URL    string `json:"url"`
		Origin string `json:"origin,omitempty"`
	}{Repo: cfg.Repo, Remote: "aether", URL: url, Origin: origin}, nil
}

// recordWorkspaceOrigin teaches the workspace where the just-linked clone
// pushes, so runs reach the same upstream. It only ever fills a blank: a
// workspace that already names an origin keeps it, because the server's
// answer is shared by everyone and this clone is one developer's.
// Returns the workspace's origin afterwards.
func (g *Gateway) recordWorkspaceOrigin(r *http.Request, wsID, repo string) (string, *protocol.Error) {
	ws, perr := g.pickWorkspace(r, wsID)
	if perr != nil {
		return "", perr
	}
	if ws.Origin != "" {
		return ws.Origin, nil
	}
	origin, err := localops.OriginURL(repo)
	if err != nil {
		return "", &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	if origin == "" {
		return "", nil
	}
	params, err := json.Marshal(protocol.WorkspaceOriginParams{WorkspaceID: wsID, Origin: origin})
	if err != nil {
		return "", &protocol.Error{Code: protocol.CodeInternal, Message: "encode workspace.origin params: " + err.Error()}
	}
	if _, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodWorkspaceOrigin, params); perr != nil {
		return "", perr
	}
	return origin, nil
}

// resolveWorkspace picks the workspace whose ID the git remote URL must
// carry, exactly like `aether link --repo`: a single workspace resolves
// implicitly, none or several is an invalid state the user resolves
// server-side first.
func (g *Gateway) resolveWorkspace(r *http.Request) (string, *protocol.Error) {
	ws, perr := g.pickWorkspace(r, "")
	if perr != nil {
		return "", perr
	}
	return ws.ID, nil
}

// pickWorkspace returns the workspace the caller named, or the sole one
// on the server when it named none. Callers that only have a name to
// show the user need the whole record, not just the ID.
func (g *Gateway) pickWorkspace(r *http.Request, wsID string) (protocol.Workspace, *protocol.Error) {
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodWorkspaceList, nil)
	if perr != nil {
		return protocol.Workspace{}, perr
	}
	var wl protocol.WorkspaceListResult
	if err := json.Unmarshal(result, &wl); err != nil {
		return protocol.Workspace{}, &protocol.Error{Code: protocol.CodeInternal, Message: "decode workspace list: " + err.Error()}
	}
	if wsID != "" {
		for _, ws := range wl.Workspaces {
			if ws.ID == wsID {
				return ws, nil
			}
		}
		return protocol.Workspace{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: "no workspace " + wsID + " on this server"}
	}
	switch len(wl.Workspaces) {
	case 0:
		return protocol.Workspace{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: "no workspace yet; add one before linking a repo"}
	case 1:
		return wl.Workspaces[0], nil
	default:
		return protocol.Workspace{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: "multiple workspaces; link with `aether link --repo --workspace <name-or-id>`"}
	}
}

// repoWorkspace resolves what every repo verb needs before it runs git:
// the link config with a repository in it, and the workspace whose base
// branch the verb acts on, checked against where the `aether` remote
// actually points.
func (g *Gateway) repoWorkspace(r *http.Request, body []byte) (cli.Config, protocol.Workspace, *protocol.Error) {
	var params struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return cli.Config{}, protocol.Workspace{}, perr
	}
	cfg := g.local.snapshot()
	if cfg.Repo == "" {
		return cfg, protocol.Workspace{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: "no linked repo; re-run aether link --repo"}
	}
	ws, perr := g.pickWorkspace(r, params.WorkspaceID)
	if perr != nil {
		return cfg, ws, perr
	}
	if ws.BaseBranch == "" {
		return cfg, ws, &protocol.Error{Code: protocol.CodeInvalidState, Message: "workspace " + ws.Name + " has no base branch"}
	}
	return cfg, ws, checkRemoteWorkspace(cfg, ws)
}

// repoGitError maps a localops failure onto the wire: a state the user
// fixes in their own repository is invalid state, anything git ran and
// lost is internal, carrying git's own words either way.
func repoGitError(err error) *protocol.Error {
	switch {
	case errors.Is(err, localops.ErrPushPrecondition):
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	case err != nil:
		return &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return nil
}

// localRepoPush seeds the workspace with the push the quickstart used to
// ask the user to run in a terminal: one `git push -u aether <base>` in
// the linked repository, never forced and never carrying a second ref.
// The branch is the workspace's own base branch, so a workspace created
// with `--base` seeds the branch its runs actually fork from.
//
// It compares before it pushes. A workspace someone else already seeded
// leaves a later member's clone behind it, where a plain push is
// rejected with "fetch first"; reporting that state is what lets the
// caller offer a fast-forward instead of failing. Only a clone that is
// ahead, or a workspace with no such branch, is pushed.
func (g *Gateway) localRepoPush(r *http.Request, body []byte) (any, *protocol.Error) {
	cfg, ws, perr := g.repoWorkspace(r, body)
	if perr != nil {
		return nil, perr
	}
	comparison, err := localops.CompareBranch(cfg.Repo, ws.BaseBranch)
	if perr := repoGitError(err); perr != nil {
		return nil, perr
	}
	result := struct {
		Branch          string `json:"branch"`
		Remote          string `json:"remote"`
		State           string `json:"state"`
		LocalCommit     string `json:"local_commit"`
		WorkspaceCommit string `json:"workspace_commit"`
		Ahead           int    `json:"ahead"`
		Behind          int    `json:"behind"`
		Output          string `json:"output"`
	}{
		Branch: ws.BaseBranch, Remote: "aether",
		LocalCommit: comparison.Local, WorkspaceCommit: comparison.Workspace,
		Ahead: comparison.Ahead, Behind: comparison.Behind, Output: comparison.Output,
	}
	switch comparison.State {
	case localops.BranchMissing, localops.BranchAhead:
		output, err := localops.Push(cfg.Repo, ws.BaseBranch)
		result.Output += output
		if errors.Is(err, localops.ErrPushRejected) {
			// Another member moved the workspace branch between the
			// compare and the push, so git answered the "fetch first"
			// this verb exists to replace. Compare again and report what
			// the caller can act on; when the second compare no longer
			// explains the rejection, git's own words stand rather than a
			// state that claims a push that never landed.
			again, cmpErr := localops.CompareBranch(cfg.Repo, ws.BaseBranch)
			if cmpErr == nil && (again.State == localops.BranchBehind || again.State == localops.BranchDiverged) {
				result.Output += again.Output
				result.LocalCommit, result.WorkspaceCommit = again.Local, again.Workspace
				result.Ahead, result.Behind = again.Ahead, again.Behind
				result.State = string(again.State)
				return result, nil
			}
		}
		if perr := repoGitError(err); perr != nil {
			return nil, perr
		}
		result.State = "pushed"
	case localops.BranchSame:
		result.State = "up-to-date"
	case localops.BranchBehind:
		result.State = "behind"
	default:
		result.State = "diverged"
	}
	return result, nil
}

// localRepoFastForward catches the linked clone's base branch up with the
// workspace's copy of it. It is the follow-up to a `repo.push` that
// answered `behind`, and it fast-forwards only: a diverged branch is
// refused, because choosing between a rebase and a merge is the member's
// call to make in their own repository.
func (g *Gateway) localRepoFastForward(r *http.Request, body []byte) (any, *protocol.Error) {
	cfg, ws, perr := g.repoWorkspace(r, body)
	if perr != nil {
		return nil, perr
	}
	result, err := localops.FastForward(cfg.Repo, ws.BaseBranch)
	if perr := repoGitError(err); perr != nil {
		return nil, perr
	}
	return struct {
		Branch  string `json:"branch"`
		Commit  string `json:"commit"`
		Current bool   `json:"current"`
		Dirty   bool   `json:"dirty"`
		Output  string `json:"output"`
	}{Branch: result.Branch, Commit: result.Commit, Current: result.Current, Dirty: result.Dirty, Output: result.Output}, nil
}

// localRepoSync fetches the workspace base branch from the repository's
// origin remote and advances the matching server branch without touching the
// local branch or working tree.
func (g *Gateway) localRepoSync(r *http.Request, body []byte) (any, *protocol.Error) {
	cfg, ws, perr := g.repoWorkspace(r, body)
	if perr != nil {
		return nil, perr
	}
	output, err := localops.SyncBase(cfg.Repo, ws.BaseBranch)
	if perr := repoGitError(err); perr != nil {
		return nil, perr
	}
	return struct {
		Branch string `json:"branch"`
		Output string `json:"output"`
	}{Branch: ws.BaseBranch, Output: output}, nil
}

// checkRemoteWorkspace refuses a push or sync whose branch was read from one
// workspace while the `aether` remote points at another. The remote URL
// carries the workspace ID, so the two can disagree whenever link.repo
// last ran for a different workspace - and the answer would otherwise
// report success for a workspace this repository never seeded. A repo
// with no remote at all passes through to Push or SyncBase's own refusal,
// which names the fix.
func checkRemoteWorkspace(cfg cli.Config, ws protocol.Workspace) *protocol.Error {
	url, err := localops.AetherRemoteURL(cfg.Repo)
	if err != nil {
		// This check reads the repository before the push does, so a
		// folder the user has since moved or deleted fails here first.
		// Say nothing: Push's preflight names the path and the fix a
		// moment later, in the user's own terms.
		return nil
	}
	if want := cli.GitURL(cfg.User, cfg.Addr, ws.ID); url != "" && url != want {
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: "the aether remote in " + cfg.Repo +
			" points at " + url + ", not workspace " + ws.Name + "; add the remote for this workspace first"}
	}
	return nil
}

func (g *Gateway) localPull(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		RunID string `json:"run_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.RunID == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "run_id is required"}
	}
	cfg := g.local.snapshot()
	if cfg.Repo == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: "no linked repo; re-run aether link --repo"}
	}
	callParams, err := json.Marshal(protocol.RunIDParams{RunID: params.RunID})
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodRunPull, callParams)
	if perr != nil {
		return nil, perr
	}
	var coords protocol.RunPullResult
	if err = json.Unmarshal(result, &coords); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "decode pull coordinates: " + err.Error()}
	}
	pullResult, err := localops.Pull(cfg.Repo, cfg.User, cfg.Addr, coords)
	if perr := repoGitError(err); perr != nil {
		return nil, perr
	}
	return struct {
		Branch  string `json:"branch"`
		Ref     string `json:"ref"`
		Output  string `json:"output"`
		Current bool   `json:"current"`
		Dirty   bool   `json:"dirty"`
	}{
		Branch: pullResult.Branch, Ref: pullResult.Ref, Output: pullResult.Output,
		Current: pullResult.Current, Dirty: pullResult.Dirty,
	}, nil

}

func (g *Gateway) localPullSwitch(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		RunID string `json:"run_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.RunID == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "run_id is required"}
	}
	cfg := g.local.snapshot()
	if cfg.Repo == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: "no linked repo; re-run aether link --repo"}
	}
	callParams, err := json.Marshal(protocol.RunIDParams{RunID: params.RunID})
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodRunPull, callParams)
	if perr != nil {
		return nil, perr
	}
	var coords protocol.RunPullResult
	if err = json.Unmarshal(result, &coords); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "decode pull coordinates: " + err.Error()}
	}
	if err := localops.SwitchPull(cfg.Repo, coords.Branch); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	return struct {
		Branch string `json:"branch"`
	}{Branch: coords.Branch}, nil
}

func (g *Gateway) localSyncStart(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		RunID string `json:"run_id"`
		Force bool   `json:"force"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.RunID == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "run_id is required"}
	}
	cfg := g.local.snapshot()
	if cfg.Repo == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: "no linked repo; re-run aether link --repo"}
	}
	err := g.local.sync.Start(cfg.Repo, params.RunID, params.Force, g.cfg.Backend.Sync, g.reportSyncConflict)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	return struct {
		RunID string `json:"run_id"`
		State string `json:"state"`
	}{RunID: params.RunID, State: localops.SyncRunning}, nil
}

// reportSyncConflict publishes a paused overlay to the server so both
// affected members see the sync.conflict event, mirroring the CLI's
// publishSyncConflict. A notification failure has nowhere to go (the
// HTTP request that started the session is long gone), so it is dropped;
// the conflict itself stays visible through sync.status.
func (g *Gateway) reportSyncConflict(runID string, c *overlay.Conflict) {
	params, err := json.Marshal(protocol.SyncConflictParams{
		RunID:         runID,
		SyncSessionID: c.SessionID,
		Files:         c.Files,
	})
	if err != nil {
		return
	}
	_, _ = g.cfg.Backend.Call(context.Background(), protocol.MethodSyncConflict, params)
}

func (g *Gateway) localSyncStop(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		RunID string `json:"run_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.RunID == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "run_id is required"}
	}
	if err := g.local.sync.Stop(params.RunID); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	return struct {
		RunID string `json:"run_id"`
		State string `json:"state"`
	}{RunID: params.RunID, State: localops.SyncStopped}, nil
}

func (g *Gateway) localSyncStatus(*http.Request, []byte) (any, *protocol.Error) {
	sessions := g.local.sync.Status()
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].RunID < sessions[j].RunID })
	return struct {
		Sessions []localops.SyncSession `json:"sessions"`
	}{Sessions: sessions}, nil
}

func (g *Gateway) localForwardStart(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Target string `json:"target"`
		Port   uint32 `json:"port"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if !validForwardTarget(params.Target) {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "target must be run:<run-id> or terminal"}
	}
	if params.Port < 1 || params.Port > 65535 {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "port must be between 1 and 65535"}
	}
	err := g.local.forward.Ensure(params.Target, int(params.Port), func() (io.ReadWriteCloser, error) {
		return g.cfg.Backend.Forward(params.Target, params.Port)
	})
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	return struct {
		Target    string `json:"target"`
		Port      uint32 `json:"port"`
		LocalPort uint32 `json:"local_port"`
		State     string `json:"state"`
	}{Target: params.Target, Port: params.Port, LocalPort: params.Port, State: "active"}, nil
}

func (g *Gateway) localForwardStop(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Target string `json:"target"`
		Port   uint32 `json:"port"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if !validForwardTarget(params.Target) {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "target must be run:<run-id> or terminal"}
	}
	if params.Port < 1 || params.Port > 65535 {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "port must be between 1 and 65535"}
	}
	if err := g.local.forward.Stop(params.Target, int(params.Port)); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	}
	return struct {
		Target string `json:"target"`
		Port   uint32 `json:"port"`
		State  string `json:"state"`
	}{Target: params.Target, Port: params.Port, State: "stopped"}, nil
}

func (g *Gateway) localForwardStatus(*http.Request, []byte) (any, *protocol.Error) {
	forwards := g.local.forward.Status()
	sort.Slice(forwards, func(i, j int) bool {
		if forwards[i].Target != forwards[j].Target {
			return forwards[i].Target < forwards[j].Target
		}
		return forwards[i].Port < forwards[j].Port
	})
	return struct {
		Forwards []localops.ForwardSession `json:"forwards"`
	}{Forwards: forwards}, nil
}

func (g *Gateway) localDaemonInstall(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Server string `json:"server"`
		Repo   string `json:"repo"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	if params.Server == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "server is required"}
	}
	linked := g.local.snapshot()
	repo := params.Repo
	if repo == "" {
		repo = linked.Repo
	}
	if repo == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "repo is required (none linked)"}
	}
	// The daemon dials the same server as this gateway, so it needs the
	// key `aether link --key` chose; without it the unit falls back to
	// ~/.ssh/id_ed25519 and cannot authenticate.
	unitPath, note, err := localops.InstallDaemon(params.Server, repo, linked.Key)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return struct {
		UnitPath string `json:"unit_path"`
		Note     string `json:"note"`
	}{UnitPath: unitPath, Note: note}, nil
}

func (g *Gateway) localDaemonStatus(*http.Request, []byte) (any, *protocol.Error) {
	installed, unitPath, err := localops.DaemonStatus()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return struct {
		Installed bool   `json:"installed"`
		UnitPath  string `json:"unit_path"`
	}{Installed: installed, UnitPath: unitPath}, nil
}

// loginPathTimeout bounds the login shell asked for its PATH before a
// harness lookup or scan; an rc file that hangs must not hold either back.
const loginPathTimeout = 5 * time.Second

// localEnvHarnesses reports which setup-capable harnesses are installed
// on this machine, for the onboarding wizard's harness picker. PATH is
// widened from the login shell first, so an agent installed through a
// shell profile, or since the gateway started, is found; a failed probe
// becomes the warning and only the standard folders are checked. The
// folders searched let the wizard say where it looked when nothing is
// found, and repo_path is the one repository folder the saved link config
// knows (when exactly one is known) so the wizard can prefill the
// from-repo folder input.
func (g *Gateway) localEnvHarnesses(r *http.Request, _ []byte) (any, *protocol.Error) {
	ctx, cancel := context.WithTimeout(r.Context(), loginPathTimeout)
	defer cancel()
	var warning string
	if _, err := localops.AdoptLoginPath(ctx); err != nil {
		warning = err.Error()
	}
	return struct {
		Harnesses []localops.HarnessStatus `json:"harnesses"`
		Searched  []string                 `json:"searched"`
		Warning   string                   `json:"warning,omitempty"`
		RepoPath  string                   `json:"repo_path,omitempty"`
	}{
		Harnesses: localops.DetectHarnesses(),
		Searched:  localops.SearchedDirs(),
		Warning:   warning,
		RepoPath:  suggestedRepo(g.local.snapshot()),
	}, nil
}

// suggestedRepo returns the single repository folder the link config
// carries, across the default link and every named profile. Several
// distinct folders mean there is no safe guess, so nothing is suggested.
func suggestedRepo(cfg cli.Config) string {
	repo := cfg.Repo
	for _, l := range cfg.Links {
		switch {
		case l.Repo == "" || l.Repo == repo:
		case repo == "":
			repo = l.Repo
		default:
			return ""
		}
	}
	return repo
}
