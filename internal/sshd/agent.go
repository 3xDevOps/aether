package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

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
	registerGuarded(protocol.MethodAgentInstall, permissions.Launch, nil, (*Server).agentInstall)
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
		Executable:      p.Definition.Executable,
		ProfileRoot:     p.Definition.ProfileRoot,
		CredentialPaths: p.Definition.CredentialPaths,
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
	describe := func(profile harness.Profile, source, executable string) (protocol.AgentInfo, error) {
		info, err := s.describeAgent(member, account, profile, source, executable)
		if err != nil {
			return protocol.AgentInfo{}, fmt.Errorf("check agent %q: %w", profile.Name, err)
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
			return nil, rpcError(err)
		}
		agents = append(agents, info)
	}
	rows, serr := s.cfg.Store.ListHarnessDefinitions(ctx, member)
	if serr != nil {
		return nil, rpcError(serr)
	}
	for _, row := range rows {
		var def harness.Definition
		if err := json.Unmarshal(row.Definition, &def); err != nil {
			return nil, rpcError(fmt.Errorf("decode harness %q definition: %w", row.Name, err))
		}
		info, err := describe(def.Profile(), "member", def.Executable)
		if err != nil {
			return nil, rpcError(err)
		}
		agents = append(agents, info)
	}
	// Shipped names and a member's rows are each sorted, but the merged
	// view must be sorted by name across both sources.
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return protocol.AgentListResult{Agents: agents}, nil
}

// describeAgent describes a launch by member on account. A member's own
// definition runs only on their own account, so it has no install command.
func (s *Server) describeAgent(member, account domain.MemberID, profile harness.Profile, source, executable string) (protocol.AgentInfo, error) {
	info := protocol.AgentInfo{
		Name:        profile.Name,
		DisplayName: profile.Label(),
		Glyph:       profile.Name,
		Source:      source,
		Enhanced:    string(profile.EnhancedSupport()),
		DefaultMode: string(domain.LaunchTUI),
	}
	if source == "member" {
		info.Glyph = "custom"
	} else {
		info.InstallScript = profile.InstallScript
		if profile.ACPInstall != nil {
			info.EnhancedInstallScript = profile.InstallCommand(true)
		}
	}
	var err error
	if info.Installed, info.EnhancedInstalled, err = s.agentInstalled(member, account, executable, profile); err != nil {
		return protocol.AgentInfo{}, err
	}
	if profile.ACPDefault && info.EnhancedInstalled {
		// The wire name of the enhanced launch mode.
		info.DefaultMode = "acp"
	}
	// A definition whose login path is the whole home has no login file to
	// look for; a launch on a shared account refuses it with that reason.
	if logins, pathErr := profile.LoginPaths(); s.cfg.Homes != nil && pathErr == nil {
		// A launch on another member's account signs in with the owner's
		// login, so that is the home to look in.
		found, loginErr := s.cfg.Homes.LoginFound(account, logins)
		if loginErr != nil {
			slog.Warn("sshd: agent.list cannot check the login", "agent", profile.Name, "account", account, "error", loginErr)
		} else {
			info.LoginFound = found
		}
	}
	return info, nil
}

// agentInstalled looks in member's home, or on another member's account in
// the owner's home whose installation the launch borrows. The ACP server
// counts only in the home the CLI comes from: that is the ~/.local the launch sees.
func (s *Server) agentInstalled(member, account domain.MemberID, executable string, profile harness.Profile) (installed, acp bool, err error) {
	if s.cfg.Homes == nil {
		return true, len(profile.ACPArgs) > 0, nil
	}
	owner, err := s.cfg.Homes.Installation(member, account, executable, profile.InstallPaths)
	if err != nil || owner == "" || len(profile.ACPArgs) == 0 {
		return owner != "", false, err
	}
	acpOwner, err := s.cfg.Homes.Installation(member, account, profile.ACPArgs[0], profile.InstallPaths)
	return true, acpOwner == owner, err
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
