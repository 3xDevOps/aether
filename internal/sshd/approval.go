package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/3xDevOps/Aether/internal/approvals"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodApprovalList, (*Server).approvalList)
	registerGuarded(protocol.MethodApprovalDecide, permissions.Steer, runTarget, (*Server).approvalDecide)
	registerMethod(protocol.MethodPresenceHeartbeat, (*Server).presenceHeartbeat)
	registerMethod(protocol.MethodPresenceRoster, (*Server).presenceRoster)
}

func (s *Server) approvals() (ApprovalService, *protocol.Error) {
	if s.cfg.Services.Approvals == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "approval service not configured"}
	}
	return s.cfg.Services.Approvals, nil
}

func (s *Server) approvalList(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.approvals()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.ApprovalListParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.SessionID == "" {
		return nil, invalidParams("session_id is required")
	}
	if _, err := s.cfg.Store.GetSession(ctx, domain.SessionID(p.SessionID)); err != nil {
		return nil, rpcError(err)
	}
	list, err := svc.List(ctx, domain.SessionID(p.SessionID), p.All)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.ApprovalListResult{Approvals: make([]protocol.Approval, 0, len(list))}
	for _, a := range list {
		out.Approvals = append(out.Approvals, approvalWire(a))
	}
	return out, nil
}

func (s *Server) approvalDecide(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.approvals()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.ApprovalDecideParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RequestID == "" {
		return nil, invalidParams("request_id is required")
	}
	decided, err := svc.Decide(ctx, p.RequestID, domain.RunID(p.RunID), p.Approve, member)
	if err != nil {
		if errors.Is(err, approvals.ErrRunMismatch) {
			return nil, invalidParams(err.Error())
		}
		return nil, rpcError(err)
	}
	return protocol.ApprovalDecideResult{Approval: approvalWire(decided)}, nil
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
	if p.SessionID == "" {
		return nil, invalidParams("session_id is required")
	}
	if _, err := s.cfg.Store.GetSession(ctx, domain.SessionID(p.SessionID)); err != nil {
		return nil, rpcError(err)
	}
	if err := svc.Heartbeat(ctx, member, domain.SessionID(p.SessionID)); err != nil {
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
	present := svc.Roster(domain.SessionID(p.SessionID), domain.RunID(p.RunID))
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

func approvalWire(a *store.Approval) protocol.Approval {
	w := protocol.Approval{
		ID:        a.ID,
		SessionID: string(a.SessionID),
		RunID:     string(a.RunID),
		Action:    a.Action,
		Detail:    a.Detail,
		Decision:  a.Decision,
		DecidedBy: string(a.DecidedBy),
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.DecidedAt != nil {
		t := a.DecidedAt.UTC().Format(time.RFC3339)
		w.DecidedAt = &t
	}
	return w
}
