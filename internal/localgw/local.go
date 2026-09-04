package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/overlay"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// localVerbs is the /local/v1 surface, sorted, advertised by the
// capabilities endpoint. These verbs need the user's repository and SSH
// key, so only the local gateway offers them.
var localVerbs = []string{
	"daemon.install",
	"daemon.status",
	"env.harnesses",
	"image.scaffold",
	"link.repo",
	"link.status",
	"link.switch",
	"profile.preview",
	"profile.push",
	"pull",
	"repo.push",
	"sync.start",
	"sync.status",
	"sync.stop",
	"update.apply",
	"update.check",
	"update.status",
}

// localState is the mutable client-machine state behind /local/v1 and
// /ws/envscan: the saved link config (link.repo updates it), the
// background sync sessions, and the single environment-scan slot.
type localState struct {
	mu    sync.Mutex
	cfg   cli.Config
	mtime time.Time
	sync  *localops.SyncManager
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
	state := &localState{cfg: cfg.CLI, sync: localops.NewSyncManager()}
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
	handler, ok := map[string]func(*Gateway, *http.Request, []byte) (any, *protocol.Error){
		"daemon.install":  (*Gateway).localDaemonInstall,
		"daemon.status":   (*Gateway).localDaemonStatus,
		"env.harnesses":   (*Gateway).localEnvHarnesses,
		"image.scaffold":  (*Gateway).localImageScaffold,
		"link.repo":       (*Gateway).localLinkRepo,
		"link.switch":     (*Gateway).localLinkSwitch,
		"link.status":     (*Gateway).localLinkStatus,
		"profile.preview": (*Gateway).localProfilePreview,
		"profile.push":    (*Gateway).localProfilePush,
		"pull":            (*Gateway).localPull,
		"repo.push":       (*Gateway).localRepoPush,
		"sync.start":      (*Gateway).localSyncStart,
		"sync.status":     (*Gateway).localSyncStatus,
		"sync.stop":       (*Gateway).localSyncStop,
		"update.apply":    (*Gateway).localUpdateApply,
		"update.check":    (*Gateway).localUpdateCheck,
		"update.status":   (*Gateway).localUpdateStatus,
	}[verb]
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

// localLinkSwitch always refuses: the gateway's SSH identity, host-key
// verification, and every WebSocket bridge are bound to the backend built
// at process start, so swapping servers in-place would leave live event
// streams and attach sessions pointed at the old host. Switching is a
// process restart. The verb exists so the SPA can probe it and render
// the instruction verbatim.
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
	return struct {
		Repo   string `json:"repo"`
		Remote string `json:"remote"`
		URL    string `json:"url"`
	}{Repo: cfg.Repo, Remote: "aether", URL: url}, nil
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

// localRepoPush seeds the workspace with the push the quickstart used to
// ask the user to run in a terminal: one `git push -u aether <base>` in
// the linked repository, never forced and never carrying a second ref.
// The branch is the workspace's own base branch, so a workspace created
// with `--base` seeds the branch its runs actually fork from.
func (g *Gateway) localRepoPush(r *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	cfg := g.local.snapshot()
	if cfg.Repo == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: "no linked repo; re-run aether link --repo"}
	}
	ws, perr := g.pickWorkspace(r, params.WorkspaceID)
	if perr != nil {
		return nil, perr
	}
	if ws.BaseBranch == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: "workspace " + ws.Name + " has no base branch"}
	}
	if perr := checkRemoteWorkspace(cfg, ws); perr != nil {
		return nil, perr
	}
	output, err := localops.Push(cfg.Repo, ws.BaseBranch)
	switch {
	case errors.Is(err, localops.ErrPushPrecondition):
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	case err != nil:
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return struct {
		Branch string `json:"branch"`
		Remote string `json:"remote"`
		Output string `json:"output"`
	}{Branch: ws.BaseBranch, Remote: "aether", Output: output}, nil
}

// checkRemoteWorkspace refuses a push whose branch was read from one
// workspace while the `aether` remote points at another. The remote URL
// carries the workspace ID, so the two can disagree whenever link.repo
// last ran for a different workspace - and the answer would otherwise
// report success for a workspace this repository never seeded. A repo
// with no remote at all passes through to Push's own refusal, which
// names the fix.
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
	branch, ref, output, err := localops.Pull(cfg.Repo, cfg.User, cfg.Addr, coords)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return struct {
		Branch string `json:"branch"`
		Ref    string `json:"ref"`
		Output string `json:"output"`
	}{Branch: branch, Ref: ref, Output: output}, nil
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
	repo := params.Repo
	if repo == "" {
		repo = g.local.snapshot().Repo
	}
	if repo == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "repo is required (none linked)"}
	}
	unitPath, note, err := localops.InstallDaemon(params.Server, repo)
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

// localEnvHarnesses reports which setup-capable harnesses are installed
// on this machine, for the onboarding wizard's harness picker, plus the
// one repository folder the saved link config knows (when exactly one
// is known) so the wizard can prefill the from-repo folder input.
func (g *Gateway) localEnvHarnesses(*http.Request, []byte) (any, *protocol.Error) {
	return struct {
		Harnesses []localops.HarnessStatus `json:"harnesses"`
		RepoPath  string                   `json:"repo_path,omitempty"`
	}{Harnesses: localops.DetectHarnesses(), RepoPath: suggestedRepo(g.local.snapshot())}, nil
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

func (g *Gateway) localImageScaffold(_ *http.Request, body []byte) (any, *protocol.Error) {
	var params struct {
		Repo string `json:"repo"`
		Kind string `json:"kind"`
	}
	if perr := decodeParams(body, &params); perr != nil {
		return nil, perr
	}
	repo := params.Repo
	if repo == "" {
		repo = g.local.snapshot().Repo
	}
	if repo == "" {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "repo is required (none linked)"}
	}
	switch params.Kind {
	case "dockerfile", "devcontainer":
	default:
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "kind must be dockerfile or devcontainer"}
	}
	written, err := localops.Scaffold(repo, params.Kind)
	switch {
	case errors.Is(err, localops.ErrScaffoldExists):
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	case err != nil:
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
	}
	return struct {
		Written []string `json:"written"`
	}{Written: written}, nil
}
