// Package harness is the static registry of agent launch profiles: how
// Aether starts each supported agent CLI. A profile carries the argv
// templates for tui and headless modes (auto/full-permission flags applied
// by default), the environment variables that pass plain API keys from
// server-side config into run containers and the fixed ones the CLI needs to
// start there at all, the container-side paths holding the harness's native
// login state (persisted per member under <data>/homes/<member-id>/ and
// bind-mounted read-write into every run), an explicit numeric uid:gid
// mapping for images whose configured user is named rather than numeric,
// whether the harness can be pointed at an MCP server config at launch (how
// conflict coordination reaches the agent; see docs/mcp-bridge.md).
//
// The registry is a map and a few functions, not a plugin system.

package harness

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/agentstatus"
)

// TaskPlaceholder is replaced by the run's task prompt in argv templates.
const TaskPlaceholder = "{task}"

// CoordPlaceholder is replaced by the container path of the run's
// coordination directory in StatusArgs. The directory is where the server
// writes the harness's status-reporter asset, and a profile must not have
// to know the mount point.
const CoordPlaceholder = "{aether}"

// Reporter says how much the harness's status reporter can tell the
// server: nothing, only that a turn ended, or every start and stop. It is
// what lets the scheduler tell an agent that has gone quiet because it is
// waiting for its member from one that has hung.
type Reporter int

const (
	// ReporterNone: the harness cannot report at all, so "working" or
	// "waiting" is inferred from silence alone.
	ReporterNone Reporter = iota
	// ReporterTurnEnd: the harness says when a turn ends but not when the
	// next one starts, so activity is still what un-parks the run.
	ReporterTurnEnd
	// ReporterFull: the harness reports both, so only the agent's own
	// "working" un-parks the run and a repaint while the member types
	// does not.
	ReporterFull
)

// reporterNames is the text form a Reporter travels in. The server records
// the reporter a run was launched with so a restart knows it without
// recomputing it, and a name survives reordering the constants where the
// iota's number would not.
var reporterNames = map[Reporter]string{
	ReporterNone:    "none",
	ReporterTurnEnd: "turn-end",
	ReporterFull:    "full",
}

func (r Reporter) String() string {
	if name, ok := reporterNames[r]; ok {
		return name
	}
	return "reporter(" + strconv.Itoa(int(r)) + ")"
}

// MarshalText and UnmarshalText are what put a Reporter in a JSON document.
func (r Reporter) MarshalText() ([]byte, error) {
	name, ok := reporterNames[r]
	if !ok {
		return nil, fmt.Errorf("harness: %s is not a reporter kind", r)
	}
	return []byte(name), nil
}

func (r *Reporter) UnmarshalText(text []byte) error {
	for kind, name := range reporterNames {
		if name == string(text) {
			*r = kind
			return nil
		}
	}
	return fmt.Errorf("harness: %q is not a reporter kind", text)
}

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
// scheduler and mount code. A definition names the argv and the paths, not
// the CLI's environment, so the registry entry of the same name still
// supplies EnvPassthrough and Env: an override that renames the executable
// must not silently drop the key passthrough or a variable the CLI needs to
// start at all. A registered name contributes only those environment
// settings; the generic definition supplies its own launch and control
// capabilities. A name the registry does not know contributes nothing.
func (d Definition) Profile() Profile {
	p := Profile{
		Name:            d.Name,
		TUIArgs:         append([]string(nil), d.TUIArgs...),
		HeadlessArgs:    append([]string(nil), d.HeadlessArgs...),
		CredentialPaths: append([]string(nil), d.CredentialPaths...),
		LocalRoot:       d.ProfileRoot,
		DenyNames:       append([]string(nil), d.DenyNames...),
	}
	if registered, ok := profiles[d.Name]; ok {
		p.EnvPassthrough = append([]string(nil), registered.EnvPassthrough...)
		p.Env = maps.Clone(registered.Env)
	}
	return p
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
	// Env are fixed environment variables every container run of this
	// harness needs. Unlike EnvPassthrough they carry no server-side
	// value: they state a launch requirement of the CLI itself, so a
	// workspace variable never overrides them.
	Env map[string]string
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
	// SteerSubmit is the literal bytes appended to a steering message
	// written to the agent's stdin (run.inject). TUIs submit on Enter
	// ("\r"), but some - opencode - treat the first Enter as "accept the
	// text into the editor" and need a second one to send. Empty means
	// the default single Enter; see SteerSuffix.
	SteerSubmit string
	// MCPConfigFlag is the harness's flag for a server-supplied MCP server
	// config file (Claude Code's "--mcp-config"). Set means the harness can
	// be pointed at the in-container coordination bridge at launch; empty
	// means it has no MCP registration and conflict coordination degrades
	// to the overlap notice alone.
	MCPConfigFlag string
	// Reporter is how much this harness's status reporter can say.
	Reporter Reporter
	// StatusArgs are appended to an interactive launch so the harness runs
	// the reporter on its own lifecycle events. CoordPlaceholder stands for
	// the coordination directory inside the container. Headless runs never
	// get them: a headless agent exits when it is done and never waits for
	// anyone.
	StatusArgs []string
	// StatusEnv is what a harness that has no flag for its reporter needs
	// in the launch environment instead: opencode loads a plugin named in
	// OPENCODE_CONFIG_CONTENT. CoordPlaceholder stands for the same
	// directory as in StatusArgs. It is applied after the workspace's own
	// variables, so these names carry the server's value whatever a
	// workspace sets them to, and like StatusArgs only an interactive run
	// gets it. That is not a guarantee the reporter loads: a harness has
	// other switches of its own, and Aether takes none of them away from
	// the member (docs/harnesses.md).
	StatusEnv map[string]string
	// StatusFiles are the assets StatusArgs and StatusEnv point at, written
	// into the run's coordination directory before the container exists,
	// keyed by the file name they take there. A harness whose reporter is
	// a launch flag alone needs none: the Reporter field above is what
	// declares a reporter, not this map.
	StatusFiles map[string][]byte
	// InstallScript is the vendor's documented install command, run in the
	// member's terminal (aether terminal). It must install into ~/.local/bin.
	// A failed install leaves the member in the terminal to install manually.
	InstallScript string
}

// SteerSuffix returns the bytes that follow a steering message: the
// profile's SteerSubmit, or the default single Enter.
func (p Profile) SteerSuffix() string {
	if p.SteerSubmit == "" {
		return "\r"
	}
	return p.SteerSubmit
}

// profiles is the shipped registry. "custom" is the escape hatch: its
// command comes from the deployment's run/workspace harness configuration
// (scheduler Config.Harnesses), never from here, and it declares no
// credentials or key passthrough.
var profiles = map[string]Profile{
	"claude": {
		Name:    "claude",
		TUIArgs: []string{"claude", "--dangerously-skip-permissions", TaskPlaceholder},
		// Claude Code refuses "--print --output-format stream-json" without
		// --verbose. The flag only adds records to the stream; the envelope
		// the adapter parses is unchanged.
		HeadlessArgs:   []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", TaskPlaceholder},
		EnvPassthrough: []string{"ANTHROPIC_API_KEY"},
		// Runs execute as root on the standard image, and Claude Code
		// refuses --dangerously-skip-permissions as root unless the
		// environment declares a sandbox. The run container is that
		// sandbox.
		Env:             map[string]string{"IS_SANDBOX": "1"},
		CredentialPaths: []string{".claude"},
		LocalRoot:       ".claude",
		DenyNames:       []string{".credentials.json", "credentials", ".claude.json"},
		MCPConfigFlag:   "--mcp-config",
		// Claude Code runs a command on every lifecycle event a settings
		// file registers a hook for, and --settings merges one more
		// settings document over the member's own for this launch alone.
		Reporter:      ReporterFull,
		StatusArgs:    []string{"--settings", CoordPlaceholder + "/" + agentstatus.ClaudeSettingsName},
		StatusFiles:   map[string][]byte{agentstatus.ClaudeSettingsName: agentstatus.ClaudeSettings},
		InstallScript: "curl -fsSL https://claude.ai/install.sh | bash",
	},
	"codex": {
		Name:            "codex",
		TUIArgs:         []string{"codex", "--dangerously-bypass-approvals-and-sandbox", TaskPlaceholder},
		HeadlessArgs:    []string{"codex", "exec", "--json", "--dangerously-bypass-approvals-and-sandbox", TaskPlaceholder},
		EnvPassthrough:  []string{"OPENAI_API_KEY"},
		CredentialPaths: []string{".codex"},
		LocalRoot:       ".codex",
		DenyNames:       []string{"auth.json", "keychain", "token.json"},
		// Codex runs an external program when a turn completes, and a -c
		// override points it at the reporter for this launch alone. It
		// says nothing when the next turn starts, so the run comes back
		// on the agent's own output.
		Reporter:   ReporterTurnEnd,
		StatusArgs: []string{"-c", agentstatus.CodexNotifySetting},
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
		// pi loads an extension with -e, and the one Aether ships reports
		// every start and stop of a turn.
		Reporter:    ReporterFull,
		StatusArgs:  []string{"-e", CoordPlaceholder + "/" + agentstatus.PiExtensionName},
		StatusFiles: map[string][]byte{agentstatus.PiExtensionName: agentstatus.PiExtension},
		// The vendor's install instruction adds --ignore-scripts.
		InstallScript: "command -v npm >/dev/null 2>&1 && npm install -g --prefix \"$HOME/.local\" --ignore-scripts @earendil-works/pi-coding-agent",
	},
	// omp is a fork of pi and takes the same extension. It has a
	// permission prompt of its own, which --auto-approve bypasses.
	"omp": {
		Name:            "omp",
		TUIArgs:         []string{"omp", "--auto-approve", TaskPlaceholder},
		HeadlessArgs:    []string{"omp", "-p", "--auto-approve", TaskPlaceholder},
		EnvPassthrough:  []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialPaths: []string{".omp"},
		LocalRoot:       ".omp",
		// omp keeps provider keys and OAuth tokens in the SQLite database
		// under ~/.omp/agent/, so the write-ahead log holds them too.
		DenyNames:     []string{"agent.db", "agent.db-wal", "agent.db-shm"},
		Reporter:      ReporterFull,
		StatusArgs:    []string{"-e", CoordPlaceholder + "/" + agentstatus.PiExtensionName},
		StatusFiles:   map[string][]byte{agentstatus.PiExtensionName: agentstatus.PiExtension},
		InstallScript: "curl -fsSL https://omp.sh/install | sh",
	},
	"opencode": {
		Name:            "opencode",
		TUIArgs:         []string{"opencode", "--prompt=" + TaskPlaceholder},
		HeadlessArgs:    []string{"opencode", "run", TaskPlaceholder},
		EnvPassthrough:  []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialPaths: []string{".local/share/opencode"},
		LocalRoot:       ".local/share/opencode",
		DenyNames:       []string{"auth.json", "token.json", "tokens.json"},
		// The TUI accepts steered text into its editor on the first
		// Enter and sends on the second.
		SteerSubmit: "\r\r",
		// opencode has no flag for a plugin, but it merges the inline JSON
		// config in OPENCODE_CONFIG_CONTENT over the member's own and
		// concatenates the plugin lists, so naming the reporter there adds
		// it to whatever the member already loads. It reports both ends of
		// a turn: session.status busy and session.idle.
		Reporter: ReporterFull,
		StatusEnv: map[string]string{
			"OPENCODE_CONFIG_CONTENT": `{"plugin":["file://` + CoordPlaceholder + "/" + agentstatus.OpenCodePluginName + `"]}`,
		},
		StatusFiles:   map[string][]byte{agentstatus.OpenCodePluginName: agentstatus.OpenCodePlugin},
		InstallScript: "curl -fsSL https://opencode.ai/install | bash",
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
// omp, opencode and custom stay launchable for runs but are never offered
// here, and the deterministic fake harness is a scheduler registration,
// not a registry profile.
func SetupHarnesses() []Profile {
	out := make([]Profile, 0, 3)
	for _, name := range []string{"claude", "codex", "pi"} {
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

// StatusLaunchArgs renders StatusArgs, the arguments appended to an
// interactive run's launch command so the harness reports its own state,
// with CoordPlaceholder replaced by dir, the container path of the
// coordination directory. Nil for a harness that takes no arguments for
// its reporter, whether because it has none or because it loads it from
// the environment instead (StatusLaunchEnv).
func (p Profile) StatusLaunchArgs(dir string) []string {
	if len(p.StatusArgs) == 0 || dir == "" {
		return nil
	}
	out := make([]string, 0, len(p.StatusArgs))
	for _, a := range p.StatusArgs {
		out = append(out, strings.ReplaceAll(a, CoordPlaceholder, dir))
	}
	return out
}

// StatusLaunchEnv renders StatusEnv, the environment variables an
// interactive run needs for the harness to load its reporter, with
// CoordPlaceholder replaced by dir, the container path of the coordination
// directory. Nil for a harness whose reporter needs no environment.
func (p Profile) StatusLaunchEnv(dir string) map[string]string {
	if len(p.StatusEnv) == 0 || dir == "" {
		return nil
	}
	out := make(map[string]string, len(p.StatusEnv))
	for name, value := range p.StatusEnv {
		out[name] = strings.ReplaceAll(value, CoordPlaceholder, dir)
	}
	return out
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
