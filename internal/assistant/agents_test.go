package assistant

import (
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func listed(name string, login bool) protocol.AgentInfo {
	return protocol.AgentInfo{Name: name, DisplayName: name, Installed: true, EnhancedInstalled: true, LoginFound: &login}
}

// TestReadiness takes the login from the assistant's own rule, not from the
// agent list, which looks in a shared account's home.
func TestReadiness(t *testing.T) {
	logins := map[string]bool{"claude": true}
	agents, err := readiness([]protocol.AgentInfo{
		listed("opencode", true),
		listed("codex", true),
		listed("claude", false),
	}, func(agent string) (bool, error) { return logins[agent], nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.AssistantAgent{
		{Name: "claude", DisplayName: "claude", Ready: true},
		{Name: "codex", DisplayName: "codex", Reason: "Not signed in for the assistant"},
	}
	if !slices.Equal(agents, want) {
		t.Fatalf("readiness = %+v, want %+v", agents, want)
	}
}

func TestChoose(t *testing.T) {
	ready := func(claude, codex bool) []protocol.AssistantAgent {
		return []protocol.AssistantAgent{
			{Name: "claude", Ready: claude, Reason: "No sign-in found"},
			{Name: "codex", Ready: codex, Reason: "No sign-in found"},
		}
	}
	tests := []struct {
		name              string
		agents            []protocol.AssistantAgent
		requested, stored string
		want              string
		wantErr           bool
	}{
		{name: "requested wins", agents: ready(true, true), requested: "codex", stored: "claude", want: "codex"},
		{name: "stored over order", agents: ready(true, true), stored: "codex", want: "codex"},
		{name: "first ready", agents: ready(true, true), want: "claude"},
		{name: "stored not ready", agents: ready(false, true), stored: "claude", want: "codex"},
		{name: "none ready", agents: ready(false, false)},
		{name: "requested not ready", agents: ready(false, true), requested: "claude", wantErr: true},
		{name: "requested unknown", agents: ready(true, true), requested: "pi", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := choose(tc.agents, tc.requested, tc.stored)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("choose = %q, %v; want %q, error %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
