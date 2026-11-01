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

// authorize lets sender open a conversation with target: its owner may
// message, and unless the runs are parent and child or overlap
// on a file, the target's protection and its workspace's steer-others
// setting allow it. Any
// live run on the server qualifies, in any workspace and tree. Callers have
// checked that target is live.
func (s *Service) authorize(ctx context.Context, method string, sender, target *domain.Run) *protocol.Error {
	owner, reason, err := s.messagingOwner(ctx, sender)
	if err != nil {
		return internalError(method, err)
	}
	if owner == nil {
		return deniedPeer(method, sender.ID, target.ID, reason)
	}
	if s.cfg.Children != nil {
		related, relErr := s.cfg.Children.Related(ctx, sender.ID, target.ID)
		if relErr != nil {
			return internalError(method, relErr)
		}
		if related {
			return nil
		}
	}
	peer, err := s.radar.authorized(ctx, sender.ID, target.ID)
	if err != nil {
		return internalError(method, err)
	}
	if peer.state != "" {
		return nil
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, target.WorkspaceID)
	if err != nil {
		return internalError(method, err)
	}
	if err := checkMessage(owner, ws, target.MemberID, target.Protected); err != nil {
		return deniedPeer(method, sender.ID, target.ID, ownerDenial(owner, err))
	}
	return nil
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
	if err := checkMessage(owner, &domain.Workspace{ID: run.WorkspaceID}, "", false); err != nil {
		return nil, ownerDenial(owner, err), nil
	}
	return owner, "", nil
}

// statusPeers lists the most relevant live runs self may message, bounded to
// CoordMaxStatusPeers, with the unbounded total: parent and children, the radar's active/grace peers, then the other live runs in the
// workspace that self's owner may message, most recently active first. Any
// other live run on the server can be messaged by ID without being listed. A
// run whose owner may not message lists none.
func (s *Service) statusPeers(ctx context.Context, self *domain.Run, lineage []domain.RunID) ([]protocol.CoordPeer, int, error) {
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
	ws, err := s.cfg.Store.GetWorkspace(ctx, self.WorkspaceID)
	if err != nil {
		return nil, 0, err
	}
	radarPeers, err := s.radar.authorizedSet(ctx, self.ID)
	if err != nil {
		return nil, 0, err
	}
	sent, err := s.cfg.Mail.LastSentRunMessages(ctx, self.ID)
	if err != nil {
		return nil, 0, err
	}
	overlaps := make(map[domain.RunID]authorizedPeer, len(radarPeers))
	for _, p := range radarPeers {
		overlaps[p.run] = p
	}
	seen := make(map[domain.RunID]bool)
	var addErr error
	add := func(r *store.RunSummary, state string, overlap authorizedPeer) {
		if r == nil || r.ID == self.ID || seen[r.ID] {
			return
		}
		seen[r.ID] = true
		if len(peers) == protocol.CoordMaxStatusPeers {
			return
		}
		task, _, _ := boundStatusText(r.Task)
		reason, _, reasonTrunc := boundStatusText(r.Reason)
		peer := protocol.CoordPeer{
			RunID: string(r.ID), MemberID: string(r.MemberID), Task: task, TaskBytes: r.TaskBytes,
			TaskTruncated: r.TaskBytes > protocol.CoordMaxStatusTaskBytes, State: state,
			Activity: string(r.Activity), Reason: reason, ReasonTruncated: reasonTrunc, RunStatus: string(r.Status),
		}
		if s.cfg.Waits != nil {
			peer.Wait, _, _ = boundStatusText(s.cfg.Waits.Wait(r.ID, r.Activity).Text(time.Now()))
		}
		if overlap.state != "" {
			peer.Files, peer.FileTotal, peer.FilesTruncated = boundStatusFiles(overlap.files)
		}
		if state == protocol.CoordPeerGrace {
			peer.ExpiresAt = overlap.expiry.UTC().Format(time.RFC3339)
		}
		if m := sent[r.ID]; m != nil {
			last := &protocol.CoordSentMessage{
				MessageID: m.ID, State: protocol.CoordSentStateSent, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
			}
			if held, heldErr := s.heldChild(ctx, r.ID); heldErr != nil {
				addErr = heldErr
			} else if held {
				last.State = protocol.CoordSentStateHeld
			}
			if m.DeliveredAt != nil {
				last.State, last.DeliveredAt = protocol.CoordSentStateDelivered, m.DeliveredAt.UTC().Format(time.RFC3339)
			}
			if m.RetiredAt != nil {
				last.State = protocol.CoordSentStateRetired
			}
			if m.AckedAt != nil {
				last.State, last.AckedAt = protocol.CoordSentStateAcked, m.AckedAt.UTC().Format(time.RFC3339)
			}
			peer.LastSent = last
		}
		peers = append(peers, peer)
	}
	for _, id := range lineage {
		add(byID[id], protocol.CoordPeerLineage, overlaps[id])
	}
	for _, p := range radarPeers {
		add(byID[p.run], p.state, p)
	}
	for _, r := range live {
		if checkMessage(owner, ws, r.MemberID, r.Protected) == nil {
			add(r, protocol.CoordPeerWorkspace, authorizedPeer{})
		}
	}
	if addErr != nil {
		return nil, 0, addErr
	}
	return peers, len(seen), nil
}

func checkMessage(owner *domain.Member, ws *domain.Workspace, targetOwner domain.MemberID, protected bool) error {
	return permissions.Check(permissions.Message,
		permissions.Actor{ID: owner.ID, Role: owner.Role},
		permissions.Target{Workspace: ws.ID, Owner: targetOwner, Protected: protected, SteerOthers: ws.SteerOthers})
}

func ownerDenial(owner *domain.Member, err error) string {
	return fmt.Sprintf("member %s (%s) may not message it: %s",
		owner.ID, owner.Role, strings.TrimPrefix(err.Error(), permissions.ErrDenied.Error()+": "))
}

func deniedPeer(method string, from, to domain.RunID, reason string) *protocol.Error {
	return &protocol.Error{
		Code: protocol.CodeDenied,
		Message: fmt.Sprintf("%s: session %s is not an authorized peer of session %s: %s; "+
			"run `aether-internal status` to list the sessions it can message", method, to, from, reason),
	}
}
