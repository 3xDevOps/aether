package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/envdef"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// EnvironmentService is the control channel's view of the scheduler's
// environment orchestration (*scheduler.Scheduler).
type EnvironmentService interface {
	// BuildEnvironment builds one stored definition version into the
	// workspace image: build, verify, activate, swap, prune.
	BuildEnvironment(ctx context.Context, workspace domain.WorkspaceID, version int) error
	// RollbackEnvironment re-activates the previous good version,
	// rebuilding its image first if retention removed the tag, and
	// returns the version that is active again.
	RollbackEnvironment(ctx context.Context, workspace domain.WorkspaceID) (int, error)
	// EditEnvironment runs the chosen harness headless in a throwaway
	// container against the current definition and the admin's change
	// request, storing the validated result as a new saved version - the
	// proposal env.build later approves. Progress and the terminal state
	// ride environment.edit events.
	EditEnvironment(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID, harness, request string) (int, error)
}

// Every environment method is workspace administration: a stored
// Dockerfile is arbitrary code on the server's docker daemon the moment
// it builds, and status detail is admin-facing operational state.
func init() {
	registerGuarded(protocol.MethodEnvSave, permissions.WorkspaceAdmin, nil, (*Server).envSave)
	registerGuarded(protocol.MethodEnvBuild, permissions.WorkspaceAdmin, nil, (*Server).envBuild)
	registerGuarded(protocol.MethodEnvStatus, permissions.WorkspaceAdmin, nil, (*Server).envStatus)
	registerGuarded(protocol.MethodEnvRollback, permissions.WorkspaceAdmin, nil, (*Server).envRollback)
	registerGuarded(protocol.MethodEnvEdit, permissions.WorkspaceAdmin, nil, (*Server).envEdit)
	registerGuarded(protocol.MethodEnvGet, permissions.WorkspaceAdmin, nil, (*Server).envGet)
}

func (s *Server) environments() (EnvironmentService, *protocol.Error) {
	if s.cfg.Services.Environments == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "environment service not configured"}
	}
	return s.cfg.Services.Environments, nil
}

// envSave validates a definition through the envdef contract and stores it
// as the workspace's next version. Nothing builds until env.build.
func (s *Server) envSave(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.EnvSaveParams](params)
	if perr != nil {
		return nil, perr
	}
	ws, err := s.resolveWorkspaceSelector(ctx, p.Workspace)
	if err != nil {
		return nil, rpcError(err)
	}
	source := domain.EnvironmentSource(p.Source)
	if !source.Valid() {
		return nil, invalidParams("unknown environment source " + strconv.Quote(p.Source))
	}
	manifest, err := envdef.ParseManifest(p.Manifest)
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	if err := envdef.ValidateDockerfile(p.Dockerfile, manifest); err != nil {
		return nil, invalidParams(err.Error())
	}
	def := &domain.EnvironmentDefinition{
		WorkspaceID: ws.ID,
		Dockerfile:  p.Dockerfile,
		Manifest:    manifest,
		Source:      source,
		Harness:     p.Harness,
		Status:      domain.EnvironmentSaved,
	}
	if err := s.cfg.Store.SaveEnvironmentDefinition(ctx, def); err != nil {
		return nil, rpcError(err)
	}
	return protocol.EnvSaveResult{Version: def.Version}, nil
}

// envBuild launches the build asynchronously and returns the version now
// building; progress and the terminal status ride the environment.build
// event stream.
func (s *Server) envBuild(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.environments()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.EnvBuildParams](params)
	if perr != nil {
		return nil, perr
	}
	ws, err := s.resolveWorkspaceSelector(ctx, p.Workspace)
	if err != nil {
		return nil, rpcError(err)
	}
	version := p.Version
	if version == 0 {
		if version, perr = s.defaultEnvVersion(ctx, ws.ID); perr != nil {
			return nil, perr
		}
	} else if _, err := s.cfg.Store.GetEnvironmentDefinition(ctx, ws.ID, version); err != nil {
		return nil, rpcError(err)
	}
	// The build outlives this request: it runs against the server's
	// lifetime context and reports through the store and the event feed,
	// which also record any failure - the log line is for the operator
	// tailing the server, not the only sink.
	buildCtx := s.authCtx()
	s.spawn(func() {
		if err := svc.BuildEnvironment(buildCtx, ws.ID, version); err != nil {
			slog.Warn("sshd: environment build failed", "workspace", ws.ID, "version", version, "error", err)
		}
	})
	return protocol.EnvBuildResult{Version: version}, nil
}

// defaultEnvVersion picks what env.build builds when no version is named:
// the active version, or the newest one before anything has activated.
func (s *Server) defaultEnvVersion(ctx context.Context, workspace domain.WorkspaceID) (int, *protocol.Error) {
	active, err := s.cfg.Store.GetActiveEnvironmentDefinition(ctx, workspace)
	if err == nil {
		return active.Version, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, rpcError(err)
	}
	defs, err := s.cfg.Store.ListEnvironmentDefinitions(ctx, workspace)
	if err != nil {
		return 0, rpcError(err)
	}
	if len(defs) == 0 {
		return 0, &protocol.Error{Code: protocol.CodeInvalidState, Message: "workspace has no saved environment definition"}
	}
	return defs[0].Version, nil
}

func (s *Server) envStatus(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.EnvStatusParams](params)
	if perr != nil {
		return nil, perr
	}
	ws, err := s.resolveWorkspaceSelector(ctx, p.Workspace)
	if err != nil {
		return nil, rpcError(err)
	}
	defs, err := s.cfg.Store.ListEnvironmentDefinitions(ctx, ws.ID)
	if err != nil {
		return nil, rpcError(err)
	}
	result := protocol.EnvStatusResult{Versions: make([]protocol.EnvironmentVersion, 0, len(defs))}
	for _, def := range defs {
		v := protocol.EnvironmentVersionFromDomain(def)
		if v.Active {
			result.ActiveVersion = v.Version
		}
		result.Versions = append(result.Versions, v)
	}
	return result, nil
}

func (s *Server) envRollback(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.environments()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.EnvRollbackParams](params)
	if perr != nil {
		return nil, perr
	}
	ws, err := s.resolveWorkspaceSelector(ctx, p.Workspace)
	if err != nil {
		return nil, rpcError(err)
	}
	// Nothing active means nothing to roll back from: an invalid state,
	// not an internal failure.
	if _, err = s.cfg.Store.GetActiveEnvironmentDefinition(ctx, ws.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: "workspace has no active environment version to roll back from"}
		}
		return nil, rpcError(err)
	}
	version, err := svc.RollbackEnvironment(ctx, ws.ID)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.EnvRollbackResult{Version: version}, nil
}

// envEdit launches the edit-agent run asynchronously for the calling
// admin; agent output, the proposed version, and any failure ride the
// environment.edit event stream.
func (s *Server) envEdit(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.environments()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.EnvEditParams](params)
	if perr != nil {
		return nil, perr
	}
	ws, err := s.resolveWorkspaceSelector(ctx, p.Workspace)
	if err != nil {
		return nil, rpcError(err)
	}
	if perr := checkEnvEditHarness(p.Harness); perr != nil {
		return nil, perr
	}
	if strings.TrimSpace(p.Request) == "" {
		return nil, invalidParams("describe the change you want in the request field")
	}
	// The edit outlives this request, like env.build: it runs against the
	// server's lifetime context and reports through the store and the
	// event feed; the log line is for the operator tailing the server.
	editCtx := s.authCtx()
	s.spawn(func() {
		if _, err := svc.EditEnvironment(editCtx, ws.ID, member, p.Harness, p.Request); err != nil {
			slog.Warn("sshd: environment edit failed", "workspace", ws.ID, "harness", p.Harness, "error", err)
		}
	})
	return protocol.EnvEditResult{Accepted: true}, nil
}

// checkEnvEditHarness rejects an agent that could never run an edit, so
// the mistake surfaces in the reply instead of the event stream. Deeper
// preflight - login state on this server - stays with the scheduler,
// which reports it as a failed event naming the aether agent add fix.
// The fake harness stays allowed: demos and tests drive the full flow
// with it and it never launches a vendor CLI.
func checkEnvEditHarness(name string) *protocol.Error {
	if name == "fake" {
		return nil
	}
	for _, p := range harness.SetupHarnesses() {
		if p.Name == name {
			return nil
		}
	}
	return invalidParams(strconv.Quote(name) + " cannot edit environments; pick claude, codex, pi, or amp")
}

// envGet returns one stored version in full - including the Dockerfile
// that env.status deliberately omits - plus, when diff_against names
// another version, a unified diff between the two Dockerfiles.
func (s *Server) envGet(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.EnvGetParams](params)
	if perr != nil {
		return nil, perr
	}
	ws, err := s.resolveWorkspaceSelector(ctx, p.Workspace)
	if err != nil {
		return nil, rpcError(err)
	}
	if p.Version <= 0 {
		return nil, invalidParams("name the environment version to fetch")
	}
	def, err := s.cfg.Store.GetEnvironmentDefinition(ctx, ws.ID, p.Version)
	if err != nil {
		return nil, rpcError(err)
	}
	result := protocol.EnvGetResult{
		Version:    def.Version,
		Dockerfile: def.Dockerfile,
		Manifest:   def.Manifest,
		Source:     def.Source,
		Harness:    def.Harness,
		Status:     def.Status,
	}
	if p.DiffAgainst != 0 {
		base, err := s.cfg.Store.GetEnvironmentDefinition(ctx, ws.ID, p.DiffAgainst)
		if err != nil {
			return nil, rpcError(err)
		}
		diff, err := dockerfileDiff(ctx, base.Dockerfile, def.Dockerfile)
		if err != nil {
			return nil, rpcError(err)
		}
		result.Diff = diff
	}
	return result, nil
}

// dockerfileDiff renders a git unified diff from one Dockerfile text to
// another. git diff --no-index compares files, so both texts land in a
// throwaway directory laid out as a/Dockerfile and b/Dockerfile;
// --no-prefix then yields exactly the `diff --git a/Dockerfile
// b/Dockerfile` header shape the dashboard's patch parser reads. An
// empty result means the texts match.
func dockerfileDiff(ctx context.Context, before, after string) (string, error) {
	dir, err := os.MkdirTemp("", "aether-env-diff-")
	if err != nil {
		return "", fmt.Errorf("sshd: create the diff scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for side, text := range map[string]string{"a": before, "b": after} {
		if err = os.Mkdir(filepath.Join(dir, side), 0o700); err != nil {
			return "", fmt.Errorf("sshd: lay out the diff scratch directory: %w", err)
		}
		if err = os.WriteFile(filepath.Join(dir, side, "Dockerfile"), []byte(text), 0o600); err != nil {
			return "", fmt.Errorf("sshd: write the Dockerfile pair for diffing: %w", err)
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--no-index", "--no-prefix", "--no-color", "a/Dockerfile", "b/Dockerfile")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		// Exit 0: the files are identical.
		return "", nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		// Exit 1 is git's "the files differ", not a failure.
		return stdout.String(), nil
	default:
		return "", fmt.Errorf("sshd: git diff over the Dockerfile pair: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
}

func (s *Server) resolveWorkspaceSelector(ctx context.Context, selector protocol.WorkspaceSelector) (*domain.Workspace, error) {
	if (selector.ID == "") == (selector.Name == "") {
		return nil, errors.New("workspace selector must contain exactly one ID or name")
	}
	if selector.ID != "" {
		return s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(selector.ID))
	}
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, ws := range workspaces {
		if ws.Name == selector.Name {
			return ws, nil
		}
	}
	return nil, store.ErrNotFound
}
