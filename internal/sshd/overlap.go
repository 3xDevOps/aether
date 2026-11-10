package sshd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodRunOverlaps, (*Server).runOverlaps)
}

func (s *Server) runOverlaps(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	idx := s.cfg.Services.Overlaps
	if idx == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run.overlaps: conflict radar is not enabled"}
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	entries, err := idx.Overlaps(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	reached := map[domain.RunID]bool{}
	reaches := func(id domain.RunID) (bool, error) {
		if ok, seen := reached[id]; seen {
			return ok, nil
		}
		run, err := s.cfg.Store.GetRun(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			reached[id] = false
		case err != nil:
			return false, err
		default:
			reached[id] = actor.Reaches(run.WorkspaceID)
		}
		return reached[id], nil
	}
	out := make([]protocol.Overlap, 0, len(entries))
	for _, e := range entries {
		ok, err := reaches(e.RunID)
		if err != nil {
			return nil, rpcError(err)
		}
		if !ok {
			continue
		}
		peers := make([]protocol.OverlapPeer, 0, len(e.With))
		for _, p := range e.With {
			ok, err := reaches(p.RunID)
			if err != nil {
				return nil, rpcError(err)
			}
			if !ok {
				continue
			}
			peers = append(peers, protocol.OverlapPeer{
				RunID:    string(p.RunID),
				MemberID: string(p.MemberID),
				Files:    p.Files,
			})
		}
		if len(peers) == 0 {
			continue
		}
		out = append(out, protocol.Overlap{RunID: string(e.RunID), With: peers})
	}
	return protocol.RunOverlapsResult{Overlaps: out}, nil
}
