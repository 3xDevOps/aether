package children

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	choiceReady        = "ready"
	choiceExhausted    = "exhausted"
	choiceNotInstalled = "not-installed"
	choiceNotSignedIn  = "not-signed-in"
)

var modeNames = map[string]domain.LaunchMode{
	"standard": domain.LaunchTUI, "enhanced": domain.LaunchACP, "background": domain.LaunchHeadless,
	"tui": domain.LaunchTUI, "acp": domain.LaunchACP, "headless": domain.LaunchHeadless,
}

func modeName(m domain.LaunchMode) string {
	switch m {
	case domain.LaunchACP:
		return "enhanced"
	case domain.LaunchHeadless:
		return "background"
	}
	return "standard"
}

// choices lists what the policy lets a child run, each with its state.
func (s *Service) choices(ctx context.Context, p *policy) ([]protocol.ChildChoice, error) {
	if p.mission != nil {
		out := make([]protocol.ChildChoice, 0, len(p.mission.ExecutionChoices))
		for _, c := range p.mission.ExecutionChoices {
			name, err := s.memberName(ctx, c.AccountMemberID)
			if err != nil {
				return nil, err
			}
			out = append(out, protocol.ChildChoice{
				AccountMemberID: string(c.AccountMemberID), AccountName: name, Harness: c.Harness,
				Modes: []string{modeName(c.Mode)}, DefaultMode: modeName(c.Mode), Model: c.Model, Status: choiceReady,
			})
		}
		return out, nil
	}
	if s.cfg.Agents == nil || s.cfg.Agents() == nil {
		return nil, fmt.Errorf("agent listing is unavailable")
	}
	own, err := s.agentChoices(ctx, p, p.owner)
	if err != nil {
		return nil, err
	}
	if !p.settings.SharedWhenMissing {
		return own, nil
	}
	missing := map[string]bool{}
	for _, c := range own {
		if c.Status == choiceNotInstalled || c.Status == choiceNotSignedIn {
			missing[c.Harness] = true
		}
	}
	if len(missing) == 0 {
		return own, nil
	}
	owners, err := s.cfg.Store.ListAccountOwners(ctx, p.owner)
	if err != nil {
		return nil, err
	}
	out := own
	for _, owner := range owners {
		shared, err := s.agentChoices(ctx, p, owner.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range shared {
			if missing[c.Harness] && (c.Status == choiceReady || c.Status == choiceExhausted) {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func (s *Service) agentChoices(ctx context.Context, p *policy, account domain.MemberID) ([]protocol.ChildChoice, error) {
	agents, err := s.cfg.Agents().ListAgents(ctx, p.owner, account)
	if err != nil {
		return nil, err
	}
	name, err := s.memberName(ctx, account)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.ChildChoice, 0, len(agents))
	for _, a := range agents {
		if account != p.owner && (a.OwnAccountOnly || a.Unavailable != "") {
			continue
		}
		c := protocol.ChildChoice{AccountMemberID: string(account), AccountName: name, Harness: a.Name, Status: choiceReady}
		c.Modes = []string{"standard", "background"}
		c.DefaultMode = "standard"
		if a.EnhancedInstalled {
			c.Modes = append([]string{"enhanced"}, c.Modes...)
			c.DefaultMode = "enhanced"
		}
		switch {
		case !a.Installed:
			c.Status = choiceNotInstalled
			c.Detail = fmt.Sprintf("%s is not installed in %s's environment; they run aether agent add %s", a.Name, name, a.Name)
		case a.LoginMissing || (a.LoginFound != nil && !*a.LoginFound):
			c.Status = choiceNotSignedIn
			c.Detail = fmt.Sprintf("%s is not signed in to %s; they sign in with aether agent login %s", name, a.Name, a.Name)
		default:
			if used, ok := s.usedPercent(ctx, p.owner, account, a.Name); ok && used >= fallback.ExhaustedPercent {
				c.Status = choiceExhausted
				c.Detail = fmt.Sprintf("%s's %s usage is at %.0f%%", name, a.Name, used)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// usedPercent is the account's quota use for harness, when readable.
func (s *Service) usedPercent(ctx context.Context, owner, account domain.MemberID, harness string) (float64, bool) {
	measured, err := fallback.Measure(ctx, s.cfg.Store, s.cfg.Usage, owner, string(account), harness, domain.AccountFallback{})
	if err != nil {
		return 0, false
	}
	used, ok := measured[account]
	return used, ok
}

func (s *Service) memberName(ctx context.Context, id domain.MemberID) (string, error) {
	m, err := s.cfg.Store.GetMember(ctx, id)
	if err != nil {
		return "", fmt.Errorf("read member %s: %w", id, err)
	}
	return m.DisplayName, nil
}

// launchSpec is the agent, account, mode, model and fallback a child runs.
type launchSpec struct {
	harness  string
	account  domain.MemberID
	mode     domain.LaunchMode
	model    string
	fallback domain.AccountFallback
}

// choose resolves a start request against the policy, refusing anything
// outside it.
func (s *Service) choose(ctx context.Context, p *policy, req protocol.ChildStartParams) (launchSpec, *protocol.Error) {
	var mode domain.LaunchMode
	if req.Mode != "" {
		var ok bool
		if mode, ok = modeNames[req.Mode]; !ok {
			return launchSpec{}, invalid("mode must be standard, enhanced or background")
		}
	}
	if err := domain.ValidModel(req.Model); err != nil {
		return launchSpec{}, invalid("%s", err.Error())
	}
	if p.mission != nil {
		var match []domain.MissionExecutionChoice
		for _, c := range p.mission.ExecutionChoices {
			if c.Harness == req.Harness && (req.Model == "" || c.Model == req.Model) &&
				(mode == "" || c.Mode == mode) && (req.AccountMemberID == "" || string(c.AccountMemberID) == req.AccountMemberID) {
				match = append(match, c)
			}
		}
		switch len(match) {
		case 0:
			return launchSpec{}, denied("swarm %s allows no %s choice matching that request; aether-internal child options lists its choices", p.mission.ID, req.Harness)
		case 1:
			c := match[0]
			return launchSpec{harness: c.Harness, account: c.AccountMemberID, mode: c.Mode, model: c.Model, fallback: p.mission.Fallback}, nil
		}
		return launchSpec{}, invalid("swarm %s has %d %s choices; name --model, --mode or --account to pick one", p.mission.ID, len(match), req.Harness)
	}
	spec := launchSpec{harness: req.Harness, account: p.owner, mode: mode, model: req.Model, fallback: p.parent.Fallback}
	if !spec.fallback.Disabled && len(spec.fallback.Order) == 0 {
		spec.fallback.Disabled = !p.settings.Fallback
	}
	choices, err := s.choices(ctx, p)
	if err != nil {
		return launchSpec{}, rpcErr(protocol.MethodChildStart, err)
	}
	var own *protocol.ChildChoice
	var shared []protocol.ChildChoice
	for i, c := range choices {
		if c.Harness != req.Harness {
			continue
		}
		if domain.MemberID(c.AccountMemberID) == p.owner {
			own = &choices[i]
		} else {
			shared = append(shared, c)
		}
	}
	if own == nil {
		return launchSpec{}, invalid("unknown agent %q; aether-internal child options lists the agents", req.Harness)
	}
	switch {
	case req.AccountMemberID == "" || domain.MemberID(req.AccountMemberID) == p.owner:
		if (own.Status == choiceNotInstalled || own.Status == choiceNotSignedIn) && len(shared) > 0 {
			spec.account = domain.MemberID(shared[0].AccountMemberID)
		} else if own.Status == choiceNotInstalled || own.Status == choiceNotSignedIn {
			return launchSpec{}, denied("%s%s", own.Detail, sharedHint(p))
		}
	default:
		i := slices.IndexFunc(shared, func(c protocol.ChildChoice) bool { return c.AccountMemberID == req.AccountMemberID })
		if i < 0 {
			return launchSpec{}, denied("child runs use the owner's own %s account; another member's account needs \"Use subscriptions shared with me when I don't have my own\" in Settings → Child runs, a share from that member, and no %s login of the owner's", req.Harness, req.Harness)
		}
		spec.account = domain.MemberID(req.AccountMemberID)
	}
	if spec.mode == "" {
		if spec.mode, err = s.cfg.Runs.DefaultMode(ctx, p.owner, spec.account, spec.harness); err != nil {
			return launchSpec{}, rpcErr(protocol.MethodChildStart, err)
		}
	}
	return spec, nil
}

func sharedHint(p *policy) string {
	if p.settings.SharedWhenMissing {
		return "; no account shared with them has it signed in either"
	}
	return "; to use a teammate's shared subscription instead, the owner turns on \"Use subscriptions shared with me when I don't have my own\" in Settings → Child runs"
}

func trimPurpose(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}
