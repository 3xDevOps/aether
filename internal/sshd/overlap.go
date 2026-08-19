package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerMethod(protocol.MethodRunOverlaps, (*Server).runOverlaps)
}

func (s *Server) runOverlaps(ctx context.Context, _ domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	idx := s.cfg.Services.Overlaps
	if idx == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.overlaps: conflict radar is not enabled"}
	}
	entries, err := idx.Overlaps(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]protocol.Overlap, 0, len(entries))
	for _, e := range entries {
		peers := make([]protocol.OverlapPeer, 0, len(e.With))
		for _, p := range e.With {
			peers = append(peers, protocol.OverlapPeer{
				RunID:    string(p.RunID),
				MemberID: string(p.MemberID),
				Files:    p.Files,
			})
		}
		out = append(out, protocol.Overlap{RunID: string(e.RunID), With: peers})
	}
	return protocol.RunOverlapsResult{Overlaps: out}, nil
}
