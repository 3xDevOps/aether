package sshd

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
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
