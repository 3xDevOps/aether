package sshd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodWorkspaceToolsList, (*Server).workspaceToolsList)
	registerMethod(protocol.MethodWorkspaceToolsVerify, (*Server).workspaceToolsVerify)
	registerMethod(protocol.MethodWorkspaceToolsRollback, (*Server).workspaceToolsRollback)
	registerMethod(protocol.MethodWorkspaceToolsReset, (*Server).workspaceToolsReset)
}

func (s *Server) workspaceToolsList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, err := decodeParams[protocol.ToolSnapshotListParams](raw)
	if err != nil {
		return nil, err
	}
	ws, serr := s.resolveToolWorkspace(ctx, p.Workspace)
	if serr != nil {
		return nil, rpcError(serr)
	}
	snapshots, serr := s.cfg.Store.ListToolSnapshots(ctx, member, ws.ID)
	if serr != nil {
		return nil, rpcError(serr)
	}
	active, _ := s.cfg.Store.GetActiveToolSnapshot(ctx, member, ws.ID)
	result := protocol.ToolSnapshotListResult{Snapshots: make([]protocol.ToolSnapshot, 0, len(snapshots))}
	for _, snapshot := range snapshots {
		item := protocol.ToolSnapshotFromDomain(*snapshot)
		if active != nil && snapshot.ID == active.ID {
			item.Active = true
		}
		result.Snapshots = append(result.Snapshots, item)
	}
	if active != nil {
		item := protocol.ToolSnapshotFromDomain(*active)
		item.Active = true
		result.Active = &item
	}
	return result, nil
}

func (s *Server) workspaceToolsVerify(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, err := decodeParams[protocol.ToolSnapshotVerifyParams](raw)
	if err != nil {
		return nil, err
	}
	if e := s.requireToolLaunch(ctx, member); e != nil {
		return nil, e
	}
	ws, serr := s.resolveToolWorkspace(ctx, p.Workspace)
	if serr != nil {
		return nil, rpcError(serr)
	}
	active, serr := s.cfg.Store.GetActiveToolSnapshot(ctx, member, ws.ID)
	if serr != nil {
		return protocol.ToolSnapshotVerifyResult{Verified: false, VerificationExecutable: p.VerificationExecutable, Error: serr.Error()}, nil
	}
	verified := p.VerificationExecutable == "" || active.Manifest.Executable == p.VerificationExecutable
	result := protocol.ToolSnapshotVerifyResult{
		Verified: verified, VerificationExecutable: p.VerificationExecutable,
	}
	if !verified {
		result.Error = "requested executable is not recorded in the active snapshot"
	}
	item := protocol.ToolSnapshotFromDomain(*active)
	result.Snapshot = &item
	return result, nil
}

func (s *Server) workspaceToolsRollback(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, err := decodeParams[protocol.ToolSnapshotRollbackParams](raw)
	if err != nil {
		return nil, err
	}
	if e := s.requireToolLaunch(ctx, member); e != nil {
		return nil, e
	}
	ws, serr := s.resolveToolWorkspace(ctx, p.Workspace)
	if serr != nil {
		return nil, rpcError(serr)
	}
	id := domain.ToolSnapshotID(p.SnapshotID)
	if s.cfg.Toolenv != nil {
		serr = s.cfg.Toolenv.Rollback(ctx, member, ws.ID, id)
	} else {
		serr = s.cfg.Store.SetActiveToolSnapshot(ctx, member, ws.ID, id)
	}
	if serr != nil {
		return nil, rpcError(serr)
	}
	snapshot, serr := s.cfg.Store.GetToolSnapshot(ctx, id)
	if serr != nil {
		return nil, rpcError(serr)
	}
	return protocol.ToolSnapshotRollbackResult{Snapshot: protocol.ToolSnapshotFromDomain(*snapshot)}, nil
}

func (s *Server) workspaceToolsReset(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, err := decodeParams[protocol.ToolSnapshotResetParams](raw)
	if err != nil {
		return nil, err
	}
	if !p.Confirm {
		return nil, invalidParams("confirm is required")
	}
	if e := s.requireToolLaunch(ctx, member); e != nil {
		return nil, e
	}
	ws, serr := s.resolveToolWorkspace(ctx, p.Workspace)
	if serr != nil {
		return nil, rpcError(serr)
	}
	if s.cfg.Toolenv != nil {
		serr = s.cfg.Toolenv.Reset(ctx, member, ws.ID)
	} else {
		serr = s.cfg.Store.SetActiveToolSnapshot(ctx, member, ws.ID, "")
	}
	if serr != nil {
		return nil, rpcError(serr)
	}
	return protocol.ToolSnapshotResetResult{Reset: true}, nil
}

func (s *Server) requireToolLaunch(ctx context.Context, member domain.MemberID) *protocol.Error {
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return rpcError(err)
	}
	if err := permissions.Check(permissions.Launch, actor, permissions.Target{}); err != nil {
		return &protocol.Error{Code: protocol.CodeDenied, Message: err.Error()}
	}
	return nil
}

func (s *Server) resolveToolWorkspace(ctx context.Context, selector protocol.WorkspaceSelector) (*domain.Workspace, error) {
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
