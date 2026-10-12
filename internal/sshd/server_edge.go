package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodServerEdge, (*Server).serverEdge)
	registerMethod(protocol.MethodServerEdgeClaimCode, (*Server).serverEdgeClaimCode)
}

func (s *Server) serverEdge(context.Context, domain.MemberID, json.RawMessage) (any, *protocol.Error) {
	out := protocol.ServerEdgeResult{ServerID: edgeproto.ServerID(s.hostKey.PublicKey()), AccessPolicy: string(s.cfg.EdgeAccess)}
	edge := s.currentEdgeOwner()
	if edge == nil {
		return out, nil
	}
	owner, err := edge.Owner()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: protocol.MethodServerEdge + ": " + err.Error()}
	}
	out.EdgeURL, out.Connected, out.Claimed = edge.Origin(), edge.Connected(), edge.EdgeState() == edgeproto.StateClaimed
	if owner != nil {
		out.Owner = &protocol.EdgeAccount{Provider: owner.Provider, Login: owner.Login}
	}
	return out, nil
}

// serverEdgeClaimCode issues the calling admin a claim code bound to their
// own member, so an admin who joined over an SSH key or a tailnet can
// connect their GitHub account and give the server an owner. The claim
// creates no member and raises no role.
func (s *Server) serverEdgeClaimCode(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodServerEdgeClaimCode
	if perr := s.requireAdmin(ctx, member, method); perr != nil {
		return nil, perr
	}
	if perr := s.requireApprovedCaller(ctx, method, true); perr != nil {
		return nil, perr
	}
	edge := s.currentEdgeOwner()
	if edge == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": this server is not enrolled with an edge (edge-url is empty)"}
	}
	owner, err := edge.Owner()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": " + err.Error()}
	}
	if owner != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: this server is already owned at its edge by %s %s; aether member transfer moves ownership", method, owner.Provider, owner.Login)}
	}
	code, expires, err := edge.IssueClaimCode(member)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": " + err.Error()}
	}
	slog.Info("sshd: an admin issued a claim code for themself", "member", member)
	return protocol.ServerEdgeClaimCodeResult{Code: code, ExpiresAt: expires.UTC().Format(time.RFC3339)}, nil
}

// requireEdgeClaimed refuses method while the edge last reported this
// server as unclaimed: such an edge relays nothing for it and keeps no
// directory, so an invitation or a link could never be used. A claimed
// server that lost its owner keeps relaying for its members and passes.
func (s *Server) requireEdgeClaimed(method string) *protocol.Error {
	edge := s.currentEdgeOwner()
	if edge == nil || edge.EdgeState() != edgeproto.StateUnclaimed {
		return nil
	}
	return &protocol.Error{Code: protocol.CodeInvalidState, Message: method + ": the edge has not recorded this server yet, " +
		"so it relays nothing for it and keeps no directory; connect your GitHub account first: " +
		"Settings › Remote access › Connect my GitHub account in the dashboard, or aether edge connect"}
}
