package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// route names the first route that lets sender open a conversation with
// target: its swarm assignment, the radar's active/grace set, or the
// workspace. Every route requires a sender owner who may message; only the
// workspace route honors protection. Callers have checked that both runs
// share a workspace and that target is live.
func (s *Service) route(ctx context.Context, method string, sender, target *domain.Run) (string, *protocol.Error) {
	owner, reason, err := s.messagingOwner(ctx, sender)
	if err != nil {
		return "", internalError(method, err)
	}
	if owner == nil {
		return "", deniedPeer(method, sender.ID, target.ID, reason)
	}
	member, err := s.missionMember(ctx, sender.ID, target.ID)
	if err != nil {
		return "", missionRPCError(method, err)
	}
	if member {
		return protocol.CoordPeerMission, nil
	}
	peer, err := s.radar.authorized(ctx, sender.ID, target.ID)
	if err != nil {
		return "", internalError(method, err)
	}
	if peer.state != "" {
		return peer.state, nil
	}
	if err := checkMessage(owner, target.WorkspaceID, target.MemberID, target.Protected); err != nil {
		return "", deniedPeer(method, sender.ID, target.ID, ownerDenial(owner, err))
	}
	return protocol.CoordPeerWorkspace, nil
}

// messagingOwner resolves run's owner for every non-reply route. An owner
// who is gone or whom the Message capability refuses outright (a viewer)
// comes back nil with the reason.
func (s *Service) messagingOwner(ctx context.Context, run *domain.Run) (*domain.Member, string, error) {
	owner, err := s.cfg.Store.GetMember(ctx, run.MemberID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Sprintf("its owner %s is no longer a member", run.MemberID), nil
	}
	if err != nil {
		return nil, "", err
	}
	if err := checkMessage(owner, run.WorkspaceID, "", false); err != nil {
		return nil, ownerDenial(owner, err), nil
	}
	return owner, "", nil
}

func (s *Service) missionMember(ctx context.Context, run, peer domain.RunID) (bool, error) {
	if s.cfg.Mission == nil {
		return false, nil
	}
	assignment, err := s.cfg.Mission.Assignment(ctx, run)
	if err != nil || assignment.MissionID == "" {
		return false, err
	}
	peers, err := s.cfg.Mission.Peers(ctx, run)
	if err != nil {
		return false, err
	}
	for _, p := range peers {
		if domain.RunID(p.RunID) == peer {
			return true, nil
		}
	}
	return false, nil
}

// statusPeers lists exactly the runs self may open a conversation with,
// bounded to CoordMaxStatusPeers, with the unbounded total: live swarm
// peers first, then the radar's active/grace peers, then the other live
// runs in the workspace that self's owner may message, most recently active
// first. A run whose owner may not message lists none.
func (s *Service) statusPeers(ctx context.Context, self *domain.Run, missionPeers []protocol.CoordPeer) ([]protocol.CoordPeer, int, error) {
	peers := make([]protocol.CoordPeer, 0, protocol.CoordMaxStatusPeers)
	owner, _, err := s.messagingOwner(ctx, self)
	if err != nil || owner == nil {
		return peers, 0, err
	}
	// One extra byte tells boundStatusText that the stored task is longer.
	live, err := s.cfg.Store.ListLiveRunSummaries(ctx, self.WorkspaceID, protocol.CoordMaxStatusTaskBytes+1)
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[domain.RunID]*store.RunSummary, len(live))
	for _, r := range live {
		byID[r.ID] = r
	}
	radarPeers, err := s.radar.authorizedSet(ctx, self.ID)
	if err != nil {
		return nil, 0, err
	}
	overlaps := make(map[domain.RunID]authorizedPeer, len(radarPeers))
	for _, p := range radarPeers {
		overlaps[p.run] = p
	}
	seen := make(map[domain.RunID]bool)
	add := func(r *store.RunSummary, state string, overlap authorizedPeer) {
		if r == nil || r.ID == self.ID || seen[r.ID] {
			return
		}
		seen[r.ID] = true
		if len(peers) == protocol.CoordMaxStatusPeers {
			return
		}
		task, _, _ := boundStatusText(r.Task)
		peer := protocol.CoordPeer{
			RunID: string(r.ID), MemberID: string(r.MemberID), Task: task, TaskBytes: r.TaskBytes,
			TaskTruncated: r.TaskBytes > protocol.CoordMaxStatusTaskBytes, State: state, RunStatus: string(r.Status),
		}
		if overlap.state != "" {
			peer.Files, peer.FileTotal, peer.FilesTruncated = boundStatusFiles(overlap.files)
		}
		if state == protocol.CoordPeerGrace {
			peer.ExpiresAt = overlap.expiry.UTC().Format(time.RFC3339)
		}
		peers = append(peers, peer)
	}
	for _, p := range missionPeers {
		id := domain.RunID(p.RunID)
		add(byID[id], protocol.CoordPeerMission, overlaps[id])
	}
	for _, p := range radarPeers {
		add(byID[p.run], p.state, p)
	}
	for _, r := range live {
		if checkMessage(owner, self.WorkspaceID, r.MemberID, r.Protected) == nil {
			add(r, protocol.CoordPeerWorkspace, authorizedPeer{})
		}
	}
	return peers, len(seen), nil
}

func checkMessage(owner *domain.Member, workspace domain.WorkspaceID, targetOwner domain.MemberID, protected bool) error {
	return permissions.Check(permissions.Message,
		permissions.Actor{ID: owner.ID, Role: owner.Role},
		permissions.Target{Workspace: workspace, Owner: targetOwner, Protected: protected})
}

func ownerDenial(owner *domain.Member, err error) string {
	return fmt.Sprintf("member %s (%s) may not message it: %s",
		owner.ID, owner.Role, strings.TrimPrefix(err.Error(), permissions.ErrDenied.Error()+": "))
}

func deniedPeer(method string, from, to domain.RunID, reason string) *protocol.Error {
	return &protocol.Error{
		Code: protocol.CodeDenied,
		Message: fmt.Sprintf("%s: run %s is not an authorized peer of run %s: %s; "+
			"run `aether-internal status` to list the runs it can message", method, to, from, reason),
	}
}
