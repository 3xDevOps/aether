package harness

import (
	"strings"

	"github.com/3xDevOps/Aether/internal/agentstatus"
)

// SessionPlaceholder is replaced by a stored agent session id in ResumeArgs.
const SessionPlaceholder = "{session}"

// Enhanced says how an agent speaks the Agent Client Protocol (ACP), which
// is what an enhanced run drives it through.
type Enhanced string

const (
	// EnhancedNative: the agent's own CLI serves ACP (ACPArgs, no install).
	EnhancedNative Enhanced = "native"
	// EnhancedAdapter: a separate adapter package serves ACP (ACPInstall).
	EnhancedAdapter Enhanced = "adapter"
	// EnhancedNone: the agent can run only in its own terminal.
	EnhancedNone Enhanced = "none"
)

// ACPInstall is an ACP adapter installed from npm into the member home's
// ~/.local, pinned to the version recorded in acpregistry.json, the
// vendored snapshot of the ACP registry.
type ACPInstall struct {
	RegistryID string
	Package    string
	Version    string
	// Binary is the executable the package links into ~/.local/bin.
	Binary string
}

// The adapters the registry pins. Versions move only together with
// acpregistry.json; TestACPRegistryPins holds them equal.
var (
	claudeACP = &ACPInstall{RegistryID: "claude-acp", Package: "@agentclientprotocol/claude-agent-acp", Version: "0.86.0", Binary: "claude-agent-acp"}
	codexACP  = &ACPInstall{RegistryID: "codex-acp", Package: "@agentclientprotocol/codex-acp", Version: "2.1.1", Binary: "codex-acp"}
	piACP     = &ACPInstall{RegistryID: "pi-acp", Package: "pi-acp", Version: "0.0.34", Binary: "pi-acp"}
)

func (p Profile) EnhancedSupport() Enhanced {
	switch {
	case p.ACPInstall != nil:
		return EnhancedAdapter
	case len(p.ACPArgs) > 0:
		return EnhancedNative
	}
	return EnhancedNone
}

// Switchable reports whether a live run of the agent can move between its
// terminal and its ACP server, keeping the conversation.
func (p Profile) Switchable() bool {
	return p.ACPSessionShared && p.SwitchVerified && len(p.ResumeArgs) > 0 && len(p.ACPArgs) > 0
}

// ResumeCommand is ResumeArgs reopening session.
func (p Profile) ResumeCommand(session string) []string {
	out := make([]string, len(p.ResumeArgs))
	for i, a := range p.ResumeArgs {
		out[i] = strings.ReplaceAll(a, SessionPlaceholder, session)
	}
	return out
}

// InstallCommand is the shell command that installs the agent into the member home.
func (p Profile) InstallCommand(enhanced bool) string {
	if p.InstallScript == "" || !enhanced || p.ACPInstall == nil {
		return p.InstallScript
	}
	return p.InstallScript + " && " + p.ACPInstall.installLine()
}

// Label is the name the agent is shown by: its vendor's product name for a
// shipped profile, the registered name otherwise.
func (p Profile) Label() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Name
}

func (i *ACPInstall) installLine() string {
	return `npm install -g --prefix "$HOME/.local" ` + i.Package + "@" + i.Version
}

// withAdapterUpdate runs the agent's own update, then brings an adapter the
// member installed to its pinned version. The agent's script may end with
// exit, so it runs in a subshell. An adapter that is not installed is left
// alone: only an explicit install (InstallCommand) adds one.
func withAdapterUpdate(agent string, adapter *ACPInstall) string {
	return "(\n" + agent + "\n) || exit\n" + strings.NewReplacer("{pkg}", adapter.Package, "{exe}", adapter.Binary,
		"{version}", adapter.Version, "{server}", agentstatus.ReporterCommand).Replace(
		`dir="$HOME/.local/lib/node_modules/{pkg}"
[ -d "$dir" ] || exit 0
[ "$(node -p 'require(process.argv[1]).version' "$dir/package.json" 2>/dev/null)" = "{version}" ] && exit 0
command -v npm >/dev/null 2>&1 || { echo "npm is not in this environment's PATH, and {exe} updates through npm" >&2; exit 1; }
rm -rf "$HOME/.local/lib/.{exe}-update."*
stage=$(mktemp -d "$HOME/.local/lib/.{exe}-update.XXXXXX") || exit 1
trap 'rm -rf "$stage"' EXIT
npm install -g --prefix "$stage" "{pkg}@{version}" || exit 1
"{server}" package-exchange "$dir" "$stage/lib/node_modules/{pkg}"`)
}
