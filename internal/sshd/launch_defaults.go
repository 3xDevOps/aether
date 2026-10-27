package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerGuarded(protocol.MethodLaunchDefaultsGet, permissions.View, nil, (*Server).launchDefaultsGet)
	registerGuarded(protocol.MethodLaunchDefaultsSet, permissions.Launch, nil, (*Server).launchDefaultsSet)
}

func (s *Server) launchDefaultsGet(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	out, perr := s.launchDefaultsResult(ctx, member)
	if perr != nil {
		return nil, perr
	}
	return out, nil
}

func (s *Server) launchDefaultsSet(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.LaunchDefaultsSetParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.Clear == (p.Defaults != nil) {
		return nil, invalidParams("set exactly one of defaults and clear")
	}
	var err error
	if p.Clear {
		err = s.cfg.Store.DeleteLaunchDefaults(ctx, member, p.Revision)
	} else {
		saved, werr := launchDefaultsFromWire(*p.Defaults)
		if werr != nil {
			return nil, invalidParams(werr.Error())
		}
		saved.Source = protocol.LaunchSourceMember
		if fromAssistant(ctx) {
			saved.Source = protocol.LaunchSourceAssistant
		}
		if _, rerr := fallback.ResolveLaunchAccount(ctx, s.cfg.Store, member, string(saved.Account)); rerr != nil {
			return nil, launchDefaultsRefusal("account_member_id", rerr)
		}
		for _, id := range saved.FallbackOrder {
			if _, rerr := fallback.ResolveLaunchAccount(ctx, s.cfg.Store, member, string(id)); rerr != nil {
				return nil, launchDefaultsRefusal("fallback.order", rerr)
			}
		}
		if saved.Account == member {
			saved.Account = ""
		}
		err = s.cfg.Store.PutLaunchDefaults(ctx, member, saved, p.Revision)
	}
	if errors.Is(err, store.ErrConflict) {
		current, perr := s.launchDefaultsResult(ctx, member)
		if perr != nil {
			return nil, perr
		}
		data, merr := json.Marshal(current)
		if merr != nil {
			return nil, rpcError(merr)
		}
		return nil, &protocol.Error{Code: protocol.CodeConflict, Data: data, Message: fmt.Sprintf(
			"launch.defaults.set: revision %d is not current; the saved defaults are revision %d, in this error's data: read them and save again", p.Revision, current.Revision)}
	}
	if err != nil {
		return nil, rpcError(err)
	}
	return s.launchDefaultsResult(ctx, member)
}

func launchDefaultsRefusal(field string, err error) *protocol.Error {
	perr := rpcError(err)
	perr.Message = fmt.Sprintf("%s: %s", field, perr.Message)
	return perr
}

// launchDefaultsFromWire checks d's shape as run.launch checks the same
// fields, without asking whether the caller may use them.
func launchDefaultsFromWire(d protocol.LaunchDefaults) (store.LaunchDefaults, error) {
	out := store.LaunchDefaults{
		Agent:   d.Agent,
		Agents:  map[string]store.AgentLaunchDefault{},
		Account: domain.MemberID(d.AccountMemberID),
	}
	if d.Agent != "" {
		if err := harness.ValidateName(d.Agent); err != nil {
			return out, fmt.Errorf("agent: %w", err)
		}
	}
	if len(d.Agents) > protocol.LaunchDefaultsMaxAgents {
		return out, fmt.Errorf("agents: at most %d agents", protocol.LaunchDefaultsMaxAgents)
	}
	for name, a := range d.Agents {
		if err := harness.ValidateName(name); err != nil {
			return out, fmt.Errorf("agents: %w", err)
		}
		mode := domain.LaunchMode(a.Mode)
		if mode != "" && !mode.Valid() {
			return out, fmt.Errorf("agents.%s.mode: invalid mode: %s", name, a.Mode)
		}
		if err := domain.ValidModel(a.Model); err != nil {
			return out, fmt.Errorf("agents.%s.model: %w", name, err)
		}
		out.Agents[name] = store.AgentLaunchDefault{Mode: mode, Model: a.Model}
	}
	if d.Fallback == nil {
		return out, nil
	}
	out.FallbackPolicy = d.Fallback.Policy
	switch d.Fallback.Policy {
	case protocol.FallbackAutomatic, protocol.FallbackOff:
		if len(d.Fallback.Order) > 0 {
			return out, fmt.Errorf("fallback.order is set only with the %q policy", protocol.FallbackOrdered)
		}
	case protocol.FallbackOrdered:
		if len(d.Fallback.Order) == 0 || len(d.Fallback.Order) > protocol.LaunchDefaultsMaxFallback {
			return out, fmt.Errorf("fallback.order needs 1 to %d member IDs with the %q policy", protocol.LaunchDefaultsMaxFallback, protocol.FallbackOrdered)
		}
		for _, id := range d.Fallback.Order {
			if id == "" || slices.Contains(out.FallbackOrder, domain.MemberID(id)) {
				return out, fmt.Errorf("fallback.order must list distinct, non-empty member IDs")
			}
			out.FallbackOrder = append(out.FallbackOrder, domain.MemberID(id))
		}
	default:
		return out, fmt.Errorf(`fallback.policy must be %q, %q or %q`, protocol.FallbackAutomatic, protocol.FallbackOrdered, protocol.FallbackOff)
	}
	return out, nil
}

// launchDefaultsResult reads member's saved defaults and checks each choice
// against what a launch could use now. A choice that fails stays saved and
// is reported, never rewritten.
func (s *Server) launchDefaultsResult(ctx context.Context, member domain.MemberID) (protocol.LaunchDefaultsResult, *protocol.Error) {
	saved, err := s.cfg.Store.LaunchDefaults(ctx, member)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.LaunchDefaultsResult{}, nil
	}
	if err != nil {
		return protocol.LaunchDefaultsResult{}, rpcError(err)
	}
	if saved.Account == member {
		saved.Account = ""
	}
	out := protocol.LaunchDefaultsResult{
		Revision:  saved.Revision,
		Source:    saved.Source,
		UpdatedAt: saved.UpdatedAt.Format(time.RFC3339),
		Defaults: protocol.LaunchDefaults{
			Agent:           saved.Agent,
			AccountMemberID: string(saved.Account),
		},
	}
	if saved.FallbackPolicy != "" {
		out.Defaults.Fallback = &protocol.LaunchFallbackDefault{Policy: saved.FallbackPolicy}
		for _, id := range saved.FallbackOrder {
			out.Defaults.Fallback.Order = append(out.Defaults.Fallback.Order, string(id))
		}
	}
	if len(saved.Agents) > 0 {
		out.Defaults.Agents = map[string]protocol.AgentLaunchDefault{}
		for name, a := range saved.Agents {
			out.Defaults.Agents[name] = protocol.AgentLaunchDefault{Mode: string(a.Mode), Model: a.Model}
		}
	}
	issue := func(field, value, reason string) {
		out.Invalid = append(out.Invalid, protocol.LaunchDefaultIssue{Field: field, Value: value, Reason: reason})
	}
	unknown := func(field, value, reason string) {
		out.Unknown = append(out.Unknown, protocol.LaunchDefaultIssue{Field: field, Value: value, Reason: reason})
	}
	usable := func(field string, id domain.MemberID) (bool, *protocol.Error) {
		_, rerr := fallback.ResolveLaunchAccount(ctx, s.cfg.Store, member, string(id))
		if errors.Is(rerr, permissions.ErrDenied) || errors.Is(rerr, store.ErrNotFound) {
			issue(field, string(id), rpcError(rerr).Message)
			return false, nil
		}
		if rerr != nil {
			return false, rpcError(rerr)
		}
		return true, nil
	}
	ok, perr := usable("account_member_id", saved.Account)
	if perr != nil {
		return protocol.LaunchDefaultsResult{}, perr
	}
	account := member
	if ok && saved.Account != "" {
		account = saved.Account
	}
	for _, id := range saved.FallbackOrder {
		if _, perr := usable("fallback.order", id); perr != nil {
			return protocol.LaunchDefaultsResult{}, perr
		}
	}
	if saved.Agent == "" && len(saved.Agents) == 0 {
		return out, nil
	}
	agents, err := s.ListAgents(ctx, member, account)
	if err != nil {
		return protocol.LaunchDefaultsResult{}, rpcError(err)
	}
	if saved.Agent != "" {
		if reason := agentUnusable(agents, saved.Agent); reason != "" {
			issue("agent", saved.Agent, reason)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(saved.Agents)) {
		a := saved.Agents[name]
		if reason := agentUnusable(agents, name); reason != "" {
			issue("agents."+name, name, reason)
			continue
		}
		i := slices.IndexFunc(agents, func(info protocol.AgentInfo) bool { return info.Name == name })
		if login := agents[i].LoginFound; login != nil && !*login {
			unknown("agents."+name, name, "no "+name+" login file was found on this account; it launches only if an API key in the environment signs it in")
		}
		if a.Mode == domain.LaunchACP && !agents[i].EnhancedInstalled {
			issue("agents."+name+".mode", string(a.Mode), name+"'s enhanced mode is not installed on this account; install it from the launch dialog")
		}
		if a.Model == "" {
			continue
		}
		models, known := s.cfg.Runs.CachedAgentModels(account, name)
		if !known || (models.Source != protocol.ModelSourceAgent && models.Source != protocol.ModelSourceCache) {
			unknown("agents."+name+".model", a.Model, "Aether has no current list of the models "+name+" offers on this account to check it against")
			continue
		}
		if !slices.ContainsFunc(models.Models, func(m protocol.AgentModel) bool { return m.ID == a.Model }) {
			issue("agents."+name+".model", a.Model, fmt.Sprintf("%s does not offer model %q on this account (list fetched %s)", name, a.Model, models.FetchedAt))
		}
	}
	return out, nil
}

// agentUnusable is why a launch of name on the account agents describes
// would be refused, or "" when it would not be over the agent.
func agentUnusable(agents []protocol.AgentInfo, name string) string {
	i := slices.IndexFunc(agents, func(info protocol.AgentInfo) bool { return info.Name == name })
	switch {
	case i < 0:
		return fmt.Sprintf("no agent named %q is available to you", name)
	case agents[i].Unavailable != "":
		return agents[i].Unavailable
	case agents[i].OwnAccountOnly:
		return name + " is your own agent definition, which launches only on your own account"
	case agents[i].LoginMissing:
		return "the account owner has not signed in to " + name
	case !agents[i].Installed:
		return name + " is not installed on this account; install it from the launch dialog"
	}
	return ""
}
