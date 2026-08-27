package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/envdef"
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
}

// Every environment method is workspace administration: a stored
// Dockerfile is arbitrary code on the server's docker daemon the moment
// it builds, and status detail is admin-facing operational state.
func init() {
	registerGuarded(protocol.MethodEnvSave, permissions.WorkspaceAdmin, nil, (*Server).envSave)
	registerGuarded(protocol.MethodEnvBuild, permissions.WorkspaceAdmin, nil, (*Server).envBuild)
	registerGuarded(protocol.MethodEnvStatus, permissions.WorkspaceAdmin, nil, (*Server).envStatus)
	registerGuarded(protocol.MethodEnvRollback, permissions.WorkspaceAdmin, nil, (*Server).envRollback)
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
	if _, err := s.cfg.Store.GetActiveEnvironmentDefinition(ctx, ws.ID); err != nil {
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
