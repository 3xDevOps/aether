package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodMemberGit, (*Server).memberGit)
}

// memberGit sets the git identity commits are authored as. Members may set
// their own; setting anyone else's requires the admin role. An empty name
// or email clears that half back to its fallback.
func (s *Server) memberGit(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.MemberGitParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Name != "" && !domain.ValidGitName(p.Name) {
		return nil, invalidParams("invalid git name: no angle brackets, line breaks, or surrounding spaces")
	}
	if p.Email != "" && !domain.ValidGitEmail(p.Email) {
		return nil, invalidParams("invalid git email: want one address of the form user@example.com")
	}
	target := member
	if p.MemberID != "" && p.MemberID != string(member) {
		target = domain.MemberID(p.MemberID)
		if aerr := s.requireAdmin(ctx, member, protocol.MethodMemberGit); aerr != nil {
			return nil, aerr
		}
	}
	m, gerr := s.cfg.Store.GetMember(ctx, target)
	if gerr != nil {
		return nil, rpcError(gerr)
	}
	if uerr := s.cfg.Store.UpdateMemberGitIdentity(ctx, target, p.Name, p.Email); uerr != nil {
		return nil, rpcError(uerr)
	}
	m.GitName, m.GitEmail = p.Name, p.Email
	return protocol.MemberGitResult{Member: protocol.MemberFromDomain(m)}, nil
}
