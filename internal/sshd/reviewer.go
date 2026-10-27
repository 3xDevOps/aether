package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodMemberReviewerLink, (*Server).memberReviewerLink)
}

// memberReviewerLink binds the edge's reviewer account to a member. The
// reviewer signs in with a password the edge's operator shares with an
// app directory, never through an invitation, so an admin binds it
// directly, and only to a member who is no admin.
func (s *Server) memberReviewerLink(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodMemberReviewerLink
	if perr := s.requireApprovedCaller(ctx, method, true); perr != nil {
		return nil, perr
	}
	caller, err := s.cfg.Store.GetMember(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	if caller.Role != domain.RoleAdmin {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: method + ": admins only"}
	}
	if !s.cfg.EdgeReviewer {
		return nil, &protocol.Error{Code: protocol.CodeDenied, Message: method + ": edge-reviewer is off, so this server takes no reviewer account; " +
			"an admin turns it on on the server's host with sudo aether-server config set edge-reviewer true, then sudo systemctl restart aether-server"}
	}
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.MemberReviewerLinkParams](params)
	if perr != nil {
		return nil, perr
	}
	if !edgeproto.ValidReviewerLogin(p.Login) {
		return nil, invalidParams(fmt.Sprintf("login %q is not 1 to 39 lowercase letters, digits and dashes, as in the edge's reviewer account file", p.Login))
	}
	target, err := s.cfg.Store.GetMember(ctx, domain.MemberID(p.MemberID))
	if err != nil {
		return nil, rpcError(err)
	}
	if target.Role == domain.RoleAdmin {
		return nil, invalidParams(fmt.Sprintf("member %s is an admin; bind the reviewer account to a viewer or collaborator", target.ID))
	}
	id := &domain.Identity{Member: target.ID, Provider: edgeproto.ProviderReviewer, Subject: p.Login, Login: p.Login, CreatedAt: time.Now().UTC()}
	err = ids.BindIdentity(ctx, id)
	if errors.Is(err, store.ErrConflict) {
		bound, gerr := ids.GetMemberByIdentity(ctx, id.Provider, id.Subject)
		if gerr != nil {
			return nil, rpcError(gerr)
		}
		return nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf(
			"%s: %s:%s is bound to member %s already; unbind it with aether member unlink %s %s:%s",
			method, id.Provider, id.Subject, bound.ID, bound.ID, id.Provider, id.Subject)}
	}
	if err != nil {
		return nil, rpcError(err)
	}
	slog.Info("sshd: reviewer account bound", "actor", member, "member", target.ID, "login", p.Login)
	return protocol.Identity{Provider: id.Provider, Subject: id.Subject, Login: id.Login,
		CreatedAt: id.CreatedAt.Format(time.RFC3339)}, nil
}
