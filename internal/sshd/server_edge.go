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
	out.EdgeURL, out.Connected, out.Claimed = edge.Origin(), edge.Connected(), edge.EdgeState() == edgeproto.StateClaimed
	if owner != nil {
		out.Owner = &protocol.EdgeAccount{Provider: owner.Provider, Login: owner.Login}
	}
	return out, nil
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
