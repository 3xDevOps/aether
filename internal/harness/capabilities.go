package harness

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Capabilities is what an agent can do in each launch mode beyond starting.
// Whether it has an Enhanced mode at all follows from its ACP command
// (EnhancedSupport). docs/harnesses.md's capability table is rendered from
// these; TestCapabilityTable prints it.
type Capabilities struct {
	// Status is what a Standard run reports of its turns. Enhanced runs
	// report every turn over ACP.
	Status Reporter
	// Hooks is how a Standard run learns that mail is waiting.
	Hooks Hooks
	// IdleWake: mail starts a turn in an idle Standard session instead of
	// waiting for the next hook boundary.
	IdleWake bool
	// Steer: a Standard run's terminal is attached to the agent's app-server,
	// through which the server learns of its turns and hands it mail and
	// messages, as a new turn when idle and as a steer mid-turn. The
	// scheduler drives Codex's app-server only.
	Steer bool
	// EnhancedDefault makes Enhanced the suggested mode once the agent's ACP
	// server is installed, and a Background run then uses it. Claude stays on
	// its terminal: its adapter runs on the Claude Agent SDK, whose terms
	// favour API keys.
	EnhancedDefault bool
	// ModeSwitch: a running session moves between Standard and Enhanced. It
	// needs an ACP server and a terminal that share one session store, and is
	// set only once the live switch test (ACP_LIVE=1, switch_live_test.go)
	// passes in both directions.
	ModeSwitch bool
	// BackgroundTasks: an Enhanced run follows the agent's background tasks
	// and the turns it starts on its own when one ends. Only Claude's adapter
	// reports them, so setting it also sends that adapter's session meta.
	BackgroundTasks bool
}

type Hooks int

const (
	// HooksNone: the agent reads mail only when it runs aether-internal inbox.
	HooksNone Hooks = iota
	// HooksByHand: Aether ships the hooks, and the member installs them.
	HooksByHand
	// HooksManaged: a coordinated Standard launch with a task loads them.
	HooksManaged
)

// SwarmRefusal is why a swarm integrator or worker of agent in mode would
// sit idle with unread mail, or nil. Only shipped agents declare
// capabilities; a custom agent's own command decides when it reads mail.
func SwarmRefusal(agent string, mode domain.LaunchMode) error {
	p, shipped := profiles[agent]
	if !shipped || agent == "custom" || mode != domain.LaunchTUI || p.Capabilities.IdleWake {
		return nil
	}
	if p.EnhancedSupport() == EnhancedNone {
		return fmt.Errorf("%s in Standard mode has no idle wake, and it has no Enhanced mode; start it as a Background worker", agent)
	}
	return fmt.Errorf("%s in Standard mode has no idle wake; start it in Enhanced", agent)
}
