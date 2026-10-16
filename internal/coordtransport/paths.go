// Package coordtransport contains the shared wire-v3 client and the
// run-mounted coordination paths used by the CLI, MCP bridge, and lifecycle
// reporter.
package coordtransport

const (
	// MountDir is the read-only coordination directory mounted in a run.
	MountDir = "/run/aether"
	// SocketName is the v3 coordination socket inside a run's directory.
	// Every wire version has its own name so a stale bridge cannot parse a
	// newer status or message shape.
	SocketName = "coord3.sock"
	// AssistantMountDir is the read-only directory holding the assistant tool
	// socket in a member's environment terminal.
	AssistantMountDir = "/run/aether-assistant"
	// BinaryPath is the staged server binary path. It serves the MCP entry
	// point and harness lifecycle hooks.
	BinaryPath = "/opt/aether/aether-server"
	// CLIPath is the staged coordination CLI path.
	CLIPath = "/usr/local/bin/aether-internal"
	// SocketPath is the run's v3 coordination socket inside a container.
	SocketPath      = MountDir + "/" + SocketName
	NextCommandName = "next-command"
	// EnhancedEnv is "1" in an enhanced run's container. The server wakes
	// such an agent itself, so inbox hooks the agent loads stay silent.
	EnhancedEnv = "AETHER_ENHANCED"
	// ManagedHooksEnv is "1" when the run's launch loads Aether's inbox
	// integration itself, so the agent must not install a manual copy.
	ManagedHooksEnv = "AETHER_MANAGED_HOOKS"
	// BackgroundEnv is "1" in a Background run's container, whose agent
	// has no human to ask.
	BackgroundEnv = "AETHER_BACKGROUND"
	// WakeEnv is "1" in a Standard run whose loaded native integration
	// resumes the idle agent when mail arrives.
	WakeEnv = "AETHER_IDLE_WAKE"
	// RoleCardEnv is "1" where the agent's system prompt carries its role
	// card, so hooks and the MCP bridge do not repeat it.
	RoleCardEnv = "AETHER_ROLE_CARD"
)
