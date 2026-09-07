package sshd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func newAgentTestServer(t *testing.T) (*Server, *domain.Member) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	member := &domain.Member{DisplayName: "member", TailnetLogin: "member@example.com", Role: domain.RoleCollaborator}
	if createMemberErr := db.CreateMember(ctx, member); createMemberErr != nil {
		t.Fatal(createMemberErr)
	}
	return &Server{cfg: Config{Store: db}}, member
}

func validAgentDefinition() protocol.AgentDefinition {
	return protocol.AgentDefinition{
		Name:         "mybot",
		Executable:   "mybot",
		TUIArgs:      []string{"mybot", harness.TaskPlaceholder},
		HeadlessArgs: []string{"mybot", "-p", harness.TaskPlaceholder},
	}
}

func callAgentRegister(t *testing.T, s *Server, member domain.MemberID, def protocol.AgentDefinition) (any, *protocol.Error) {
	t.Helper()
	raw, err := json.Marshal(protocol.AgentRegisterParams{Definition: def})
	if err != nil {
		t.Fatal(err)
	}
	return s.agentRegister(context.Background(), member, raw)
}

func shippedAgentNames() []string {
	var names []string
	for _, p := range harness.Profiles() {
		if p.Name != "custom" {
			names = append(names, p.Name)
		}
	}
	return names
}

func TestAgentRegisterRoundTripsThroughList(t *testing.T) {
	s, member := newAgentTestServer(t)
	def := validAgentDefinition()
	result, rpcErr := callAgentRegister(t, s, member.ID, def)
	if rpcErr != nil {
		t.Fatalf("agentRegister: %+v", rpcErr)
	}
	echoed, ok := result.(protocol.AgentRegisterResult)
	if !ok {
		t.Fatalf("agentRegister result type %T", result)
	}
	if echoed.Definition.Name != def.Name || echoed.Definition.Executable != def.Executable {
		t.Fatalf("agentRegister echoed %+v, want %+v", echoed.Definition, def)
	}
	listResult, rpcErr := s.agentList(context.Background(), member.ID, nil)
	if rpcErr != nil {
		t.Fatalf("agentList: %+v", rpcErr)
	}
	list, ok := listResult.(protocol.AgentListResult)
	if !ok {
		t.Fatalf("agentList result type %T", listResult)
	}
	found := false
	for i, a := range list.Agents {
		if i > 0 && list.Agents[i-1].Name > a.Name {
			t.Fatalf("agentList not sorted: %q after %q", a.Name, list.Agents[i-1].Name)
		}
		if a.Name == def.Name {
			found = true
			if a.Source != "member" {
				t.Fatalf("registered agent source = %q, want member", a.Source)
			}
		}
	}
	if !found {
		t.Fatalf("registered agent %q missing from list %+v", def.Name, list.Agents)
	}
	if want := len(shippedAgentNames()) + 1; len(list.Agents) != want {
		t.Fatalf("agentList returned %d agents, want %d", len(list.Agents), want)
	}
}

func TestAgentRegisterRejections(t *testing.T) {
	shipped := validAgentDefinition()
	shipped.Name = "claude"
	reserved := validAgentDefinition()
	reserved.Name = "custom"
	reservedFake := validAgentDefinition()
	reservedFake.Name = "fake"
	invalid := validAgentDefinition()
	invalid.TUIArgs = []string{"otherbot", harness.TaskPlaceholder}
	tests := []struct {
		name    string
		def     protocol.AgentDefinition
		mention string
	}{
		{"shipped name", shipped, "claude"},
		{"reserved custom", reserved, "reserved"},
		{"reserved fake", reservedFake, "reserved"},
		{"argv executable mismatch", invalid, "does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, member := newAgentTestServer(t)
			_, rpcErr := callAgentRegister(t, s, member.ID, tt.def)
			if rpcErr == nil {
				t.Fatalf("agentRegister accepted %+v", tt.def)
			}
			if !strings.Contains(rpcErr.Message, tt.mention) {
				t.Fatalf("error %q does not mention %q", rpcErr.Message, tt.mention)
			}
		})
	}
}

func TestAgentListFreshMemberReturnsShippedSet(t *testing.T) {
	s, member := newAgentTestServer(t)
	result, rpcErr := s.agentList(context.Background(), member.ID, nil)
	if rpcErr != nil {
		t.Fatalf("agentList: %+v", rpcErr)
	}
	list, ok := result.(protocol.AgentListResult)
	if !ok {
		t.Fatalf("agentList result type %T", result)
	}
	want := shippedAgentNames()
	if len(list.Agents) != len(want) {
		t.Fatalf("agentList returned %d agents, want %d", len(list.Agents), len(want))
	}
	got := make(map[string]bool, len(list.Agents))
	for _, a := range list.Agents {
		if a.Source != "shipped" {
			t.Fatalf("fresh member agent %q source = %q, want shipped", a.Name, a.Source)
		}
		if p, ok := harness.Lookup(a.Name); ok && a.InstallScript != p.InstallScript {
			t.Fatalf("shipped agent %q install script = %q, want %q", a.Name, a.InstallScript, p.InstallScript)
		}
		got[a.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("shipped agent %q missing from list", name)
		}
	}
	if got["custom"] {
		t.Fatal("agentList must exclude the custom escape hatch")
	}
}

func TestAgentListReportsExecutablesInTheMemberHome(t *testing.T) {
	s, member := newAgentTestServer(t)
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Homes = homes
	home, err := homes.Path(member.ID)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	result, rpcErr := s.agentList(context.Background(), member.ID, nil)
	if rpcErr != nil {
		t.Fatalf("agentList: %+v", rpcErr)
	}
	list := result.(protocol.AgentListResult)
	installed := make(map[string]bool, len(list.Agents))
	for _, agent := range list.Agents {
		installed[agent.Name] = agent.Installed
	}
	if !installed["claude"] {
		t.Fatal("claude should be installed")
	}
	if installed["codex"] {
		t.Fatal("codex should not be installed")
	}
}

func TestAgentListResolvesContainerSymlinks(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name      string
		target    string
		installed bool
	}{
		{"root home", "/root/.local/share/claude/versions/test", true},
		{"non-root home", "/home/aether/.local/share/claude/versions/test", true},
		{"relative", "../share/claude/versions/test", true},
		{"directory symlink", "/root/.local/share/alias/test", true},
		{"symlink then parent", "/root/.local/share/alias/../versions/test", true},
		{"broken", "/root/.local/share/claude/versions/missing", false},
		{"directory", "/root/.local/share/claude/versions", false},
		{"not executable", "/root/.local/share/claude/versions/plain", false},
		{"loop", "claude", false},
		{"host absolute path", outside, false},
		{"relative escape", "../../../../../" + outside[1:], false},
		{"absolute escape", "/root/../" + outside[1:], false},
		{"home prefix lookalike", "/root-other/tool", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, member := newAgentTestServer(t)
			homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
			if err != nil {
				t.Fatal(err)
			}
			s.cfg.Homes = homes
			home, err := homes.Path(member.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{".local/bin", ".local/share/claude/versions"} {
				if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(home, ".local/share/claude/versions/test"), []byte("#!/bin/sh\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tt.target, filepath.Join(home, ".local/bin/claude")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/root/.local/share/claude/versions", filepath.Join(home, ".local/share/alias")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".local/share/claude/versions/plain"), []byte("plain"), 0o600); err != nil {
				t.Fatal(err)
			}
			// A fresh handler must discover an existing installation without registration.
			s = &Server{cfg: s.cfg}
			result, perr := s.agentList(context.Background(), member.ID, nil)
			if perr != nil {
				t.Fatal(perr)
			}
			for _, agent := range result.(protocol.AgentListResult).Agents {
				if agent.Name == "claude" && agent.Installed != tt.installed {
					t.Fatalf("claude installed = %v, want %v", agent.Installed, tt.installed)
				}
			}
		})
	}
}

func TestAgentListDiscoversSharedAccountInstallations(t *testing.T) {
	s, owner := newAgentTestServer(t)
	ctx := context.Background()
	grantee := &domain.Member{DisplayName: "grantee", TailnetLogin: "grantee@example.com", Role: domain.RoleCollaborator}
	if err := s.cfg.Store.CreateMember(ctx, grantee); err != nil {
		t.Fatal(err)
	}
	homes, err := memberhome.New(filepath.Join(t.TempDir(), "homes"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Homes = homes
	home, err := homes.Path(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(home, ".local/bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "agent-version"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "mybot"} {
		if err = os.Symlink("/root/agent-version", filepath.Join(home, ".local/bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, perr := callAgentRegister(t, s, owner.ID, validAgentDefinition()); perr != nil {
		t.Fatal(perr)
	}
	raw, err := json.Marshal(protocol.AgentListParams{AccountMemberID: string(owner.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := s.agentList(ctx, grantee.ID, raw); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("unshared account discovery = %v, want denied", perr)
	}
	if err := s.cfg.Store.ShareAccount(ctx, owner.ID, grantee.ID); err != nil {
		t.Fatal(err)
	}
	result, perr := s.agentList(ctx, grantee.ID, raw)
	if perr != nil {
		t.Fatal(perr)
	}
	installed := map[string]bool{}
	for _, agent := range result.(protocol.AgentListResult).Agents {
		installed[agent.Name] = agent.Installed
	}
	if !installed["claude"] || !installed["mybot"] {
		t.Fatalf("shared account installations = %v", installed)
	}
	result, perr = s.agentList(ctx, grantee.ID, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	for _, agent := range result.(protocol.AgentListResult).Agents {
		if agent.Installed || agent.Name == "mybot" {
			t.Fatalf("owner installation leaked into grantee's own account: %+v", agent)
		}
	}
}
