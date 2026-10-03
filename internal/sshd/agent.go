package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerMethod(protocol.MethodAgentRegister, (*Server).agentRegister)
	registerMethod(protocol.MethodAgentList, (*Server).agentList)
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

// agentList describes what a launch by member on the requested account would
// run: member's own definitions, since a run uses its launcher's environment,
// the executables installed for it, and on another member's account whether
// that account has the login the launch needs.
func (s *Server) agentList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentListParams](raw)
	if perr != nil {
		return nil, perr
	}
	account, perr := s.launchAccount(ctx, member, p.AccountMemberID)
	if perr != nil {
		return nil, perr
	}
	describe := func(name, source, executable, installScript string) (protocol.AgentInfo, error) {
		installed, err := s.agentInstalled(member, account, executable)
		if err != nil {
			return protocol.AgentInfo{}, fmt.Errorf("check agent %q: %w", name, err)
		}
		info := protocol.AgentInfo{Name: name, Source: source, Installed: installed, InstallScript: installScript}
		if account != member {
			shared, refusal, err := s.cfg.Runs.CheckSharedLaunch(ctx, member, account, name)
			if err != nil {
				return protocol.AgentInfo{}, fmt.Errorf("check agent %q login: %w", name, err)
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
		info, err := describe(p.Name, "shipped", p.TUIArgs[0], p.InstallScript)
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
		info, err := describe(row.Name, "member", def.Executable, "")
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

// agentInstalled reports whether a launch by member on account finds
// executable: in member's home, or on another member's account in that
// owner's home, whose installation the launch then borrows.
func (s *Server) agentInstalled(member, account domain.MemberID, executable string) (bool, error) {
	if s.cfg.Homes == nil {
		return true, nil
	}
	installation, err := s.cfg.Homes.Installation(member, account, executable)
	return installation != "", err
}
