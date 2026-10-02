package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodServerOwnerTransfer, (*Server).serverOwnerTransfer)
}

// EdgeOwner is the edge agent's record of who owns this server at its
// edge.
type EdgeOwner interface {
	// TransferOwner records owner as the server's owner and reports it to
	// the edge. It refuses when the server has no owner: only a claim
	// code from the machine's console gives it one.
	TransferOwner(owner edgeproto.Account) error
}

// SetEdgeOwner connects the edge agent that records ownership. Call it
// before Serve.
func (s *Server) SetEdgeOwner(o EdgeOwner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edgeOwner = o
}

func (s *Server) currentEdgeOwner() EdgeOwner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edgeOwner
}

// serverOwnerTransfer makes another admin, through one of their edge
// identities, the server's owner at its edge. It never creates an admin
// and never changes a role: the new owner must already be an admin.
func (s *Server) serverOwnerTransfer(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodServerOwnerTransfer
	if perr := s.requireAdmin(ctx, member, method); perr != nil {
		return nil, perr
	}
	if perr := s.requireApprovedCaller(ctx, method, false); perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.ServerOwnerTransferParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.MemberID == "" {
		return nil, invalidParams("member_id is required")
	}
	owner := s.currentEdgeOwner()
	if owner == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": this server is not enrolled with an edge (edge-url is empty)"}
	}
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return nil, perr
	}
	// No registerMu here: a claim holds the edge state lock that
	// TransferOwner takes while it waits for registerMu. A role change or
	// removal that lands after these checks has the effect it would have
	// landing just after the transfer.
	target, err := s.cfg.Store.GetMember(ctx, domain.MemberID(p.MemberID))
	if err != nil {
		return nil, rpcError(err)
	}
	if target.Role != domain.RoleAdmin {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: ownership goes to an admin, and %s is %s; an admin changes their role first with member.role", method, target.ID, target.Role)}
	}
	all, err := ids.ListIdentities(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	var matched []*domain.Identity
	var names, others []string
	for _, id := range all {
		if id.Member != target.ID {
			continue
		}
		if id.Provider != edgeproto.ProviderGitHub {
			others = append(others, id.Provider+":"+id.Subject)
			continue
		}
		matched = append(matched, id)
		names = append(names, id.Provider+":"+id.Subject)
	}
	switch {
	case len(matched) == 0 && len(others) == 0:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: %s has no edge identity; they link one with member.identity.link first", method, target.ID)}
	case len(matched) == 0:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: %s has no GitHub identity, only %s, which cannot sign in at an edge; they link a GitHub account with member.identity.link first",
			method, target.ID, strings.Join(others, ", "))}
	case len(matched) > 1:
		return nil, invalidParams(fmt.Sprintf("%s has the GitHub identities %s and ownership goes to one; remove the others with member.identity.remove",
			target.ID, strings.Join(names, ", ")))
	}
	id := matched[0]
	account := edgeproto.Account{Provider: id.Provider, Subject: id.Subject, Email: id.Email, Login: id.Login}
	if err := owner.TransferOwner(account); err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": " + err.Error()}
	}
	slog.Info("sshd: ownership transferred at the edge", "actor", member, "member", target.ID,
		"provider", id.Provider, "subject", id.Subject)
	return protocol.ServerOwnerTransferResult{MemberID: string(target.ID), Provider: id.Provider,
		Subject: id.Subject, Login: id.Login, Email: id.Email}, nil
}

// requireApprovedCaller refuses method on a connection that authenticated
// with an edge device no person has approved. Approving devices and
// minting invite codes need it under either policy, so that no device is
// approved and no bearer credential is issued on the strength of a
// sign-in alone, and a later switch to approved-devices inherits nothing.
// The other methods that raise privilege or admit a credential
// need it under approved-devices, where signing in admits nothing. A
// member SSH key, a tailnet identity and the tailnet dashboard carry no
// device and pass.
func (s *Server) requireApprovedCaller(ctx context.Context, method string, always bool) *protocol.Error {
	id, ok := ctx.Value(connIdentityKey{}).(connIdentity)
	if !ok || id.device == "" {
		return nil
	}
	if !always && s.cfg.EdgeAccess == edgeproto.PolicyAccount {
		return nil
	}
	ids, perr := s.rpcIdentityStore()
	if perr != nil {
		return perr
	}
	dev, err := ids.GetDevice(ctx, id.device)
	if err != nil {
		return rpcError(err)
	}
	if dev.Status != domain.DeviceApproved {
		return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
			"%s needs an approved device, a member SSH key or a tailnet connection; this connection signed in with device %q, which is %s",
			method, dev.Label, dev.Status)}
	}
	return nil
}
