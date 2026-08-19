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
// themselves; recoloring anyone else requires the admin role.
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
	if p.MemberID != "" && p.MemberID != string(member) {
		target = domain.MemberID(p.MemberID)
		if aerr := s.requireAdmin(ctx, member, protocol.MethodMemberColor); aerr != nil {
			return nil, aerr
		}
	}
	m, gerr := s.cfg.Store.GetMember(ctx, target)
	if gerr != nil {
		return nil, rpcError(gerr)
	}
	m.Color = color
	if uerr := s.cfg.Store.UpdateMember(ctx, m); uerr != nil {
		return nil, rpcError(uerr)
	}
	return protocol.MemberColorResult{Member: protocol.MemberFromDomain(m)}, nil
}
