package coord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// route names the first route that lets sender open a conversation with
// target: its swarm assignment, the radar's active/grace set, or the
// workspace. Callers have checked that both runs share a workspace and
// that target is live.
func (s *Service) route(ctx context.Context, method string, sender, target *domain.Run) (string, *protocol.Error) {
	if s.cfg.Mission != nil {
		assignment, err := s.cfg.Mission.Assignment(ctx, sender.ID)
		if err != nil {
			return "", missionRPCError(method, err)
		}
		if assignment.MissionID != "" {
			peers, err := s.cfg.Mission.Peers(ctx, sender.ID)
			if err != nil {
				return "", missionRPCError(method, err)
			}
			for _, peer := range peers {
				if domain.RunID(peer.RunID) == target.ID {
					return protocol.CoordPeerMission, nil
				}
			}
		}
	}
	peer, err := s.radar.authorized(ctx, sender.ID, target.ID)
	if err != nil {
		return "", internalError(method, err)
	}
	if peer.state != "" {
		return peer.state, nil
	}
	owner, err := s.cfg.Store.GetMember(ctx, sender.MemberID)
	if errors.Is(err, store.ErrNotFound) {
		return "", deniedPeer(method, sender.ID, target.ID,
			fmt.Sprintf("its owner %s is no longer a member", sender.MemberID))
	}
	if err != nil {
		return "", internalError(method, err)
	}
	if err := checkMessage(owner, target); err != nil {
		return "", deniedPeer(method, sender.ID, target.ID, fmt.Sprintf("member %s (%s) may not message it: %s",
			owner.ID, owner.Role, strings.TrimPrefix(err.Error(), permissions.ErrDenied.Error()+": ")))
	}
	return protocol.CoordPeerWorkspace, nil
}

func checkMessage(owner *domain.Member, target *domain.Run) error {
	return permissions.Check(permissions.Message,
		permissions.Actor{ID: owner.ID, Role: owner.Role},
		permissions.Target{Workspace: target.WorkspaceID, Owner: target.MemberID, Protected: target.Protected})
}

func deniedPeer(method string, from, to domain.RunID, reason string) *protocol.Error {
	return &protocol.Error{
		Code: protocol.CodeDenied,
		Message: fmt.Sprintf("%s: run %s is not an authorized peer of run %s: %s; "+
			"run `aether-internal status` to list the runs it can message", method, to, from, reason),
	}
}

// workspacePeers returns the live runs in self's workspace that self's
// owner may message and that listed does not already cover, most recently
// active first.
func (s *Service) workspacePeers(ctx context.Context, self *domain.Run, listed func(domain.RunID) bool) ([]*domain.Run, error) {
	owner, err := s.cfg.Store.GetMember(ctx, self.MemberID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	runs, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Run, 0, len(runs))
	for _, r := range runs {
		if r.ID == self.ID || r.WorkspaceID != self.WorkspaceID || r.Status.Terminal() ||
			listed(r.ID) || checkMessage(owner, r) != nil {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(a, b int) bool { return lastActive(out[a]).After(lastActive(out[b])) })
	return out, nil
}

func lastActive(r *domain.Run) time.Time {
	if r.StatusChangedAt != nil && r.StatusChangedAt.After(r.CreatedAt) {
		return *r.StatusChangedAt
	}
	return r.CreatedAt
}
