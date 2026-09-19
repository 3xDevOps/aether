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
	// BinaryPath is the staged server binary path. It serves the MCP entry
	// point and harness lifecycle hooks.
	BinaryPath = "/opt/aether/aether-server"
	// CLIPath is the staged coordination CLI path.
	CLIPath = "/usr/local/bin/aether-internal"
	// SocketPath is the run's v3 coordination socket inside a container.
	SocketPath = MountDir + "/" + SocketName
)
