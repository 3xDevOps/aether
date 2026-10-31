package children

import (
	"context"
	"fmt"
	"sort"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Settings is member's own child-run setting.
func (s *Service) Settings(ctx context.Context, member domain.MemberID) (protocol.ChildSettings, error) {
	st, err := s.cfg.Store.ChildSettings(ctx, member)
	if err != nil {
		return protocol.ChildSettings{}, err
	}
	return protocol.ChildSettings{Enabled: st.Enabled}, nil
}

// SetSettings changes member's own setting.
func (s *Service) SetSettings(ctx context.Context, member domain.MemberID, p protocol.ChildSettingsParams) (protocol.ChildSettings, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	st, err := s.cfg.Store.ChildSettings(ctx, member)
	if err != nil {
		return protocol.ChildSettings{}, err
	}
	if p.Enabled != nil {
		st.Enabled = *p.Enabled
	}
	if err := s.cfg.Store.PutChildSettings(ctx, member, st); err != nil {
		return protocol.ChildSettings{}, err
	}
	s.Kick()
	return protocol.ChildSettings{Enabled: st.Enabled}, nil
}

// Allow sets run's own child-run setting. Only its owner turns it on: it
// spends their accounts. The owner or an admin turns it off.
func (s *Service) Allow(ctx context.Context, actor *domain.Member, run domain.RunID, value string) (protocol.RunChildrenAllowResult, error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return protocol.RunChildrenAllowResult{}, err
	}
	switch value {
	case domain.ChildrenOn:
		if actor.ID != r.MemberID {
			return protocol.RunChildrenAllowResult{}, fmt.Errorf("%w: only the session's owner can allow its subsessions, since they spend the owner's accounts", permissions.ErrDenied)
		}
	case domain.ChildrenOff, "":
		if actor.ID != r.MemberID && actor.Role != domain.RoleAdmin {
			return protocol.RunChildrenAllowResult{}, fmt.Errorf("%w: only the session's owner or an admin can change its subsessions", permissions.ErrDenied)
		}
	default:
		return protocol.RunChildrenAllowResult{}, invalid("subsessions must be %q, %q or empty", domain.ChildrenOn, domain.ChildrenOff)
	}
	if err := s.cfg.Store.SetRunChildren(ctx, run, value, actor.ID); err != nil {
		return protocol.RunChildrenAllowResult{}, err
	}
	s.publish(ctx, run, actor.ID)
	s.Kick()
	return protocol.RunChildrenAllowResult{Children: value}, nil
}

// Children lists run's children and the usage of run and all of them,
// each run counted once on the accounts that paid.
func (s *Service) Children(ctx context.Context, run domain.RunID) (protocol.RunChildrenResult, error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return protocol.RunChildrenResult{}, err
	}
	kids, err := s.cfg.Store.ListChildren(ctx, run)
	if err != nil {
		return protocol.RunChildrenResult{}, err
	}
	limits, err := s.cfg.Store.WorkspaceChildLimits(ctx, r.WorkspaceID)
	if err != nil {
		return protocol.RunChildrenResult{}, err
	}
	root := r.RootRunID
	if root == "" {
		root = r.ID
	}
	out := protocol.RunChildrenResult{RootRunID: string(root), Children: make([]protocol.Child, 0, len(kids)), Limits: limitsWire(limits)}
	ids := []domain.RunID{run}
	for _, c := range kids {
		child, wireErr := s.wire(ctx, c)
		if wireErr != nil {
			return protocol.RunChildrenResult{}, wireErr
		}
		out.Children = append(out.Children, child)
		if c.State == domain.ChildStarted {
			ids = append(ids, c.RunID)
		}
	}
	if out.Usage, err = s.usage(ctx, ids); err != nil {
		return protocol.RunChildrenResult{}, err
	}
	return out, nil
}

func (s *Service) usage(ctx context.Context, runs []domain.RunID) (protocol.ChildUsage, error) {
	u := protocol.ChildUsage{ByAccount: []protocol.AccountUsage{}}
	byAccount := map[domain.MemberID]*protocol.AccountUsage{}
	for _, id := range runs {
		rows, err := s.cfg.Store.ListRunCosts(ctx, id)
		if err != nil {
			return u, err
		}
		u.Runs++
		metered := false
		paid := map[domain.MemberID]bool{}
		for _, row := range rows {
			if !row.Metered {
				continue
			}
			metered = true
			u.InputTokens += row.InputTokens
			u.OutputTokens += row.OutputTokens
			u.CostUSD += row.CostUSD
			a := byAccount[row.MemberID]
			if a == nil {
				a = &protocol.AccountUsage{AccountMemberID: string(row.MemberID)}
				byAccount[row.MemberID] = a
			}
			a.CostUSD += row.CostUSD
			if !paid[row.MemberID] {
				paid[row.MemberID] = true
				a.Runs++
			}
		}
		if metered {
			u.MeteredRuns++
		} else {
			u.UnmeteredRuns++
		}
	}
	for _, a := range byAccount {
		u.ByAccount = append(u.ByAccount, *a)
	}
	sort.Slice(u.ByAccount, func(i, j int) bool { return u.ByAccount[i].AccountMemberID < u.ByAccount[j].AccountMemberID })
	return u, nil
}

// Limits are the workspace's child-run limits.
func (s *Service) Limits(ctx context.Context, ws domain.WorkspaceID) (protocol.ChildLimits, error) {
	l, err := s.cfg.Store.WorkspaceChildLimits(ctx, ws)
	return limitsWire(l), err
}

// SetLimits changes the given limit within 1 and domain.MaxChildLimits.
func (s *Service) SetLimits(ctx context.Context, p protocol.WorkspaceChildLimitsParams) (protocol.ChildLimits, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	ws := domain.WorkspaceID(p.WorkspaceID)
	l, err := s.cfg.Store.WorkspaceChildLimits(ctx, ws)
	if err != nil {
		return protocol.ChildLimits{}, err
	}
	if p.MaxLive != nil {
		if *p.MaxLive < 1 || *p.MaxLive > domain.MaxChildLimits.MaxLive {
			return protocol.ChildLimits{}, invalid("max_live must be between 1 and %d", domain.MaxChildLimits.MaxLive)
		}
		l.MaxLive = *p.MaxLive
	}
	if err := s.cfg.Store.SetWorkspaceChildLimits(ctx, ws, l); err != nil {
		return protocol.ChildLimits{}, err
	}
	s.Kick()
	return limitsWire(l), nil
}
