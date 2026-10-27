package assistant

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func listed(name string, installed, adapter, login bool) protocol.AgentInfo {
	return protocol.AgentInfo{Name: name, DisplayName: name, Installed: installed, EnhancedInstalled: adapter, LoginFound: &login}
}

func TestReadiness(t *testing.T) {
	agents := readiness([]protocol.AgentInfo{
		listed("opencode", true, true, true),
		listed("codex", true, true, false),
		listed("claude", false, false, true),
	})
	want := []protocol.AssistantAgent{
		{Name: "claude", DisplayName: "claude", Ready: true},
		{Name: "codex", DisplayName: "codex", Reason: "No sign-in found"},
	}
	if len(agents) != len(want) {
		t.Fatalf("readiness = %+v, want only claude and codex", agents)
	}
	for i := range want {
		if agents[i] != want[i] {
			t.Errorf("agent %d = %+v, want %+v", i, agents[i], want[i])
		}
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
