package sshd

import (
	"context"
	"encoding/json"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodPresenceHeartbeat, (*Server).presenceHeartbeat)
	registerMethod(protocol.MethodPresenceRoster, (*Server).presenceRoster)
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
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	if _, err := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(p.WorkspaceID)); err != nil {
		return nil, rpcError(err)
	}
	if err := svc.Heartbeat(ctx, member, domain.WorkspaceID(p.WorkspaceID)); err != nil {
		return nil, rpcError(err)
	}
	return protocol.PresenceHeartbeatResult{TTLSeconds: int(svc.TTL() / time.Second)}, nil
}

func (s *Server) presenceRoster(_ context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.approvals()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.PresenceRosterParams](params)
	if perr != nil {
		return nil, perr
	}
	present := svc.Roster(domain.WorkspaceID(p.WorkspaceID), domain.RunID(p.RunID))
	out := protocol.PresenceRosterResult{Members: make([]protocol.PresenceEntry, 0, len(present))}
	for _, m := range present {
		e := protocol.PresenceEntry{
			MemberID: string(m.Member),
			State:    string(m.State),
			LastSeen: m.LastSeen.UTC().Format(time.RFC3339),
		}
		for _, run := range m.Watching {
			e.Watching = append(e.Watching, string(run))
		}
		out.Members = append(out.Members, e)
	}
	return out, nil
}
