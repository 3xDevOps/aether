package sshd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

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
		installed, err := s.agentInstalled(account, p.TUIArgs[0])
		if err != nil {
			return nil, rpcError(fmt.Errorf("check agent %q: %w", p.Name, err))
		}
		agents = append(agents, protocol.AgentInfo{
			Name:          p.Name,
			Source:        "shipped",
			Installed:     installed,
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
		installed, err := s.agentInstalled(account, def.Executable)
		if err != nil {
			return nil, rpcError(fmt.Errorf("check agent %q: %w", row.Name, err))
		}
		agents = append(agents, protocol.AgentInfo{
			Name:      row.Name,
			Source:    "member",
			Installed: installed,
		})
	}
	// Shipped names and a member's rows are each sorted, but the merged
	// view must be sorted by name across both sources.
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return protocol.AgentListResult{Agents: agents}, nil
}

func (s *Server) agentInstalled(member domain.MemberID, executable string) (bool, error) {
	if s.cfg.Homes == nil {
		return true, nil
	}
	home, err := s.cfg.Homes.Path(member)
	if err != nil {
		return false, err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()

	// Vendor installers use absolute container-home symlinks. Resolve each
	// component explicitly; os.Root confines even concurrent symlink swaps
	// to this account's home instead of following a link on the server host.
	pending := []string{".local", "bin", executable}
	resolved := []string{}
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return false, nil
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		candidate := filepath.Join(append(resolved, part)...)
		info, err := root.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return false, nil
			}
			target, err := root.Readlink(candidate)
			if err != nil {
				return false, err
			}
			if path.IsAbs(target) {
				// Strip only the home prefix: cleaning before resolving a
				// symlink followed by ".." would change its meaning.
				switch {
				case strings.HasPrefix(target, harness.HomeDir("")+"/"):
					target = strings.TrimPrefix(target, harness.HomeDir("")+"/")
				case strings.HasPrefix(target, harness.HomeDir("1000")+"/"):
					target = strings.TrimPrefix(target, harness.HomeDir("1000")+"/")
				default:
					return false, nil
				}
				resolved = nil
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		if len(pending) == 0 {
			return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0, nil
		}
		if !info.IsDir() {
			return false, nil
		}
		resolved = append(resolved, part)
	}
	return false, nil
}
