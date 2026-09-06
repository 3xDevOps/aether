package sshd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodAccountList, (*Server).accountList)
	registerMethod(protocol.MethodAccountShare, (*Server).accountShare)
	registerMethod(protocol.MethodAccountRevoke, (*Server).accountRevoke)
}

// launchAccount resolves the account a caller may use. Access is opt-in and
// directional: an admin has no implicit right to another member's credentials.
func (s *Server) launchAccount(ctx context.Context, actor domain.MemberID, requested string) (domain.MemberID, *protocol.Error) {
	account := domain.MemberID(requested)
	if account == "" || account == actor {
		return actor, nil
	}
	owner, err := s.cfg.Store.GetMember(ctx, account)
	if err != nil {
		return "", rpcError(err)
	}
	if owner.Pending {
		return "", &protocol.Error{Code: protocol.CodeDenied, Message: "account owner is pending admin approval"}
	}
	shared, err := s.cfg.Store.AccountSharedWith(ctx, account, actor)
	if err != nil {
		return "", rpcError(err)
	}
	if !shared {
		return "", &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf("member %s has not shared their account with you", account)}
	}
	return account, nil
}

func (s *Server) accountList(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	self, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	owners, err := s.cfg.Store.ListAccountOwners(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	grantees, err := s.cfg.Store.ListAccountGrantees(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	accounts := []protocol.Member{protocol.MemberFromDomain(self)}
	for _, owner := range owners {
		if !owner.Pending {
			accounts = append(accounts, protocol.MemberFromDomain(owner))
		}
	}
	sharedWith := make([]protocol.Member, 0, len(grantees))
	for _, grantee := range grantees {
		if !grantee.Pending {
			sharedWith = append(sharedWith, protocol.MemberFromDomain(grantee))
		}
	}
	return protocol.AccountListResult{Accounts: accounts, SharedWith: sharedWith}, nil
}

func accountMemberParams(raw json.RawMessage) (domain.MemberID, *protocol.Error) {
	p, perr := decodeParams[protocol.AccountMemberParams](raw)
	if perr != nil {
		return "", perr
	}
	if p.MemberID == "" {
		return "", invalidParams("member_id is required")
	}
	return domain.MemberID(p.MemberID), nil
}

func (s *Server) accountShare(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	grantee, perr := accountMemberParams(raw)
	if perr != nil {
		return nil, perr
	}
	if grantee == member {
		return nil, invalidParams("cannot share an account with yourself")
	}
	target, err := s.cfg.Store.GetMember(ctx, grantee)
	if err != nil {
		return nil, rpcError(err)
	}
	if target.Pending {
		return nil, invalidParams("cannot share an account with a member pending admin approval")
	}
	if err := s.cfg.Store.ShareAccount(ctx, member, grantee); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}

func (s *Server) accountRevoke(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	grantee, perr := accountMemberParams(raw)
	if perr != nil {
		return nil, perr
	}
	if err := s.cfg.Store.RevokeAccountShare(ctx, member, grantee); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, nil
}
