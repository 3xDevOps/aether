package children

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	scopeSwarm  = "swarm"
	scopeMember = "member"
)

// policy is what a parent's next child is bound by.
type policy struct {
	parent   *domain.Run
	scope    string
	owner    domain.MemberID
	mission  *domain.Mission
	treeKey  string
	root     domain.RunID
	depth    int
	limits   domain.ChildLimits
	settings store.ChildSettings
	// refusal is the exact reason a child cannot start; empty when one can.
	refusal string
	// off marks a refusal because child runs are turned off, which the
	// dashboard suggests turning on.
	off bool
}

// resolve reads the parent's lineage and the owner's authority. It never
// trusts the agent: the parent is the socket's run.
func (s *Service) resolve(ctx context.Context, parentID domain.RunID) (*policy, error) {
	parent, err := s.cfg.Store.GetRun(ctx, parentID)
	if err != nil {
		return nil, err
	}
	p := &policy{parent: parent, owner: parent.MemberID, root: parent.ID, treeKey: string(parent.ID), depth: 1}
	missionID := parent.MissionID
	reservation, err := s.cfg.Store.GetChild(ctx, parent.ID)
	switch {
	case err == nil:
		p.root, p.treeKey, p.depth = reservation.RootRunID, reservation.TreeKey, reservation.Depth+1
		missionID = reservation.MissionID
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	case parent.MissionID != "":
		p.treeKey = "mission:" + string(parent.MissionID)
		if parent.MissionRole == "worker" {
			p.depth = 2
			if parent.IntegratorRunID != "" {
				p.root = parent.IntegratorRunID
			}
		}
	}
	if missionID != "" {
		if p.mission, err = s.cfg.Store.GetMission(ctx, missionID); err != nil {
			return nil, fmt.Errorf("read swarm %s: %w", missionID, err)
		}
		p.scope, p.owner = scopeSwarm, p.mission.AccountableHumanID
	} else {
		p.scope = scopeMember
	}
	if p.limits, err = s.cfg.Store.WorkspaceChildLimits(ctx, parent.WorkspaceID); err != nil {
		return nil, err
	}
	if p.settings, err = s.cfg.Store.ChildSettings(ctx, p.owner); err != nil {
		return nil, err
	}
	switch {
	case parent.Status.Terminal():
		p.refusal = fmt.Sprintf("run %s has finished", parent.ID)
	case p.mission != nil && parent.MemberID != p.owner:
		p.refusal = fmt.Sprintf("run %s belongs to %s, not to %s who owns swarm %s, so it cannot start children on the swarm's accounts", parent.ID, parent.MemberID, p.owner, p.mission.ID)
	case parent.Mode == domain.LaunchHeadless:
		p.refusal = "a Background run exits when its turn ends, before a child's result can reach it; start the parent in Standard or Enhanced mode"
	case p.mission != nil && p.mission.Phase == domain.MissionPhaseCancelled:
		p.refusal = fmt.Sprintf("swarm %s is cancelled", p.mission.ID)
	case p.depth > p.limits.MaxDepth:
		p.refusal = fmt.Sprintf("a child of this run would be %d levels deep; this workspace allows %d (an admin changes it with aether workspace settings --child-max-depth)", p.depth, p.limits.MaxDepth)
	case p.scope == scopeMember:
		on, err := s.memberAllows(ctx, parent, p.settings)
		if err != nil {
			return nil, err
		}
		if !on {
			p.off = true
			p.refusal = "child runs are off for this run's owner; they can turn them on in Settings → Child runs (aether children on) or allow them for this run only from the run's More menu (Allow child runs)"
		}
	}
	return p, nil
}

// memberAllows applies the nearest run-level setting among the run and its
// ancestors that the run's current owner made, else the owner's own.
func (s *Service) memberAllows(ctx context.Context, run *domain.Run, settings store.ChildSettings) (bool, error) {
	owner := run.MemberID
	for r := run; r != nil; {
		switch {
		case r.Children == domain.ChildrenOff:
			return false, nil
		case r.Children == domain.ChildrenOn && r.MemberID == owner && r.ChildrenSetBy == owner:
			return true, nil
		}
		if r.ParentRunID == "" || r.MissionRole != "" {
			break
		}
		next, err := s.cfg.Store.GetRun(ctx, r.ParentRunID)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return false, err
		}
		r = next
	}
	return settings.Enabled, nil
}

func limitsWire(l domain.ChildLimits) protocol.ChildLimits {
	return protocol.ChildLimits{MaxLive: l.MaxLive, MaxDepth: l.MaxDepth, MaxTotal: l.MaxTotal}
}
