// Package harness is the static registry of agent launch profiles: how
// Aether starts each supported agent CLI. A profile carries the argv
// templates for tui and headless modes (auto/full-permission flags applied
// by default), the environment variables that pass plain API keys from
// server-side config into run containers, the container-side paths holding
// the harness's native login state (persisted per member under
// <data>/homes/<member-id>/ and bind-mounted read-write into every run), an
// explicit numeric uid:gid mapping for images whose configured user is named
// rather than numeric, whether the harness can be pointed at an MCP server
// config at launch (how conflict coordination reaches the agent; see
// docs/mcp-bridge.md), and how it names a conversation so a relaunch resumes
// the interrupted run's own: a session ID pinned at launch where the CLI
// supports one, and "continue whatever ran here last" where it does not (see
// docs/failure-handling.md).
//
// The registry is a map and a few functions, not a plugin system.
package harness

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

// TaskPlaceholder is replaced by the run's task prompt in argv templates.
const TaskPlaceholder = "{task}"

// Definition is an administrator-supplied generic harness launch definition.
// Paths are absolute container paths so the server never has to infer where
// credentials live from an executable name.
type Definition struct {
	Name            string
	TUIArgs         []string
	HeadlessArgs    []string
	Executable      string
	ProfileRoot     string
	CredentialPaths []string
	DenyNames       []string
}

// Validate checks a generic definition before it can be used for a run.
func (d Definition) Validate() error {
	// The name becomes a host path segment (<homes>/<member>/<name>) and a
	// store key; it must be a plain single-segment identifier, never a
	// relative path that could address another member's credential home.
	if err := validateName(d.Name); err != nil {
		return err
	}
	if err := validateExecutable(d.Executable); err != nil {
		return err
	}
	if err := validateArgv(d.TUIArgs, d.Executable); err != nil {
		return fmt.Errorf("harness: tui argv: %w", err)
	}
	if err := validateArgv(d.HeadlessArgs, d.Executable); err != nil {
		return fmt.Errorf("harness: headless argv: %w", err)
	}
	if d.ProfileRoot != "" {
		if err := validateContainerPath(d.ProfileRoot); err != nil {
			return fmt.Errorf("harness: profile root: %w", err)
		}
	}
	for _, credential := range d.CredentialPaths {
		if err := validateContainerPath(credential); err != nil {
			return fmt.Errorf("harness: credential path: %w", err)
		}
		if d.ProfileRoot != "" && !isPathWithin(credential, d.ProfileRoot) {
			return fmt.Errorf("harness: credential path %q is outside profile root %q", credential, d.ProfileRoot)
		}
	}
	for _, denied := range d.DenyNames {
		if denied == "" || path.Base(denied) != denied || denied == "." || denied == ".." ||
			strings.ContainsAny(denied, `/\`) {
			return fmt.Errorf("harness: denied sync name %q is not a basename", denied)
		}
	}
	return nil
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("harness: definition name is required")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") ||
		strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("harness: definition name %q must be a plain name", name)
	}
	return nil
}

// ValidateMemberDefinition applies the generic definition validation to a
// member-owned definition.
func ValidateMemberDefinition(d Definition) error {
	return d.Validate()
}

func validateExecutable(executable string) error {
	if executable == "" {
		return fmt.Errorf("harness: executable is required")
	}
	if strings.ContainsAny(executable, `/\`) || executable == "." || executable == ".." {
		return fmt.Errorf("harness: executable %q must be a name, not a host path", executable)
	}
	if strings.ContainsRune(executable, 0) {
		return fmt.Errorf("harness: executable contains NUL")
	}
	return nil
}

func validateArgv(argv []string, executable string) error {
	if len(argv) == 0 {
		return fmt.Errorf("argv is required")
	}
	if argv[0] != executable {
		return fmt.Errorf("argv executable %q does not match %q", argv[0], executable)
	}
	for _, arg := range argv {
		if arg == "" || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("argv contains an empty or NUL argument")
		}
	}
	return nil
}

func validateContainerPath(raw string) error {
	if !path.IsAbs(raw) || strings.Contains(raw, `\`) {
		return fmt.Errorf("path %q must be an absolute container path", raw)
	}
	clean := path.Clean(raw)
	if clean != raw || clean == "/" || strings.HasPrefix(clean, "/proc/") ||
		strings.HasPrefix(clean, "/sys/") || strings.HasPrefix(clean, "/dev/") {
		return fmt.Errorf("path %q is not an allowed container path", raw)
	}
	if !isPathWithin(clean, "/root") && !isPathWithin(clean, "/home/aether") {
		return fmt.Errorf("path %q must be under /root or /home/aether", raw)
	}
	return nil
}

func isPathWithin(candidate, root string) bool {
	candidate, root = path.Clean(candidate), path.Clean(root)
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

// Profile converts a generic definition to the launch profile used by the
// scheduler and mount code.
func (d Definition) Profile() Profile {
	return Profile{
		Name:            d.Name,
		TUIArgs:         append([]string(nil), d.TUIArgs...),
		HeadlessArgs:    append([]string(nil), d.HeadlessArgs...),
		CredentialPaths: append([]string(nil), d.CredentialPaths...),
		LocalRoot:       d.ProfileRoot,
		DenyNames:       append([]string(nil), d.DenyNames...),
	}
}

// Profile is one agent harness's launch profile.
type Profile struct {
	// Name is the harness name runs reference (domain.Run.Harness).
	Name string
	// TUIArgs and HeadlessArgs are argv templates for the two launch
	// modes; TaskPlaceholder is substituted with the task prompt.
	TUIArgs      []string
	HeadlessArgs []string
	// EnvPassthrough names environment variables copied from the server
	// process into run containers when set (plain API-key harnesses;
	// keys are never baked into images).
	EnvPassthrough []string
	// CredentialPaths are home-relative paths holding the harness's native
	// login state (e.g. ".claude"). They are persisted in the member's
	// shared home and available read-write in every run. This is the
	// login-home list; it is independent of LocalRoot.
	CredentialPaths []string
	// LocalRoot is the home-relative directory captured as the agent
	// profile (e.g. ".claude"). Empty means no profile sync (custom).
	// The container target is filepath.ToSlash(path.Join(HomeDir(user), LocalRoot)).
	LocalRoot string
	// DenyNames are basenames always excluded from profile snapshots
	// (credentials, auth caches, keychains, known token files).
	DenyNames []string
	// User is an explicit numeric "uid:gid" run user for images whose
	// configured user is named rather than numeric; empty resolves from
	// the image (see ResolveUser).
	User string
	// SessionFlag pins the harness's conversation identity at launch
	// (Claude Code's "--session-id <uuid>"). The server generates one UUID
	// per run and records it on the run row, so relaunching that run names
	// the exact conversation instead of guessing. Empty means the harness
	// cannot pin a session and relaunch falls back to ResumeFlag.
	SessionFlag string
	// SessionResumeFlag resumes a pinned conversation by ID (Claude Code's
	// "--resume <uuid>"). It names the conversation outright, so it is
	// unaffected by every run mounting its checkout at the same container
	// path and sharing one credential home per member. Set it only
	// together with SessionFlag.
	SessionResumeFlag string
	// ResumeFlag is the harness's flag for continuing the conversation it
	// last had in the working directory. It is the fallback a relaunch
	// uses when no pinned session is available: a harness with no
	// SessionFlag, or a run row created before pinning existed. Empty
	// means the harness has no such flag and a relaunch starts the agent
	// fresh.
	//
	// The flag names no session. Every run mounts its checkout at the same
	// container path and shares one credential home per member, so what is
	// resumed is that member's most recent conversation at that path - not
	// necessarily the interrupted run's own, and not necessarily one from
	// the same workspace. See docs/failure-handling.md.
	ResumeFlag string
	// MCPConfigFlag is the harness's flag for a server-supplied MCP server
	// config file (Claude Code's "--mcp-config"). Set means the harness can
	// be pointed at the in-container coordination bridge at launch; empty
	// means it has no MCP registration and conflict coordination degrades
	// to the overlap notice alone.
	MCPConfigFlag string
	// InstallScript is the vendor's documented install command, run in the
	// member's terminal (aether terminal). It must install into ~/.local/bin.
	// A failed install leaves the member in the terminal to install manually.
	InstallScript string
}

// profiles is the shipped registry. "custom" is the escape hatch: its
// command comes from the deployment's run/workspace harness configuration
// (scheduler Config.Harnesses), never from here, and it declares no
// credentials or key passthrough.
var profiles = map[string]Profile{
	"claude": {
		Name:            "claude",
		TUIArgs:         []string{"claude", "--dangerously-skip-permissions", TaskPlaceholder},
		HeadlessArgs:    []string{"claude", "-p", "--output-format", "stream-json", "--dangerously-skip-permissions", TaskPlaceholder},
		EnvPassthrough:  []string{"ANTHROPIC_API_KEY"},
		CredentialPaths: []string{".claude"},
		LocalRoot:       ".claude",
		DenyNames:       []string{".credentials.json", "credentials", ".claude.json"},
		// claude --session-id refuses an ID that already names a
		// conversation ("Session ID <id> is already in use."), so it is a
		// launch-only flag and --resume replaces it on relaunch.
		SessionFlag:       "--session-id",
		SessionResumeFlag: "--resume",
		ResumeFlag:        "--continue",
		MCPConfigFlag:     "--mcp-config",
		InstallScript:     "curl -fsSL https://claude.ai/install.sh | bash",
	},
	"codex": {
		Name:            "codex",
		TUIArgs:         []string{"codex", "--dangerously-bypass-approvals-and-sandbox", TaskPlaceholder},
		HeadlessArgs:    []string{"codex", "exec", "--json", "--dangerously-bypass-approvals-and-sandbox", TaskPlaceholder},
		EnvPassthrough:  []string{"OPENAI_API_KEY"},
		CredentialPaths: []string{".codex"},
		LocalRoot:       ".codex",
		DenyNames:       []string{"auth.json", "keychain", "token.json"},
		// Codex ships via npm; --prefix keeps the install inside the
		// member's persistent home. Without npm in the image the member
		// installs manually, as before.
		InstallScript: "command -v npm >/dev/null 2>&1 && npm install -g --prefix \"$HOME/.local\" @openai/codex",
	},
	"pi": {
		Name:         "pi",
		TUIArgs:      []string{"pi", TaskPlaceholder},
		HeadlessArgs: []string{"pi", "-p", TaskPlaceholder},
		// pi has no permission prompt, so there is no bypass flag to apply.
		EnvPassthrough:  []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialPaths: []string{".pi"},
		LocalRoot:       ".pi",
		// pi stores provider keys and OAuth tokens under ~/.pi/agent/.
		DenyNames: []string{"auth.json", "oauth.json"},
		// pi -c continues the most recent session; sessions are organized
		// by working directory. pi has no launch-time session ID, so a
		// relaunch keeps the best-effort behavior: it resumes whichever of
		// the member's conversations at that path spoke last.
		ResumeFlag: "--continue",
		// The vendor's install instruction adds --ignore-scripts.
		InstallScript: "command -v npm >/dev/null 2>&1 && npm install -g --prefix \"$HOME/.local\" --ignore-scripts @earendil-works/pi-coding-agent",
	},
	"amp": {
		Name: "amp",
		// amp takes the message as a trailing positional in interactive
		// mode and refuses a seeded prompt that collides with one of its
		// subcommand names; real task prompts contain spaces and never do.
		TUIArgs:        []string{"amp", "--dangerously-allow-all", TaskPlaceholder},
		HeadlessArgs:   []string{"amp", "--dangerously-allow-all", "-x", TaskPlaceholder},
		EnvPassthrough: []string{"AMP_API_KEY"},
		// Settings live under ~/.config/amp; XDG data (secrets.json,
		// state.json) under ~/.local/share/amp in the member's home.
		CredentialPaths: []string{".config/amp", ".local/share/amp"},
		LocalRoot:       ".config/amp",
		DenyNames:       []string{"secrets.json", "state.json"},
		InstallScript:   "command -v npm >/dev/null 2>&1 && npm install -g --prefix \"$HOME/.local\" @ampcode/cli",
	},
	"opencode": {
		Name:            "opencode",
		TUIArgs:         []string{"opencode", "--prompt=" + TaskPlaceholder},
		HeadlessArgs:    []string{"opencode", "run", TaskPlaceholder},
		EnvPassthrough:  []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialPaths: []string{".local/share/opencode"},
		LocalRoot:       ".local/share/opencode",
		DenyNames:       []string{"auth.json", "token.json", "tokens.json"},
		InstallScript:   "curl -fsSL https://opencode.ai/install | bash",
	},
	"custom": {Name: "custom"},
}

// Lookup returns the shipped profile for name.
func Lookup(name string) (Profile, bool) {
	p, ok := profiles[name]
	return p, ok
}

// Profiles lists the shipped profiles sorted by name.
func Profiles() []Profile {
	out := make([]Profile, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Profile) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// SetupHarnesses lists the harnesses that may drive environment setup, in
// the order setup surfaces present them. This list is the single authority:
// the wizard, the local inventory engine, and the docs all follow it.
// opencode and custom stay launchable for runs but are never offered here,
// and the deterministic fake harness is a scheduler registration, not a
// registry profile.
func SetupHarnesses() []Profile {
	out := make([]Profile, 0, 4)
	for _, name := range []string{"claude", "codex", "pi", "amp"} {
		p, ok := profiles[name]
		if !ok {
			panic(fmt.Sprintf("harness: setup harness %q missing from the registry", name))
		}
		out = append(out, p)
	}
	return out
}

// Argv instantiates an argv template with the run's task. An empty task is a
// taskless launch (drop the user straight into the agent's interactive TUI
// with no seeded prompt): every argv token that carries the placeholder is
// dropped whole, so a flag whose only purpose is to deliver the prompt
// (opencode's "--prompt={task}") leaves with it rather than dangling with an
// empty value. A non-empty task substitutes in place as before.
func Argv(template []string, task string) []string {
	out := make([]string, 0, len(template))
	for _, a := range template {
		if task == "" && strings.Contains(a, TaskPlaceholder) {
			continue
		}
		out = append(out, strings.ReplaceAll(a, TaskPlaceholder, task))
	}
	return out
}

// WithFlag returns argv with flag - and value behind it, when value is not
// empty - inserted directly behind the executable, which is where every CLI
// that has one accepts it. An empty flag or an empty argv returns argv
// unchanged: a harness with no such flag is launched exactly as before
// rather than being handed a flag it does not know.
func WithFlag(argv []string, flag, value string) []string {
	if flag == "" || len(argv) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[0], flag)
	if value != "" {
		out = append(out, value)
	}
	return append(out, argv[1:]...)
}

// MCPArgs are the arguments appended to a run's launch command so the
// harness loads the MCP server config at configPath, a container-side
// path. Nil for a harness with no MCP registration: it is launched exactly
// as before and sees only the overlap notice.
func (p Profile) MCPArgs(configPath string) []string {
	if p.MCPConfigFlag == "" || configPath == "" {
		return nil
	}
	return []string{p.MCPConfigFlag, configPath}
}

// MCPConfig renders the config file MCPArgs points a harness at: the
// standard mcpServers document naming one stdio server. It is written into
// the run's coordination directory by the server, never into the worktree
// or the member's synced profile.
func MCPConfig(name, command string, args ...string) ([]byte, error) {
	type stdioServer struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args,omitempty"`
	}
	doc := struct {
		Servers map[string]stdioServer `json:"mcpServers"`
	}{Servers: map[string]stdioServer{name: {Type: "stdio", Command: command, Args: args}}}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("harness: render MCP config for %q: %w", name, err)
	}
	return out, nil
}

// HomeDir is the container-side home directory for a resolved run user:
// root (uid 0, or the empty root default) lives in /root; any other user
// gets /home/aether, since typical images make /root untraversable for
// non-root users. Docker creates bind-mount target directories on demand,
// and the scheduler exports HOME so harnesses look in the right place.
func HomeDir(user string) string {
	uid, _, _ := strings.Cut(user, ":")
	if uid == "" || uid == "0" {
		return "/root"
	}
	return "/home/aether"
}

// HomeRelative returns a path relative to the resolved container home.
// Absolute paths outside either supported home are returned cleaned.
func HomeRelative(p string) string {
	clean := path.Clean(p)
	for _, prefix := range []string{"/root/", "/home/aether/"} {
		if strings.HasPrefix(clean, prefix) {
			return strings.TrimPrefix(clean, prefix)
		}
	}
	if clean == "/root" || clean == "/home/aether" {
		return "."
	}
	return clean
}

// ContainerLocalRoot is the absolute container path of the profile
// directory for a resolved run user. Empty LocalRoot yields "".
func (p Profile) ContainerLocalRoot(user string) string {
	if p.LocalRoot == "" {
		return ""
	}
	if path.IsAbs(p.LocalRoot) {
		return path.Clean(p.LocalRoot)
	}
	return path.Join(HomeDir(user), p.LocalRoot)
}

// ResolveUser resolves the one numeric "uid:gid" a run's container and
// host-side ownership pass share. override is the profile's explicit
// mapping and wins when set; imageUser is the user the image is
// configured to run as. An empty image user means root ("0:0"); a numeric
// image user is accepted (a bare uid implies gid = uid, so the checkout
// is never handed to the root group); a named user cannot be mapped to
// host ownership and fails unless the profile supplies the mapping.
func ResolveUser(override, imageUser string) (string, error) {
	if override != "" {
		u, ok := normalizeUser(override)
		if !ok {
			return "", fmt.Errorf("harness: profile user %q must be numeric uid:gid", override)
		}
		return u, nil
	}
	if imageUser == "" {
		return "0:0", nil
	}
	u, ok := normalizeUser(imageUser)
	if !ok {
		return "", fmt.Errorf("harness: image user %q is not numeric; the harness profile must supply a uid:gid mapping", imageUser)
	}
	return u, nil
}

func normalizeUser(s string) (string, bool) {
	uid, gid, found := strings.Cut(s, ":")
	if !found {
		gid = uid
	}
	if !validID(uid) || !validID(gid) {
		return "", false
	}
	return uid + ":" + gid, true
}

// validID accepts decimal uids/gids up to 0xFFFFFFFE: 0xFFFFFFFF is the
// kernel's "no change" chown sentinel and must never reach a chown call.
func validID(s string) bool {
	n, err := strconv.ParseUint(s, 10, 32)
	return err == nil && n <= 0xFFFFFFFE
}
