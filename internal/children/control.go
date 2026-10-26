package children

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	firstSnooze  = 7 * 24 * time.Hour
	secondSnooze = 30 * 24 * time.Hour
)

func settingsWire(st store.ChildSettings, now time.Time) protocol.ChildSettings {
	out := protocol.ChildSettings{
		Enabled: st.Enabled, Fallback: st.Fallback, SharedWhenMissing: st.SharedWhenMissing,
		Configured: st.Configured, Nudge: protocol.ChildNudgeShow,
	}
	switch {
	case st.NoNudge || st.Enabled:
		out.Nudge = protocol.ChildNudgeNever
	case now.Before(st.SnoozedUntil):
		out.Nudge = protocol.ChildNudgeSnoozed
	}
	return out
}

// Settings is member's own child-run setting.
func (s *Service) Settings(ctx context.Context, member domain.MemberID) (protocol.ChildSettings, error) {
	st, err := s.cfg.Store.ChildSettings(ctx, member)
	if err != nil {
		return protocol.ChildSettings{}, err
	}
	return settingsWire(st, time.Now()), nil
}

// SetSettings changes member's own setting. Any saved change marks it
// configured; a nudge answer only snoozes or stops the nudge.
func (s *Service) SetSettings(ctx context.Context, member domain.MemberID, p protocol.ChildSettingsParams) (protocol.ChildSettings, error) {
	st, err := s.cfg.Store.ChildSettings(ctx, member)
	if err != nil {
		return protocol.ChildSettings{}, err
	}
	switch p.Nudge {
	case "":
	case protocol.ChildNudgeLater:
		st.Snoozes++
		switch st.Snoozes {
		case 1:
			st.SnoozedUntil = time.Now().Add(firstSnooze).UTC()
		case 2:
			st.SnoozedUntil = time.Now().Add(secondSnooze).UTC()
		default:
			st.NoNudge = true
		}
	case protocol.ChildNudgeNever:
		st.NoNudge = true
	default:
		return protocol.ChildSettings{}, invalid("nudge must be %q or %q", protocol.ChildNudgeLater, protocol.ChildNudgeNever)
	}
	if p.Enabled != nil || p.Fallback != nil || p.SharedWhenMissing != nil {
		st.Configured = true
	}
	if p.Enabled != nil {
		st.Enabled = *p.Enabled
	}
	if p.Fallback != nil {
		st.Fallback = *p.Fallback
	}
	if p.SharedWhenMissing != nil {
		st.SharedWhenMissing = *p.SharedWhenMissing
	}
	if err := s.cfg.Store.PutChildSettings(ctx, member, st); err != nil {
		return protocol.ChildSettings{}, err
	}
	s.Kick()
	return settingsWire(st, time.Now()), nil
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
			return protocol.RunChildrenAllowResult{}, fmt.Errorf("%w: only the run's owner can allow its child runs, since they spend the owner's accounts", permissions.ErrDenied)
		}
	case domain.ChildrenOff, "":
		if actor.ID != r.MemberID && actor.Role != domain.RoleAdmin {
			return protocol.RunChildrenAllowResult{}, fmt.Errorf("%w: only the run's owner or an admin can change its child runs", permissions.ErrDenied)
		}
	default:
		return protocol.RunChildrenAllowResult{}, invalid("children must be %q, %q or empty", domain.ChildrenOn, domain.ChildrenOff)
	}
	if err := s.cfg.Store.SetRunChildren(ctx, run, value, actor.ID); err != nil {
		return protocol.RunChildrenAllowResult{}, err
	}
	s.publish(ctx, run, actor.ID)
	s.Kick()
	return protocol.RunChildrenAllowResult{Children: value}, nil
}

// Detach keeps child running on its own.
func (s *Service) Detach(ctx context.Context, child domain.RunID) (protocol.Child, error) {
	c, err := s.cfg.Store.GetChild(ctx, child)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Child{}, fmt.Errorf("%w: run %s is not a child run", store.ErrNotFound, child)
	}
	if err != nil {
		return protocol.Child{}, err
	}
	return s.detach(ctx, c)
}

// Children lists run's descendants and the usage of run and all of them,
// each run counted once on the accounts that paid.
func (s *Service) Children(ctx context.Context, run domain.RunID) (protocol.RunChildrenResult, error) {
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return protocol.RunChildrenResult{}, err
	}
	kids, err := s.cfg.Store.ListChildren(ctx, run, true)
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
		child, err := s.wire(ctx, c)
		if err != nil {
			return protocol.RunChildrenResult{}, err
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

// SetLimits changes the given limits within 1 and domain.MaxChildLimits.
func (s *Service) SetLimits(ctx context.Context, p protocol.WorkspaceChildLimitsParams) (protocol.ChildLimits, error) {
	ws := domain.WorkspaceID(p.WorkspaceID)
	l, err := s.cfg.Store.WorkspaceChildLimits(ctx, ws)
	if err != nil {
		return protocol.ChildLimits{}, err
	}
	for _, f := range []struct {
		name string
		in   *int
		out  *int
		max  int
	}{
		{"max_live", p.MaxLive, &l.MaxLive, domain.MaxChildLimits.MaxLive},
		{"max_depth", p.MaxDepth, &l.MaxDepth, domain.MaxChildLimits.MaxDepth},
		{"max_total", p.MaxTotal, &l.MaxTotal, domain.MaxChildLimits.MaxTotal},
	} {
		if f.in == nil {
			continue
		}
		if *f.in < 1 || *f.in > f.max {
			return protocol.ChildLimits{}, invalid("%s must be between 1 and %d", f.name, f.max)
		}
		*f.out = *f.in
	}
	if err := s.cfg.Store.SetWorkspaceChildLimits(ctx, ws, l); err != nil {
		return protocol.ChildLimits{}, err
	}
	s.Kick()
	return limitsWire(l), nil
}
