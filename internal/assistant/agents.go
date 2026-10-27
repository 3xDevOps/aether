package assistant

import (
	"errors"
	"fmt"
	"slices"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// agentOrder is the assistant's preference among agents.
var agentOrder = []string{"claude", "codex"}

// ErrNoAgent means none of the assistant's agents is ready.
var ErrNoAgent = errors.New("assistant: no agent is ready; sign in to Claude Code or Codex in your environment terminal")

// readiness describes the assistant's agents from an agent.list result. An
// agent is ready when its login was found in the member's home: the
// assistant installs the agent in its own container, never runs the
// member's installation.
func readiness(listed []protocol.AgentInfo) []protocol.AssistantAgent {
	out := make([]protocol.AssistantAgent, 0, len(agentOrder))
	for _, name := range agentOrder {
		i := slices.IndexFunc(listed, func(a protocol.AgentInfo) bool { return a.Name == name })
		if i < 0 {
			continue
		}
		a := listed[i]
		agent := protocol.AssistantAgent{Name: name, DisplayName: a.DisplayName, Ready: true}
		if a.LoginFound != nil && !*a.LoginFound {
			agent.Ready, agent.Reason = false, "No sign-in found"
		}
		out = append(out, agent)
	}
	return out
}

// choose picks the requested agent, else the stored one, else the first ready
// agent. It returns "" when none is ready and nothing was requested.
func choose(agents []protocol.AssistantAgent, requested, stored string) (string, error) {
	if requested != "" {
		i := slices.IndexFunc(agents, func(a protocol.AssistantAgent) bool { return a.Name == requested })
		switch {
		case i < 0:
			return "", fmt.Errorf("agent %q is not one the assistant can use", requested)
		case !agents[i].Ready:
			return "", fmt.Errorf("agent %q is not ready: %s", requested, agents[i].Reason)
		}
		return requested, nil
	}
	if i := slices.IndexFunc(agents, func(a protocol.AssistantAgent) bool { return a.Name == stored }); i >= 0 && agents[i].Ready {
		return stored, nil
	}
	for _, a := range agents {
		if a.Ready {
			return a.Name, nil
		}
	}
	return "", nil
}
