package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodServerEdge, (*Server).serverEdge)
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
	out.EdgeURL, out.Connected = edge.Origin(), edge.Connected()
	if owner != nil {
		out.Owner = &protocol.EdgeAccount{Provider: owner.Provider, Subject: owner.Subject, Login: owner.Login, Email: owner.Email}
	}
	return out, nil
}

// requireEdgeOwner refuses method while the server is enrolled with an
// edge that records no owner for it: such an edge relays nothing and
// keeps no directory, so an invitation or a link could never be used.
func (s *Server) requireEdgeOwner(method string) *protocol.Error {
	edge := s.currentEdgeOwner()
	if edge == nil {
		return nil
	}
	owner, err := edge.Owner()
	if err != nil {
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": " + err.Error()}
	}
	if owner == nil {
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: method + ": this server has no owner at its edge, " +
			"so the edge relays nothing for it and keeps no directory; connect your GitHub account first: " +
			"Settings › Remote access › Connect my GitHub account in the dashboard, or aether edge connect"}
	}
	return nil
}
