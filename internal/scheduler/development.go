package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/browser"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// LiveRun is resolved from the durable run and the exact supervised container,
// never from caller-provided account, checkout, or host paths.
type LiveRun struct {
	Run         domain.Run
	ContainerID runtime.ID
	Workdir     string
	User        string
	Env         []string
}

func (s *Scheduler) ResolveLiveRun(ctx context.Context, id domain.RunID, allowPaused bool) (LiveRun, error) {
	var live LiveRun
	if s.superCtx != nil && s.superCtx.Err() != nil {
		return live, errors.New("scheduler is shutting down")
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return live, err
	}
	if run.Status != domain.RunRunning && run.Status != domain.RunNeedsAttention {
		return live, errors.New("run has no live development environment")
	}
	s.mu.Lock()
	e := s.runs[id]
	if e == nil {
		s.mu.Unlock()
		sc, err := s.readSidecar(id)
		if err != nil {
			return live, err
		}
		if sc.RunID != string(id) || sc.ContainerID == "" || sc.KillRequested || sc.ExitObserved || sc.DestroyPending || sc.Retained || (sc.Paused && !allowPaused) {
			return live, errors.New("recorded development environment is unavailable or paused")
		}
		found, err := s.cfg.Runtime.FindByCreationKey(ctx, string(id))
		if err != nil {
			return live, err
		}
		if found != runtime.ID(sc.ContainerID) {
			return live, errors.New("recorded run container identity mismatch")
		}
		live = LiveRun{Run: *run, ContainerID: found, Workdir: s.cfg.WorktreeMount, User: sc.RunUser}
		if sc.Home != "" {
			live.Env = append(live.Env, "HOME="+sc.Home)
		}
		if !allowPaused {
			if err := s.checkDevelopmentOpen(id, found); err != nil {
				return LiveRun{}, err
			}
		}
		return live, nil
	}
	if e.containerID == "" || e.killRequested || e.finalizing || e.destroyPending || e.retained || (e.paused && !allowPaused) {
		s.mu.Unlock()
		return live, errors.New("run development environment is unavailable or paused")
	}
	if e.status != run.Status {
		s.mu.Unlock()
		return live, errors.New("run lifecycle changed; retry")
	}
	live = LiveRun{Run: *run, ContainerID: e.containerID, Workdir: s.cfg.WorktreeMount, User: e.runUser}
	if e.home != "" {
		live.Env = append(live.Env, "HOME="+e.home)
	}
	s.mu.Unlock()
	if !allowPaused {
		if err := s.checkDevelopmentOpen(id, live.ContainerID); err != nil {
			return LiveRun{}, err
		}
	}
	return live, nil
}

type developmentState struct {
	mu       sync.Mutex
	manager  *browser.Manager
	reason   error
	locks    map[domain.RunID]*sync.Mutex
	reserved map[domain.RunID]bool
	captures sync.Mutex
}

func (s *Scheduler) developmentState() *developmentState {
	s.developmentMu.Lock()
	defer s.developmentMu.Unlock()
	if s.development == nil {
		d := &developmentState{locks: make(map[domain.RunID]*sync.Mutex), reserved: make(map[domain.RunID]bool)}
		d.manager, d.reason = browser.NewManager(filepath.Join(s.cfg.StateDir, "browser"), s.cfg.Runtime, browser.Config{Image: s.cfg.BrowserImage, CPULimit: 1, MemoryLimitBytes: 1 << 30})
		s.development = d
	}
	return s.development
}
func (d *developmentState) lock(id domain.RunID) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.locks[id] == nil {
		d.locks[id] = &sync.Mutex{}
	}
	return d.locks[id]
}
func (s *Scheduler) Capabilities(ctx context.Context, id domain.RunID) ([]string, error) {
	if _, err := s.ResolveLiveRun(ctx, id, false); err != nil {
		return []string{}, nil
	}
	capabilities := []string{}
	if s.cfg.Control == nil {
		return capabilities, nil
	}
	terminals := s.DevelopmentTerminalsAvailable() == nil
	browserAvailable := s.developmentState().reason == nil
	if terminals {
		capabilities = append(capabilities, protocol.MethodDevTerminalList, protocol.MethodDevTerminalStart, protocol.MethodDevTerminalOutput, protocol.MethodDevTerminalScreen, protocol.MethodDevTerminalInput, protocol.MethodDevTerminalResize, protocol.MethodDevTerminalWait, protocol.MethodDevTerminalStop)
	}
	if browserAvailable {
		capabilities = append(capabilities, protocol.MethodDevBrowserStatus, protocol.MethodDevBrowserOpen, protocol.MethodDevBrowserPages, protocol.MethodDevBrowserNavigate, protocol.MethodDevBrowserSnapshot, protocol.MethodDevBrowserAction, protocol.MethodDevBrowserScreenshot, protocol.MethodDevBrowserViewport, protocol.MethodDevBrowserWait, protocol.MethodDevBrowserConsole, protocol.MethodDevBrowserNetwork, protocol.MethodDevBrowserReset, protocol.MethodDevBrowserClose, protocol.MethodDevArtifactList, protocol.MethodDevArtifactGet, protocol.MethodDevArtifactDelete)
		if terminals {
			capabilities = append(capabilities, protocol.MethodDevTerminalScreenshot)
		}
	}
	s.mu.Lock()
	retainer, retains := s.evidence.(interface{ RetainsDevelopmentArtifacts() bool })
	s.mu.Unlock()
	if retains && retainer.RetainsDevelopmentArtifacts() {
		capabilities = append(capabilities, protocol.MethodDevArtifactRetain)
	}
	if terminals || browserAvailable {
		capabilities = append(capabilities, protocol.MethodDevControlStatus, protocol.MethodDevControlAcquire, protocol.MethodDevControlRelease)
	}
	return capabilities, nil
}
func (s *Scheduler) HandleAgent(ctx context.Context, id domain.RunID, method string, params json.RawMessage) (any, error) {
	// DecodeDevAgentParams also rejects a null/empty run_id; ordinary human
	// decoding below must never be used as the socket identity guard.
	var shape map[string]json.RawMessage
	if err := protocol.DecodeDevAgentParams(params, &shape); err != nil {
		return nil, err
	}
	return s.CallDevelopment(ctx, id, control.Principal{Kind: control.PrincipalRunAgent, RunID: id}, method, params, func() error { return nil })
}
func decodeDevelopment(raw json.RawMessage, target any) error {
	if len(raw) > protocol.MaxDevParamsBytes {
		return errors.New("development parameters exceed limit")
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("development request must contain one JSON object")
	}
	return nil
}
func (s *Scheduler) developmentAuthorization(ctx context.Context, id domain.RunID, p control.Principal, authorize func() error) error {
	if authorize == nil {
		return errors.New("development authorization callback is required")
	}
	switch p.Kind {
	case control.PrincipalRunAgent:
		if p.RunID != id || p.MemberID != "" {
			return control.ErrInvalid
		}
	case control.PrincipalMember:
		if p.MemberID == "" || p.RunID != "" {
			return control.ErrInvalid
		}
	default:
		return control.ErrInvalid
	}
	if err := authorize(); err != nil {
		return err
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return err
	}
	actor := p.MemberID
	if p.Kind == control.PrincipalRunAgent {
		actor = run.MemberID
	}
	member, err := s.cfg.Store.GetMember(ctx, actor)
	if err != nil {
		return err
	}
	account, err := s.cfg.Store.GetMember(ctx, run.AccountMember())
	if err != nil {
		return err
	}
	if member.Pending || account.Pending {
		return permissions.ErrDenied
	}
	if actor != account.ID {
		shared, err := s.cfg.Store.AccountSharedWith(ctx, account.ID, actor)
		if err != nil {
			return err
		}
		if !shared {
			return fmt.Errorf("%w: backing account is not shared", permissions.ErrDenied)
		}
	}
	return nil
}
func (s *Scheduler) CallDevelopment(ctx context.Context, id domain.RunID, p control.Principal, method string, raw json.RawMessage, authorize func() error) (any, error) {
	if s.cfg.Control == nil {
		return nil, errors.New("development controller unavailable")
	}
	var identity protocol.DevRunParams
	if err := json.Unmarshal(raw, &identity); err != nil && len(raw) != 0 {
		return nil, err
	}
	if identity.RunID != "" && identity.RunID != string(id) {
		return nil, errors.New("development run identity mismatch")
	}
	auth := func() error { return s.developmentAuthorization(ctx, id, p, authorize) }
	if err := auth(); err != nil {
		return nil, err
	}
	var result any
	var err error
	switch {
	case strings.HasPrefix(method, "dev.control."):
		result, err = s.developmentControl(ctx, id, p, method, raw, auth)
	case method == protocol.MethodDevTerminalScreenshot:
		result, err = s.captureTerminal(ctx, id, raw, auth)
	case strings.HasPrefix(method, "dev.terminal."):
		result, err = s.DevelopmentTerminal(ctx, id, p, method, raw, auth)
	case strings.HasPrefix(method, "dev.browser."):
		result, err = s.developmentBrowser(ctx, id, p, method, raw, auth)
	case method == protocol.MethodDevArtifactRetain:
		result, err = s.retainDevelopmentArtifacts(ctx, id, p, raw, auth)
	case strings.HasPrefix(method, "dev.artifact."):
		result, err = s.developmentArtifact(id, method, raw, auth)
	default:
		return nil, errors.New("unknown development method")
	}
	if err != nil {
		return nil, err
	}
	if err = auth(); err != nil {
		return nil, err
	}
	if _, err = protocol.MarshalDevResult(result); err != nil {
		return nil, err
	}
	return result, nil
}
func controllerDTO(s *control.SurfaceSnapshot) *protocol.DevController {
	if s == nil {
		return nil
	}
	out := &protocol.DevController{Kind: string(s.Principal.Kind), MemberID: string(s.Principal.MemberID), RunID: string(s.Principal.RunID), DevControlFence: protocol.DevControlFence{ControlSessionID: s.SessionID, ControlGeneration: s.Generation}, Connected: s.Connected, AcquiredAt: s.AcquiredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
	if !s.ExpiresAt.IsZero() {
		out.ExpiresAt = s.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return out
}
func (s *Scheduler) validateDevelopmentSurface(ctx context.Context, id domain.RunID, target protocol.DevSurface) (control.Surface, error) {
	surface := control.Surface{Kind: control.SurfaceKind(target.Kind), ID: target.ID, Incarnation: target.Incarnation}
	if _, err := s.ResolveLiveRun(ctx, id, false); err != nil {
		return surface, err
	}
	switch surface.Kind {
	case control.SurfaceTerminal:
		terminal, err := s.LookupDevelopmentTerminal(ctx, id, target.ID)
		if err != nil {
			return surface, err
		}
		if terminal.Incarnation != target.Incarnation {
			return surface, control.ErrStale
		}
	case control.SurfaceBrowser:
		live, err := s.ResolveLiveRun(ctx, id, false)
		if err != nil {
			return surface, err
		}
		d := s.developmentState()
		if d.reason != nil {
			return surface, d.reason
		}
		status, _, err := d.manager.Reconcile(ctx, browser.Run{ID: string(id), ContainerID: live.ContainerID})
		if target.ID != "browser" || browserIncarnation(status) != target.Incarnation || target.Incarnation == "" {
			return surface, control.ErrStale
		}
		if err != nil && !errors.Is(err, browser.ErrUnavailable) {
			return surface, err
		}
	default:
		return surface, control.ErrInvalid
	}
	return surface, nil
}
func (s *Scheduler) developmentControl(ctx context.Context, id domain.RunID, p control.Principal, method string, raw json.RawMessage, auth func() error) (any, error) {
	switch method {
	case protocol.MethodDevControlStatus:
		var req protocol.DevControlStatusParams
		if err := decodeDevelopment(raw, &req); err != nil {
			return nil, err
		}
		surface, err := s.validateDevelopmentSurface(ctx, id, req.Surface)
		if err != nil {
			return nil, err
		}
		snapshot, ok := s.cfg.Control.SurfaceStatus(string(id), surface)
		var owner *protocol.DevController
		if ok {
			owner = controllerDTO(&snapshot)
		}
		return protocol.DevControlStatusResult{Surface: req.Surface, Controller: owner}, nil
	case protocol.MethodDevControlAcquire:
		var req protocol.DevControlAcquireParams
		if err := decodeDevelopment(raw, &req); err != nil {
			return nil, err
		}
		surface, surfaceErr := s.validateDevelopmentSurface(ctx, id, req.Surface)
		if surfaceErr != nil {
			return nil, surfaceErr
		}
		if surface.Kind == control.SurfaceBrowser {
			live, err := s.ResolveLiveRun(ctx, id, false)
			if err != nil {
				return nil, err
			}
			status, client, err := s.browserClient(ctx, live, false)
			if err == nil {
				if bindErr := s.bindBrowserInput(id, status, client); bindErr != nil {
					return nil, bindErr
				}
			} else if !errors.Is(err, browser.ErrUnavailable) {
				return nil, err
			}
		}
		current, displaced, acquireErr := s.cfg.Control.AcquireSurface(string(id), surface, p, req.ControlSessionID, req.Takeover, req.ExpectedGeneration, func() error {
			if _, err := s.validateDevelopmentSurface(ctx, id, req.Surface); err != nil {
				return err
			}
			if err := auth(); err != nil {
				return err
			}
			if p.Kind == control.PrincipalMember && surface.Kind == control.SurfaceTerminal && s.cfg.DevelopmentTerminalTakeover != nil {
				return s.cfg.DevelopmentTerminalTakeover(ctx, id, p.MemberID)
			}
			return nil
		})
		if acquireErr != nil {
			return nil, acquireErr
		}
		return protocol.DevControlAcquireResult{DevControlStatusResult: protocol.DevControlStatusResult{Surface: req.Surface, Controller: controllerDTO(&current)}, Displaced: controllerDTO(displaced)}, nil
	case protocol.MethodDevControlRelease:
		var req protocol.DevControlReleaseParams
		if err := decodeDevelopment(raw, &req); err != nil {
			return nil, err
		}
		surface := control.Surface{Kind: control.SurfaceKind(req.Surface.Kind), ID: req.Surface.ID, Incarnation: req.Surface.Incarnation}
		err := s.cfg.Control.ReleaseSurface(string(id), surface, p, req.ControlSessionID, req.ControlGeneration, auth)
		return protocol.DevControlReleaseResult{Released: err == nil}, err
	}
	return nil, errors.New("unknown development control operation")
}

func (s *Scheduler) UseDevelopmentTakeover(takeover func(context.Context, domain.RunID, domain.MemberID) error) {
	s.cfg.DevelopmentTerminalTakeover = takeover
}
func (s *Scheduler) AuthorizeDevelopment(ctx context.Context, id domain.RunID, p control.Principal, authorize func() error) error {
	return s.developmentAuthorization(ctx, id, p, authorize)
}
