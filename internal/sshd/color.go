package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodMemberColor, (*Server).memberColor)
}

// memberColor sets a member's attribution color. Members may recolor
// themselves; recoloring anyone else is for a super admin, or an admin
// recoloring a member below admin.
func (s *Server) memberColor(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.MemberColorParams](params)
	if perr != nil {
		return nil, perr
	}
	color, err := attribution.Normalize(p.Color)
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	target := member
	if p.MemberID != "" {
		target = domain.MemberID(p.MemberID)
	}
	if aerr := s.editing(ctx, member, target, protocol.MethodMemberColor); aerr != nil {
		return nil, aerr
	}
	m, gerr := s.cfg.Store.GetMember(ctx, target)
	if gerr != nil {
		return nil, rpcError(gerr)
	}
	if uerr := s.cfg.Store.UpdateMemberColor(ctx, target, color); uerr != nil {
		return nil, rpcError(uerr)
	}
	m.Color = color
	return protocol.MemberColorResult{Member: protocol.MemberFromDomain(m)}, nil
}
