package children

import (
	"context"
	"fmt"
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
	if s.cfg.Agents == nil || s.cfg.Agents() == nil {
		return nil, fmt.Errorf("agent listing is unavailable")
	}
	return s.agentChoices(ctx, p, p.owner)
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
	if req.AccountMemberID != "" && domain.MemberID(req.AccountMemberID) != p.owner {
		return launchSpec{}, denied("subsessions use the owner's own %s account", req.Harness)
	}
	spec := launchSpec{harness: req.Harness, account: p.owner, mode: mode, model: req.Model, fallback: p.parent.Fallback}
	choices, err := s.choices(ctx, p)
	if err != nil {
		return launchSpec{}, rpcErr(protocol.MethodChildStart, err)
	}
	for _, c := range choices {
		if c.Harness == req.Harness && (c.Status == choiceNotInstalled || c.Status == choiceNotSignedIn) {
			return launchSpec{}, denied("%s", c.Detail)
		}
	}
	defaultMode, err := s.cfg.Runs.DefaultMode(ctx, p.owner, spec.account, spec.harness)
	if err != nil {
		return launchSpec{}, invalid("agent %q: %s; aether-internal subsession options lists the agents", req.Harness, err.Error())
	}
	if spec.mode == "" {
		spec.mode = defaultMode
	}
	return spec, nil
}

func trimPurpose(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}
