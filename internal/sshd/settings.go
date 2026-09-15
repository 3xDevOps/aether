package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerGuarded(protocol.MethodWorkspaceSettings, permissions.WorkspaceAdmin, nil, (*Server).workspaceSettings)
	registerGuarded(protocol.MethodWorkspaceOrigin, permissions.Push, workspaceTarget, (*Server).workspaceOrigin)
	registerGuarded(protocol.MethodRunProtect, permissions.Protect, runTarget, (*Server).runProtect)
}

// workspaceSettings updates a workspace's settings (admin only; the guard
// has already checked WorkspaceAdmin). The change is stamped into the
// workspace timeline attributed to the caller.
func (s *Server) workspaceSettings(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceSettingsParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if !domain.ValidSteerOthers(p.SteerOthers) {
		return nil, invalidParams(`steer_others must be "" or "admins_only"`)
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	if cerr := permissions.Check(permissions.WorkspaceAdmin, actor, permissions.Target{}); cerr != nil {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodWorkspaceSettings + ": " + cerr.Error()}
	}

	id := domain.WorkspaceID(p.WorkspaceID)
	var workspaceRuns []*domain.Run
	if s.cfg.Control != nil {
		var listErr error
		workspaceRuns, listErr = s.cfg.Store.ListRunsByWorkspace(ctx, id)
		if listErr != nil {
			return nil, rpcError(listErr)
		}
	}
	update := func() error { return s.cfg.Store.SetWorkspaceSteerOthers(ctx, id, p.SteerOthers) }
	if s.cfg.Control == nil {
		if updateErr := update(); updateErr != nil {
			return nil, rpcError(updateErr)
		}
	} else {
		updated := false
		for _, run := range workspaceRuns {
			if run.Status.Terminal() {
				continue
			}
			if _, updateErr := s.cfg.Control.AdmitRevoke(string(run.ID), func() error {
				if updated {
					return nil
				}
				if applyErr := update(); applyErr != nil {
					return applyErr
				}
				updated = true
				return nil
			}); updateErr != nil {
				return nil, rpcError(updateErr)
			}
		}
		if !updated {
			if updateErr := update(); updateErr != nil {
				return nil, rpcError(updateErr)
			}
		}
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	setting := p.SteerOthers
	if setting == "" {
		setting = "default"
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: ws.ID,
		ActorID:     member,
		Payload: events.TimelinePayload{
			Kind:    events.TimelineNote,
			Message: "workspace settings: steer_others set to " + setting,
		},
	})
	return protocol.WorkspaceSettingsResult{Workspace: protocol.WorkspaceFromDomain(ws)}, nil
}

// workspaceOrigin sets the upstream git URL the workspace's run checkouts
// push to (collaborator or admin; the guard has already checked Push
// against the workspace). This is the one place an origin is normalized,
// so every caller records the same URL. The change is stamped into the
// workspace timeline attributed to the caller.
func (s *Server) workspaceOrigin(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.WorkspaceOriginParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	// The raw input is what gets validated: normalization would turn an
	// option-shaped scp form into an innocent https URL, and the caller
	// should hear that the input was refused.
	if !domain.ValidOrigin(p.Origin) {
		return nil, invalidParams("origin must be empty or a git URL (https://, http://, ssh://, git://, an absolute path, or user@host:path)")
	}
	origin := domain.NormalizeOrigin(p.Origin)
	id := domain.WorkspaceID(p.WorkspaceID)
	if err := s.cfg.Store.SetWorkspaceOrigin(ctx, id, origin); err != nil {
		return nil, rpcError(err)
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	note := "workspace origin cleared"
	if ws.Origin != "" {
		note = "workspace origin set to " + ws.Origin
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: ws.ID,
		ActorID:     member,
		Payload: events.TimelinePayload{
			Kind:    events.TimelineNote,
			Message: note,
		},
	})
	return protocol.WorkspaceOriginResult{Workspace: protocol.WorkspaceFromDomain(ws)}, nil
}

// runProtect toggles a run's protected flag (owner or admin; the guard has
// already checked Protect against the run). The change is stamped into the
// workspace timeline attributed to the caller.
func (s *Server) runProtect(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunProtectParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" {
		return nil, invalidParams("run_id is required")
	}
	id := domain.RunID(p.RunID)
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	target, err := resolveRunTarget(ctx, s.cfg.Store, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if cerr := permissions.Check(permissions.Protect, actor, target); cerr != nil {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunProtect + ": " + cerr.Error()}
	}

	if p.Protected {
		if rooms := s.cfg.Services.Rooms; rooms != nil {
			if protectErr := rooms.Protect(ctx, id, member); protectErr != nil {
				return nil, collaborationRPCError(protectErr)
			}
		} else {
			update := func() error {
				if atomicStore, ok := s.cfg.Store.(store.RunProtectionStore); ok {
					_, storeErr := atomicStore.SetRunProtectedAndCancelQueuedSteerRequests(ctx, id, true, member, time.Now().UTC())
					return storeErr
				}
				return s.cfg.Store.SetRunProtected(ctx, id, true)
			}
			if s.cfg.Control != nil {
				if _, protectErr := s.cfg.Control.AdmitRevoke(string(id), update); protectErr != nil {
					return nil, rpcError(protectErr)
				}
			} else if updateErr := update(); updateErr != nil {
				return nil, rpcError(updateErr)
			}
		}
	} else {
		update := func() error {
			if atomicStore, ok := s.cfg.Store.(store.RunProtectionStore); ok {
				_, storeErr := atomicStore.SetRunProtectedAndCancelQueuedSteerRequests(ctx, id, false, member, time.Now().UTC())
				return storeErr
			}
			return s.cfg.Store.SetRunProtected(ctx, id, false)
		}
		if s.cfg.Control != nil {
			if _, updateErr := s.cfg.Control.AdmitRevoke(string(id), update); updateErr != nil {
				return nil, rpcError(updateErr)
			}
		} else if updateErr := update(); updateErr != nil {
			return nil, rpcError(updateErr)
		}
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	var publicationErrs []error
	if s.cfg.Bus != nil {
		if _, publishErr := s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: run.WorkspaceID,
			RunID:       run.ID,
			ActorID:     member,
			Payload:     events.RunProtectedPayload{Protected: p.Protected},
		}); publishErr != nil {
			publicationErrs = append(publicationErrs, fmt.Errorf("publish run protection refresh event: %w", publishErr))
		}
		msg := "run protection disabled"
		if run.Protected {
			msg = "run protection enabled"
		}
		if _, publishErr := s.cfg.Bus.Publish(ctx, events.Event{
			WorkspaceID: run.WorkspaceID,
			RunID:       run.ID,
			ActorID:     member,
			Payload:     events.TimelinePayload{Kind: events.TimelineNote, Message: msg},
		}); publishErr != nil {
			publicationErrs = append(publicationErrs, fmt.Errorf("publish run protection timeline event: %w", publishErr))
		}
	}
	if len(publicationErrs) != 0 {
		return nil, rpcError(errors.Join(publicationErrs...))
	}
	return protocol.RunResult{Run: protocol.RunFromDomain(run)}, nil
}
