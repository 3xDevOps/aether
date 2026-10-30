package children

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	agentharness "github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	scopeSwarm  = "swarm"
	scopeMember = "member"
)

// policy is what a parent's next child is bound by.
type policy struct {
	parent  *domain.Run
	scope   string
	owner   domain.MemberID
	mission *domain.Mission
	treeKey string
	root    domain.RunID
	// parentOf is the run that started parent when parent is a child run.
	parentOf domain.RunID
	limits   domain.ChildLimits
	settings store.ChildSettings
	// refusal is the exact reason a child cannot start; empty when one can.
	refusal string
}

// resolve reads the parent's lineage and the owner's authority. It never
// trusts the agent: the parent is the socket's run.
func (s *Service) resolve(ctx context.Context, parentID domain.RunID) (*policy, error) {
	parent, err := s.cfg.Store.GetRun(ctx, parentID)
	if err != nil {
		return nil, err
	}
	p := &policy{parent: parent, owner: parent.MemberID, root: parent.ID, treeKey: string(parent.ID)}
	reservation, err := s.cfg.Store.GetChild(ctx, parent.ID)
	switch {
	case err == nil:
		p.parentOf = reservation.ParentRunID
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	case parent.MissionID != "":
		p.treeKey = "mission:" + string(parent.MissionID)
		if parent.MissionRole == "worker" && parent.IntegratorRunID != "" {
			p.root = parent.IntegratorRunID
		}
	}
	if parent.MissionID != "" {
		if p.mission, err = s.cfg.Store.GetMission(ctx, parent.MissionID); err != nil {
			return nil, fmt.Errorf("read swarm %s: %w", parent.MissionID, err)
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
	case p.parentOf != "":
		p.refusal = fmt.Sprintf("run %s is itself a child run of %s, and child runs go one level deep; do the work yourself or message %s", parent.ID, p.parentOf, p.parentOf)
	case agentharness.SwarmRefusal(parent.Harness, parent.Mode) != nil:
		p.refusal = agentharness.SwarmRefusal(parent.Harness, parent.Mode).Error()
	case p.scope == scopeMember:
		if !memberAllows(parent, p.settings) {
			p.refusal = "child runs are off for this run's owner; they can turn them on in Settings → Child runs (aether children on) or allow them for this run only from the run's More menu (Allow child runs)"
		}
	}
	return p, nil
}

// memberAllows applies the run's own setting, which counts only while the
// run's current owner made it, else the owner's.
func memberAllows(run *domain.Run, settings store.ChildSettings) bool {
	switch {
	case run.Children == domain.ChildrenOff:
		return false
	case run.Children == domain.ChildrenOn && run.ChildrenSetBy == run.MemberID:
		return true
	}
	return settings.Enabled
}

func limitsWire(l domain.ChildLimits) protocol.ChildLimits {
	return protocol.ChildLimits{MaxLive: l.MaxLive}
}
