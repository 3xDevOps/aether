package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodPresenceHeartbeat, permissions.View, workspaceTarget, (*Server).presenceHeartbeat)
	registerMethod(protocol.MethodPresenceRoster, (*Server).presenceRoster)
	registerGuarded(protocol.MethodPresenceTyping, permissions.Steer, runTarget, (*Server).presenceTyping)
}

func (s *Server) approvals() (ApprovalService, *protocol.Error) {
	if s.cfg.Services.Approvals == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "presence service not configured"}
	}
	return s.cfg.Services.Approvals, nil
}

func (s *Server) presenceHeartbeat(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.approvals()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.PresenceHeartbeatParams](params)
	if perr != nil {
		return nil, perr
	}
	if err := svc.Heartbeat(ctx, member, domain.WorkspaceID(p.WorkspaceID)); err != nil {
		return nil, rpcError(err)
	}
	return protocol.PresenceHeartbeatResult{TTLSeconds: int(svc.TTL() / time.Second)}, nil
}

func (s *Server) presenceTyping(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.PresenceTypingParams](params)
	if perr != nil {
		return nil, perr
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
		ActorID:     member,
		Payload:     events.TypingPayload{Stopped: p.Stopped},
	}); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}

func (s *Server) presenceRoster(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.approvals()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.PresenceRosterParams](params)
	if perr != nil {
		return nil, perr
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	runs := newRunScopes(s.cfg.Store)
	if p.RunID != "" && !runs.reaches(ctx, actor, domain.RunID(p.RunID)) {
		return protocol.PresenceRosterResult{Members: []protocol.PresenceEntry{}}, nil
	}
	present := svc.Roster(domain.WorkspaceID(p.WorkspaceID), domain.RunID(p.RunID))
	out := protocol.PresenceRosterResult{Members: make([]protocol.PresenceEntry, 0, len(present))}
	for _, m := range present {
		if !actor.Reaches(m.Workspace) {
			continue
		}
		e := protocol.PresenceEntry{
			MemberID: string(m.Member),
			State:    string(m.State),
			LastSeen: m.LastSeen.UTC().Format(time.RFC3339),
		}
		for _, run := range m.Watching {
			if runs.reaches(ctx, actor, run) {
				e.Watching = append(e.Watching, string(run))
			}
		}
		out.Members = append(out.Members, e)
	}
	return out, nil
}
