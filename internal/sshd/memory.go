package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// MemoryService is team memory as members reach it; *memory.Service
// satisfies it. Writes re-check the member's push permission themselves.
type MemoryService interface {
	Add(ctx context.Context, member domain.MemberID, p protocol.MemoryAddParams) (protocol.MemoryAddResult, error)
	Search(ctx context.Context, p protocol.MemorySearchParams) (protocol.MemorySearchResult, error)
	Show(ctx context.Context, p protocol.MemoryShowParams) (protocol.MemoryShowResult, error)
	Brief(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID, self domain.RunID) (string, error)
	Pin(ctx context.Context, member domain.MemberID, p protocol.MemoryPinParams) (protocol.MemoryPinResult, error)
	Unpin(ctx context.Context, member domain.MemberID, p protocol.MemoryUnpinParams) (protocol.MemoryUnpinResult, error)
	Remove(ctx context.Context, member domain.MemberID, p protocol.MemoryRmParams) (protocol.MemoryRmResult, error)
}

func init() {
	registerMemory(protocol.MethodMemorySearch, permissions.View,
		func(m MemoryService, ctx context.Context, _ domain.MemberID, p protocol.MemorySearchParams) (any, error) {
			return m.Search(ctx, p)
		})
	registerMemory(protocol.MethodMemoryShow, permissions.View,
		func(m MemoryService, ctx context.Context, _ domain.MemberID, p protocol.MemoryShowParams) (any, error) {
			return m.Show(ctx, p)
		})
	registerMemory(protocol.MethodMemoryAdd, permissions.Push, MemoryService.Add)
	registerMemory(protocol.MethodMemoryPin, permissions.Push, MemoryService.Pin)
	registerMemory(protocol.MethodMemoryUnpin, permissions.Push, MemoryService.Unpin)
	registerMemory(protocol.MethodMemoryRm, permissions.Push, MemoryService.Remove)
	registerGuarded(protocol.MethodMemoryBrief, permissions.View, workspaceTarget, (*Server).memoryBrief)
}

// registerMemory serves one team memory method on a workspace behind cap.
func registerMemory[P, R any](method string, cap permissions.Capability, call func(MemoryService, context.Context, domain.MemberID, P) (R, error)) {
	registerGuarded(method, cap, workspaceTarget, func(s *Server, ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
		svc, perr := s.memory(method)
		if perr != nil {
			return nil, perr
		}
		p, perr := decodeParams[P](raw)
		if perr != nil {
			return nil, perr
		}
		out, err := call(svc, ctx, member, p)
		if err != nil {
			return nil, rpcError(err)
		}
		return out, nil
	})
}

func (s *Server) memory(method string) (MemoryService, *protocol.Error) {
	if s.cfg.Services.Memory == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": team memory is not available"}
	}
	return s.cfg.Services.Memory, nil
}

// memoryBrief builds the brief a new run of the member would get: the
// caller's, or member_id's.
func (s *Server) memoryBrief(ctx context.Context, caller domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.memory(protocol.MethodMemoryBrief)
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemoryBriefParams](raw)
	if perr != nil {
		return nil, perr
	}
	member := caller
	if p.MemberID != "" {
		m, err := s.cfg.Store.GetMember(ctx, domain.MemberID(p.MemberID))
		if err != nil {
			return nil, rpcError(err)
		}
		member = m.ID
	}
	text, err := svc.Brief(ctx, domain.WorkspaceID(p.WorkspaceID), member, "")
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.MemoryBriefResult{Text: text}, nil
}
