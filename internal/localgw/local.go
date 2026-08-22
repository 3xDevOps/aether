package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"

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
	"image.scaffold",
	"link.repo",
	"link.status",
	"link.switch",
	"pull",
	"sync.start",
	"sync.status",
	"sync.stop",
}

// localState is the mutable client-machine state behind /local/v1: the
// saved link config (link.repo updates it) and the background sync
// sessions.
type localState struct {
	mu   sync.Mutex
	cfg  cli.Config
	sync *localops.SyncManager
}

// newLocalState seeds the verb state from the gateway config. It never
// fails: an unlinked (zero) cli.Config simply reports linked:false and
// refuses the verbs that need a repo.
func newLocalState(cfg Config) *localState {
	return &localState{cfg: cfg.CLI, sync: localops.NewSyncManager()}
}

// snapshot returns the current link config under the lock.
func (s *localState) snapshot() cli.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		"daemon.install": (*Gateway).localDaemonInstall,
		"daemon.status":  (*Gateway).localDaemonStatus,
		"image.scaffold": (*Gateway).localImageScaffold,
		"link.repo":      (*Gateway).localLinkRepo,
		"link.switch":    (*Gateway).localLinkSwitch,
		"link.status":    (*Gateway).localLinkStatus,
		"pull":           (*Gateway).localPull,
		"sync.start":     (*Gateway).localSyncStart,
		"sync.status":    (*Gateway).localSyncStatus,
		"sync.stop":      (*Gateway).localSyncStop,
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
		Linked bool      `json:"linked"`
		Addr   string    `json:"addr"`
		User   string    `json:"user"`
		Repo   string    `json:"repo"`
		Links  []linkRef `json:"links,omitempty"`
		Active string    `json:"active,omitempty"`
	}{Linked: cfg.Repo != "", Addr: cfg.Addr, User: cfg.User, Repo: cfg.Repo,
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
	g.local.cfg = cfg
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
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodWorkspaceList, nil)
	if perr != nil {
		return "", perr
	}
	var wl protocol.WorkspaceListResult
	if err := json.Unmarshal(result, &wl); err != nil {
		return "", &protocol.Error{Code: protocol.CodeInternal, Message: "decode workspace list: " + err.Error()}
	}
	switch len(wl.Workspaces) {
	case 0:
		return "", &protocol.Error{Code: protocol.CodeInvalidState, Message: "no workspace yet; add one before linking a repo"}
	case 1:
		return wl.Workspaces[0].ID, nil
	default:
		return "", &protocol.Error{Code: protocol.CodeInvalidState, Message: "multiple workspaces; link with `aether link --repo --workspace <name-or-id>`"}
	}
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
	if err := json.Unmarshal(result, &coords); err != nil {
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

func (g *Gateway) localSyncStart(r *http.Request, body []byte) (any, *protocol.Error) {
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
