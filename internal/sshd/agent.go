package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodAgentRegister, (*Server).agentRegister)
	registerMethod(protocol.MethodAgentList, (*Server).agentList)
	registerGuarded(protocol.MethodAgentModels, permissions.Launch, nil, (*Server).agentModels)
	registerGuarded(protocol.MethodAgentInstall, permissions.Launch, nil, (*Server).agentInstall)
	registerGuarded(protocol.MethodAgentRemove, permissions.Launch, nil, (*Server).agentRemove)
}

// reservedAgentNames are names a member can never register: "custom" is the
// deployment escape hatch and "fake" is reserved for tests.
var reservedAgentNames = map[string]bool{"custom": true, "fake": true}

func (s *Server) agentRegister(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	// Strict decoding: a misspelled field ("credential_path") in a
	// definition must be an error, not a silently reduced registration
	// that passes validation with the wrong paths.
	var p protocol.AgentRegisterParams
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, invalidParams("invalid params: " + err.Error())
		}
	}
	name := p.Definition.Name
	if reservedAgentNames[name] {
		return nil, invalidParams(fmt.Sprintf("agent name %q is reserved", name))
	}
	if _, shipped := harness.Lookup(name); shipped {
		return nil, invalidParams(fmt.Sprintf("agent name %q conflicts with a shipped harness", name))
	}
	def := harness.Definition{
		Name:            p.Definition.Name,
		TUIArgs:         p.Definition.TUIArgs,
		HeadlessArgs:    p.Definition.HeadlessArgs,
		ACPArgs:         p.Definition.ACPArgs,
		ACPMode:         p.Definition.ACPMode,
		Executable:      p.Definition.Executable,
		ProfileRoot:     p.Definition.ProfileRoot,
		CredentialPaths: expandHome(p.Definition.CredentialPaths),
		DenyNames:       p.Definition.DenyNames,
	}
	if verr := harness.ValidateMemberDefinition(def); verr != nil {
		return nil, invalidParams(verr.Error())
	}
	blob, merr := json.Marshal(def)
	if merr != nil {
		return nil, rpcError(merr)
	}
	row := &store.HarnessDefinition{MemberID: member, Name: name, Definition: blob}
	if serr := s.cfg.Store.UpsertHarnessDefinition(ctx, row); serr != nil {
		return nil, rpcError(serr)
	}
	return protocol.AgentRegisterResult(p), nil
}

// expandHome turns a "~/" path into the absolute container path a
// definition stores; harness.HomeRelative maps it back for either home.
func expandHome(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			out[i] = "/root/" + rest
		}
	}
	return out
}

func (s *Server) agentModels(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentModelsParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.Name == "" {
		return nil, invalidParams("name is required")
	}
	account, perr := s.launchAccount(ctx, member, p.AccountMemberID)
	if perr != nil {
		return nil, perr
	}
	result, err := s.cfg.Runs.AgentModels(ctx, member, account, p.Name, p.Refresh)
	if err != nil {
		return nil, rpcError(err)
	}
	return result, nil
}

// agentList lists member's own definitions, not the account owner's, since a
// run uses its launcher's environment.
func (s *Server) agentList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentListParams](raw)
	if perr != nil {
		return nil, perr
	}
	account, perr := s.launchAccount(ctx, member, p.AccountMemberID)
	if perr != nil {
		return nil, perr
	}
	agents, err := s.ListAgents(ctx, member, account)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.AgentListResult{Agents: agents}, nil
}

// ListAgents describes every agent member can launch on account, which the
// caller has already authorized.
func (s *Server) ListAgents(ctx context.Context, member, account domain.MemberID) ([]protocol.AgentInfo, error) {
	describe := func(profile harness.Profile, source, executable string) (protocol.AgentInfo, error) {
		info, err := s.describeAgent(member, account, profile, source, executable)
		if err != nil {
			return protocol.AgentInfo{}, fmt.Errorf("check agent %q: %w", profile.Name, err)
		}
		if info.Switchable {
			if info.Switchable, err = s.cfg.Runs.AgentSwitchable(ctx, member, account, profile.Name); err != nil {
				return protocol.AgentInfo{}, fmt.Errorf("check agent %q switch: %w", profile.Name, err)
			}
		}
		if account != member {
			shared, refusal, err := s.cfg.Runs.CheckSharedLaunch(ctx, member, account, profile.Name)
			if err != nil {
				return protocol.AgentInfo{}, fmt.Errorf("check agent %q login: %w", profile.Name, err)
			}
			info.LoginMissing = shared == scheduler.SharedLoginMissing
			info.OwnAccountOnly = shared == scheduler.SharedOwnDefinitionOnly
			if shared == scheduler.SharedLoginUnavailable {
				info.Unavailable = refusal
			}
		}
		return info, nil
	}
	// "custom" (deployment escape hatch) and "fake" (deterministic test
	// harness, registered scheduler-side) are deliberately not advertised.
	var agents []protocol.AgentInfo
	for _, p := range harness.Profiles() {
		if p.Name == "custom" {
			continue
		}
		info, err := describe(p, "shipped", p.TUIArgs[0])
		if err != nil {
			return nil, err
		}
		agents = append(agents, info)
	}
	rows, err := s.cfg.Store.ListHarnessDefinitions(ctx, member)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, shipped := harness.Lookup(row.Name); shipped {
			continue
		}
		var def harness.Definition
		if err := json.Unmarshal(row.Definition, &def); err != nil {
			return nil, fmt.Errorf("decode harness %q definition: %w", row.Name, err)
		}
		info, err := describe(def.Profile(), "member", def.Executable)
		if err != nil {
			return nil, err
		}
		agents = append(agents, info)
	}
	// Shipped names and a member's rows are each sorted, but the merged
	// view must be sorted by name across both sources.
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return agents, nil
}

// describeAgent describes a launch by member on account. A member's own
// definition runs only on their own account, so it has no install command.
func (s *Server) describeAgent(member, account domain.MemberID, profile harness.Profile, source, executable string) (protocol.AgentInfo, error) {
	info := protocol.AgentInfo{
		Name:            profile.Name,
		DisplayName:     profile.Label(),
		Glyph:           profile.Name,
		Executable:      executable,
		Source:          source,
		Enhanced:        string(profile.EnhancedSupport()),
		Switchable:      profile.Capabilities.ModeSwitch,
		DefaultMode:     string(domain.LaunchTUI),
		EnhancedDefault: profile.Capabilities.EnhancedDefault,
		EffortLevels:    profile.EffortLevels,
	}
	if err := s.cfg.Runs.WakeRefusal(profile.Name, domain.LaunchTUI); err != nil {
		info.WakeStandardRefusal = err.Error()
	}
	if source == "member" {
		info.Glyph = "custom"
	} else {
		info.InstallScript = profile.InstallScript
		if profile.ACPInstall != nil {
			info.EnhancedInstallScript = profile.InstallCommand(true)
		}
		if info.LoginCommand = profile.LoginCommand; info.LoginCommand != "" {
			info.LoginKind = "interactive"
			if profile.DeviceLogin {
				info.LoginKind = "device"
			}
		}
	}
	var err error
	if info.Installed, info.EnhancedInstalled, err = s.agentInstalled(member, account, executable, profile); err != nil {
		return protocol.AgentInfo{}, err
	}
	if info.EnhancedInstalled {
		info.DefaultMode = string(domain.LaunchACP)
	}
	// A definition whose login path is the whole home has no login file to
	// look for; a launch on a shared account refuses it with that reason.
	if logins, pathErr := profile.LoginPaths(); s.cfg.Homes != nil && pathErr == nil && len(logins) > 0 {
		// A launch on another member's account signs in with the owner's
		// login, so that is the home to look in.
		found, loginErr := s.cfg.Homes.LoginFound(account, logins)
		if loginErr != nil {
			slog.Warn("sshd: agent.list cannot check the login", "agent", profile.Name, "account", account, "error", loginErr)
		}
		info.LoginFound = &found
	}
	return info, nil
}

// agentInstalled looks in member's home, or on another member's account in
// the owner's home whose installation the launch borrows.
func (s *Server) agentInstalled(member, account domain.MemberID, executable string, profile harness.Profile) (installed, acp bool, err error) {
	if s.cfg.Homes == nil {
		return true, len(profile.ACPArgs) > 0, nil
	}
	var adapter string
	if len(profile.ACPArgs) > 0 {
		adapter = profile.ACPArgs[0]
	}
	return s.cfg.Homes.AgentInstalled(member, account, executable, adapter, profile.InstallPaths)
}

// agentInstall runs a shipped agent's install command in the caller's own
// environment terminal. A failed command is a result, not an error: the
// member needs its output to act on it.
func (s *Server) agentInstall(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentInstallParams](raw)
	if perr != nil {
		return nil, perr
	}
	profile, ok := harness.Lookup(p.Name)
	if !ok || profile.InstallScript == "" {
		return nil, invalidParams(fmt.Sprintf("agent %q has no install command; install it in the environment terminal", p.Name))
	}
	if p.Enhanced && profile.EnhancedSupport() == harness.EnhancedNone {
		return nil, invalidParams(fmt.Sprintf("agent %q has no enhanced mode", p.Name))
	}
	tail, code, err := s.cfg.Runs.InstallAgent(ctx, member, profile.InstallCommand(p.Enhanced))
	if err != nil {
		return nil, rpcError(err)
	}
	result := protocol.AgentInstallResult{LogTail: tail}
	if code != 0 {
		result.Error = fmt.Sprintf("the install command exited %d", code)
	}
	info, err := s.describeAgent(member, member, profile, "shipped", profile.TUIArgs[0])
	if err != nil {
		return nil, rpcError(fmt.Errorf("check agent %q: %w", p.Name, err))
	}
	result.Installed, result.EnhancedInstalled = info.Installed, info.EnhancedInstalled
	return result, nil
}

// agentRemove resolves name the way a launch does: a shipped name wins over
// a member's definition of it. It always acts on the caller's own home.
func (s *Server) agentRemove(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentRemoveParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.Name == "" {
		return nil, invalidParams("name is required")
	}
	if s.cfg.Homes == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "persistent homes are not enabled"}
	}
	if profile, shipped := harness.Lookup(p.Name); shipped && p.Name != "custom" {
		executables := []string{profile.TUIArgs[0]}
		if len(profile.ACPArgs) > 0 && profile.ACPArgs[0] != profile.TUIArgs[0] {
			executables = append(executables, profile.ACPArgs[0])
		}
		removed, err := s.cfg.Runs.RemoveAgent(ctx, member, executables)
		if err != nil {
			return nil, rpcError(err)
		}
		if len(removed) == 0 {
			return nil, invalidParams(fmt.Sprintf("agent %q is not installed in your environment: ~/.local/bin/%s does not exist", p.Name, executables[0]))
		}
		return protocol.AgentRemoveResult{Removed: removed}, nil
	}
	if err := s.cfg.Store.DeleteHarnessDefinition(ctx, member, p.Name); errors.Is(err, store.ErrNotFound) {
		return nil, invalidParams(fmt.Sprintf("you have no agent %q; aether agent list shows yours", p.Name))
	} else if err != nil {
		return nil, rpcError(err)
	}
	return protocol.AgentRemoveResult{}, nil
}
