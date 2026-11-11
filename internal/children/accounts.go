package children

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	choiceReady        = "ready"
	choiceExhausted    = "exhausted"
	choiceNotInstalled = "not-installed"
	choiceNotSignedIn  = "not-signed-in"
	choiceOff          = "off"
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

// choices lists what the policy lets a child run, each with its state: every
// agent on the owner's own account and on each account shared with the owner
// whose owner reaches the parent's workspaces.
func (s *Service) choices(ctx context.Context, p *policy) ([]protocol.ChildChoice, error) {
	accounts, err := s.sharedAccounts(ctx, p.owner)
	if err != nil {
		return nil, err
	}
	var out []protocol.ChildChoice
	for _, account := range accounts {
		if _, err := s.cfg.Authorize(ctx, s.cfg.Store, p.owner, string(account), p.workspaces()); errors.Is(err, permissions.ErrDenied) || errors.Is(err, permissions.ErrNoAccess) {
			continue
		} else if err != nil {
			return nil, err
		}
		list, err := s.agentChoices(ctx, p.owner, account)
		if err != nil {
			return nil, err
		}
		for i, c := range list {
			switch {
			case c.Status == choiceNotInstalled:
			case p.settings.Off.Covers(c.Harness, account):
				list[i].Status, list[i].Detail = choiceOff, offDetail(p.settings.Off, c.Harness, account, c.AccountName)
			case c.Status == choiceReady:
				if used, ok := s.usedPercent(ctx, p.owner, account, c.Harness); ok && used >= fallback.ExhaustedPercent {
					list[i].Status, list[i].Detail = choiceExhausted, fmt.Sprintf("%s's %s usage is at %.0f%%", c.AccountName, c.Harness, used)
				}
			}
		}
		out = append(out, list...)
	}
	return out, nil
}

func (s *Service) sharedAccounts(ctx context.Context, member domain.MemberID) ([]domain.MemberID, error) {
	sharers, err := s.cfg.Store.ListAccountOwners(ctx, member)
	if err != nil {
		return nil, err
	}
	accounts := []domain.MemberID{member}
	for _, m := range sharers {
		accounts = append(accounts, m.ID)
	}
	return accounts, nil
}

// offDetail names the command that turns harness back on for account: the
// agent's own entry when it is off everywhere, else the account's.
func offDetail(off domain.ChildChoicesOff, harness string, account domain.MemberID, accountName string) string {
	on := "aether subsessions on --agent " + harness
	if !off.Covers(harness, "") {
		on += " --account " + string(account)
	}
	return fmt.Sprintf("%s on %s's account is off for subsessions in the owner's settings (Subsession agents in the launch panel, or %s)", harness, accountName, on)
}

// agentChoices lists every agent on account, which the caller authorized.
func (s *Service) agentChoices(ctx context.Context, owner, account domain.MemberID) ([]protocol.ChildChoice, error) {
	if s.cfg.Agents == nil || s.cfg.Agents() == nil {
		return nil, fmt.Errorf("agent listing is unavailable")
	}
	agents, err := s.cfg.Agents().ListAgents(ctx, owner, account)
	if err != nil {
		return nil, err
	}
	name, err := s.memberName(ctx, account)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.ChildChoice, 0, len(agents))
	for _, a := range agents {
		c := protocol.ChildChoice{AccountMemberID: string(account), AccountName: name, Harness: a.Name, DisplayName: a.DisplayName, Status: choiceReady}
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
		case a.OwnAccountOnly:
			c.Status = choiceNotInstalled
			c.Detail = fmt.Sprintf("%s is your own agent definition, which runs only on your own account", a.Name)
		case a.Unavailable != "":
			c.Status = choiceNotSignedIn
			c.Detail = a.Unavailable
		case a.LoginMissing || (a.LoginFound != nil && !*a.LoginFound):
			c.Status = choiceNotSignedIn
			c.Detail = fmt.Sprintf("%s is not signed in to %s; they sign in with aether agent login %s", name, a.Name, a.Name)
		case a.LoginProblem != "":
			c.Detail = fmt.Sprintf("%s's %s login may be signed out: %s", name, a.Name, a.LoginProblem)
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
	account := p.owner
	switch {
	case req.AccountMemberID != "":
		account = domain.MemberID(req.AccountMemberID)
	case req.Harness == p.parent.Harness && p.parent.HomeMember() == p.owner:
		account = p.parent.AccountMember()
	}
	if err := s.admit(ctx, p, account); err != nil {
		return launchSpec{}, rpcErr(protocol.MethodChildStart, err)
	}
	if p.settings.Off.Covers(req.Harness, account) {
		name, err := s.memberName(ctx, account)
		if err != nil {
			return launchSpec{}, rpcErr(protocol.MethodChildStart, err)
		}
		return launchSpec{}, denied("%s; aether-internal subsession options lists the agents and accounts that are on", offDetail(p.settings.Off, req.Harness, account, name))
	}
	choices, err := s.agentChoices(ctx, p.owner, account)
	if err != nil {
		return launchSpec{}, rpcErr(protocol.MethodChildStart, err)
	}
	for _, c := range choices {
		if c.Harness == req.Harness && (c.Status == choiceNotInstalled || c.Status == choiceNotSignedIn) {
			return launchSpec{}, denied("%s", c.Detail)
		}
	}
	spec := launchSpec{harness: req.Harness, account: account, mode: mode, model: req.Model, fallback: p.parent.Fallback}
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
