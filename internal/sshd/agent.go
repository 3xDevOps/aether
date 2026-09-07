package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
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

func (s *Server) agentList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentListParams](raw)
	if perr != nil {
		return nil, perr
	}
	account, perr := s.launchAccount(ctx, member, p.AccountMemberID)
	if perr != nil {
		return nil, perr
	}
	// "custom" (deployment escape hatch) and "fake" (deterministic test
	// harness, registered scheduler-side) are deliberately not advertised.
	var agents []protocol.AgentInfo
	for _, p := range harness.Profiles() {
		if p.Name == "custom" {
			continue
		}
		agents = append(agents, protocol.AgentInfo{
			Name:          p.Name,
			Source:        "shipped",
			Installed:     s.agentInstalled(account, p.TUIArgs[0]),
			InstallScript: p.InstallScript,
		})
	}
	rows, serr := s.cfg.Store.ListHarnessDefinitions(ctx, account)
	if serr != nil {
		return nil, rpcError(serr)
	}
	for _, row := range rows {
		var def harness.Definition
		if err := json.Unmarshal(row.Definition, &def); err != nil {
			return nil, rpcError(fmt.Errorf("decode harness %q definition: %w", row.Name, err))
		}
		agents = append(agents, protocol.AgentInfo{
			Name:      row.Name,
			Source:    "member",
			Installed: s.agentInstalled(account, def.Executable),
		})
	}
	// Shipped names and a member's rows are each sorted, but the merged
	// view must be sorted by name across both sources.
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return protocol.AgentListResult{Agents: agents}, nil
}

// agentInstalled checks the installation location documented by the shipped
// installers. The member home is mounted at the environment container's
// HOME, so this check is shared by runs and the environment terminal. A nil
// home manager is used by narrow handler tests that do not model containers;
// those tests retain the old roster-only answer.
func (s *Server) agentInstalled(member domain.MemberID, executable string) bool {
	if s.cfg.Homes == nil {
		return true
	}
	home, err := s.cfg.Homes.Path(member)
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(home, ".local", "bin", executable))
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
