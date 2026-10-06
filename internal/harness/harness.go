// Package harness is the static registry of agent launch profiles: how
// Aether starts each supported agent CLI.

package harness

import (
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/agentstatus"
)

// TaskPlaceholder is replaced by the run's task prompt in argv templates.
const TaskPlaceholder = "{task}"

// DiscoveryInstruction points to live run identity, available capabilities, and
// any assignment. It carries no authority, identity, or task details.
const (
	DiscoveryInstruction = "Use `aether-internal skill` to read this run's live identity, capabilities, and any assignment before acting. Use only available capabilities and report only what you verified."
	DiscoveryFileName    = "discovery.md"
	// developer_instructions is the Codex config key for additional
	// instructions; model_instructions_file would replace built-ins.
	codexDiscoverySetting = "developer_instructions=\"" + DiscoveryInstruction + "\""
)

// CoordPlaceholder is replaced by the container path of the run's
// coordination directory in per-launch profile arguments and environment.
const CoordPlaceholder = "{aether}"

// Reporter says how much the harness's status reporter can tell the
// server: nothing, only that a turn ended, or every start and stop.
type Reporter int

const (
	// ReporterNone: working or waiting is inferred from silence alone.
	ReporterNone Reporter = iota
	// ReporterTurnEnd: reports turn ends only, so activity still un-parks the run.
	ReporterTurnEnd
	// ReporterFull: only the agent's own "working" un-parks the run.
	ReporterFull
)

// reporterNames is the persisted form of a Reporter; a name survives
// reordering the constants where the iota's number would not.
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
	Name         string
	TUIArgs      []string
	HeadlessArgs []string
	// ACPArgs optionally serves the Agent Client Protocol; its first value
	// may name a separate adapter executable.
	ACPArgs         []string
	Executable      string
	ProfileRoot     string
	CredentialPaths []string
	DenyNames       []string
}

func (d Definition) Validate() error {
	// The name becomes a host path segment and a store key, so it must never
	// address another member's credential home.
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
	if len(d.ACPArgs) > 0 {
		if err := validateExecutable(d.ACPArgs[0]); err != nil {
			return fmt.Errorf("harness: acp argv: %w", err)
		}
		if err := validateArgv(d.ACPArgs, d.ACPArgs[0]); err != nil {
			return fmt.Errorf("harness: acp argv: %w", err)
		}
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

// Profile converts a generic definition to a launch profile. The registry
// entry of the same name still supplies EnvPassthrough and Env, so an
// override that renames the executable keeps the variables the CLI needs.
func (d Definition) Profile() Profile {
	p := Profile{
		Name:            d.Name,
		TUIArgs:         append([]string(nil), d.TUIArgs...),
		HeadlessArgs:    append([]string(nil), d.HeadlessArgs...),
		ACPArgs:         append([]string(nil), d.ACPArgs...),
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

type Profile struct {
	Name        string
	DisplayName string
	// TaskPlaceholder is substituted with the task prompt.
	TUIArgs      []string
	HeadlessArgs []string
	// ACPArgs serves ACP over stdio: the agent's own CLI or ACPInstall's
	// adapter. Prompts travel over the protocol.
	ACPArgs []string
	// ACPInstall is the adapter package for an agent whose CLI does not serve ACP itself.
	ACPInstall *ACPInstall
	// ACPSessionShared means the ACP server and the TUI keep one session
	// store, so a session started in one resumes in the other.
	ACPSessionShared bool
	// ACPMode is the session mode an enhanced run starts in, for agents whose
	// default mode asks before most actions. Empty keeps the agent's default.
	ACPMode string
	// ACPAutoMode is the session mode a background run's one-shot session
	// starts in: the agent's mode that acts without asking. Empty keeps
	// ACPMode.
	ACPAutoMode string
	// ACPDefault makes an enhanced run the agent's default once its ACP
	// server is installed. Claude stays on its terminal by default: its
	// adapter runs on the Claude Agent SDK, whose terms favour API keys.
	ACPDefault bool
	// ResumeArgs reopens a stored session, SessionPlaceholder being its id.
	// Empty when the CLI cannot target one session.
	ResumeArgs []string
	// EnvPassthrough names server environment variables copied into run
	// containers when set; keys are never baked into images.
	EnvPassthrough []string
	// Env are fixed variables the CLI needs to start at all; a workspace
	// variable never overrides them.
	Env map[string]string
	// CredentialPaths is the allowlist of paths an account share exposes from
	// the owner's home. Resolve them with LoginPaths.
	CredentialPaths []string
	// InstallPaths are home-relative directories the ~/.local/bin launcher
	// links into, mounted read-only when a launch borrows the owner's install.
	InstallPaths []string
	// BorrowedState maps a home-relative JSON file to keys a borrowing launch
	// sets in the launcher's copy when absent, so the CLI starts signed in
	// instead of running first-time setup.
	BorrowedState map[string]map[string]any
	// PinLogin means the CLI replaces its login file by rename, so the file is
	// mounted in place in the owner's own containers too; the rename then fails
	// with EBUSY and the CLI falls back to rewriting the shared inode.
	PinLogin bool
	// LocalRoot is the home-relative profile directory (e.g. ".claude");
	// empty means no profile sync.
	LocalRoot string
	// DenyNames are basenames always excluded from profile snapshots.
	DenyNames []string
	// User is an explicit numeric "uid:gid" run user for images whose
	// configured user is named rather than numeric; empty resolves from
	// the image (see ResolveUser).
	User string
	// SteerSubmit ends a steering message written to stdin. Empty means one
	// Enter; opencode needs a second Enter to send.
	SteerSubmit string
	// DiscoveryArgs load the discovery hint on a taskless interactive launch.
	DiscoveryArgs []string
	// DiscoveryEnv is DiscoveryArgs for a harness with no startup flag for it.
	DiscoveryEnv map[string]string
	// DiscoveryFiles are written into the coordination directory before a
	// taskless interactive container exists.
	DiscoveryFiles map[string][]byte
	// NativeCoordination enables the run-scoped mailbox for coordinated
	// interactive runs with a task. Overrides never inherit it.
	NativeCoordination bool
	Reporter           Reporter
	// StatusArgs make the harness run the reporter on its lifecycle events.
	// Interactive runs only: a headless agent never waits for anyone.
	StatusArgs []string
	// StatusEnv is the environment alternative to StatusArgs. It is applied
	// after the workspace's variables, so the server's value wins.
	StatusEnv map[string]string
	// StatusFiles are the assets StatusArgs and StatusEnv point at. Reporter,
	// not this map, declares a reporter.
	StatusFiles map[string][]byte
	// InstallScript is the vendor's install command, run in the member's
	// terminal. It must install into ~/.local/bin.
	InstallScript string
	// UpdateScript updates the CLI in ~/.local/bin, is a cheap no-op when
	// current, and never touches plugins, channel or configuration.
	UpdateScript string
}

func (p Profile) SteerSuffix() string {
	if p.SteerSubmit == "" {
		return "\r"
	}
	return p.SteerSubmit
}

// installed is a shell case pattern matching `{exe} --version` against $latest.
func npmUpdateScript(pkg, exe, installed string, extra ...string) string {
	return strings.NewReplacer("{pkg}", pkg, "{exe}", exe, "{installed}", installed,
		"{server}", agentstatus.ReporterCommand,
		"{extra}", strings.Join(append([]string{""}, extra...), " ")).Replace(
		`dir="$HOME/.local/lib/node_modules/{pkg}"
if [ ! -d "$dir" ]; then
	for previous in "$HOME/.local/lib/.{exe}-update."*/previous; do
		[ -d "$previous" ] || continue
		mv "$previous" "$dir" || exit 1
		break
	done
fi
[ -d "$dir" ] || { echo "{exe} in ~/.local/bin was not installed with npm, so Aether cannot update it" >&2; exit 1; }
rm -rf "$HOME/.local/lib/.{exe}-update."*
command -v npm >/dev/null 2>&1 || { echo "npm is not in this environment's PATH, and {exe} updates through npm" >&2; exit 1; }
latest=$(npm view {pkg} version --fetch-retries=0) && [ -n "$latest" ] || exit 1
case "$({exe} --version)" in {installed}) exit 0 ;; esac
stage=$(mktemp -d "$HOME/.local/lib/.{exe}-update.XXXXXX") || exit 1
trap 'rm -rf "$stage"' EXIT
npm install -g --prefix "$stage"{extra} "{pkg}@$latest" || exit 1
"{server}" package-exchange "$dir" "$stage/lib/node_modules/{pkg}"`)
}

// profiles is the shipped registry. "custom" takes its command from
// deployment configuration and declares no credentials or key passthrough.
var profiles = map[string]Profile{
	"claude": {
		Name:        "claude",
		DisplayName: "Claude Code",
		TUIArgs:     []string{"claude", "--dangerously-skip-permissions", TaskPlaceholder},
		// Claude Code refuses stream-json in print mode without --verbose.
		HeadlessArgs:   []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", TaskPlaceholder},
		ACPArgs:        []string{claudeACP.Binary},
		ACPInstall:     claudeACP,
		ACPMode:        "auto",
		ResumeArgs:     []string{"claude", "--dangerously-skip-permissions", "--resume", SessionPlaceholder},
		EnvPassthrough: []string{"ANTHROPIC_API_KEY"},
		// Claude Code refuses --dangerously-skip-permissions as root unless the
		// environment declares a sandbox; the run container is one.
		Env:             map[string]string{"IS_SANDBOX": "1"},
		CredentialPaths: []string{".claude/.credentials.json"},
		// The native installer links ~/.local/bin/claude, by absolute
		// path, to ~/.local/share/claude/versions/<version>.
		InstallPaths: []string{".local/share/claude"},
		// Claude Code runs its setup wizard, whose sign-in step ignores an
		// existing login, until ~/.claude.json says setup is complete.
		BorrowedState: map[string]map[string]any{".claude.json": {"hasCompletedOnboarding": true}},
		// Claude Code writes the login to a temporary file and renames it
		// over the old one, and rewrites it in place only when that rename
		// fails with EXDEV, EPERM, EEXIST or EBUSY.
		PinLogin:      true,
		LocalRoot:     ".claude",
		DenyNames:     []string{".credentials.json", "credentials", ".claude.json"},
		DiscoveryArgs: []string{"--append-system-prompt", DiscoveryInstruction},
		// --settings merges a settings document registering the reporter over
		// the member's own, for this launch alone.
		Reporter:      ReporterFull,
		StatusArgs:    []string{"--settings", CoordPlaceholder + "/" + agentstatus.ClaudeSettingsName},
		StatusFiles:   map[string][]byte{agentstatus.ClaudeSettingsName: agentstatus.ClaudeSettings},
		InstallScript: "curl -fsSL https://claude.ai/install.sh | bash",
		UpdateScript:  withAdapterUpdate("claude update", claudeACP),
	},
	"codex": {
		Name:            "codex",
		DisplayName:     "Codex",
		TUIArgs:         []string{"codex", "--dangerously-bypass-approvals-and-sandbox", TaskPlaceholder},
		HeadlessArgs:    []string{"codex", "exec", "--json", "--dangerously-bypass-approvals-and-sandbox", TaskPlaceholder},
		ACPArgs:         []string{codexACP.Binary},
		ACPInstall:      codexACP,
		ACPMode:         "agent",
		ACPAutoMode:     "agent-full-access",
		ACPDefault:      true,
		ResumeArgs:      []string{"codex", "resume", "--dangerously-bypass-approvals-and-sandbox", SessionPlaceholder},
		EnvPassthrough:  []string{"OPENAI_API_KEY"},
		CredentialPaths: []string{".codex/auth.json"},
		LocalRoot:       ".codex",
		DenyNames:       []string{"auth.json", "keychain", "token.json"},
		DiscoveryArgs:   []string{"-c", codexDiscoverySetting},
		// Codex notifies only when a turn completes, so the run comes back on
		// the agent's own output.
		Reporter:   ReporterTurnEnd,
		StatusArgs: []string{"-c", agentstatus.CodexNotifySetting},
		// --prefix keeps the install inside the member's persistent home.
		InstallScript: "if command -v npm >/dev/null 2>&1; then npm install -g --prefix \"$HOME/.local\" @openai/codex; else echo \"npm is not in this environment's PATH, and codex installs through npm\" >&2; false; fi",
		// Not "codex update": it installs into the image's global npm
		// prefix, outside the home.
		UpdateScript: withAdapterUpdate(npmUpdateScript("@openai/codex", "codex", `*" $latest"`), codexACP),
	},
	"pi": {
		Name:         "pi",
		DisplayName:  "pi",
		TUIArgs:      []string{"pi", TaskPlaceholder},
		HeadlessArgs: []string{"pi", "-p", TaskPlaceholder},
		ACPArgs:      []string{piACP.Binary},
		ACPInstall:   piACP,
		// pi has no permission prompt, so there is no bypass flag to apply.
		EnvPassthrough:     []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialPaths:    []string{".pi/agent/auth.json"},
		LocalRoot:          ".pi",
		DenyNames:          []string{"auth.json", "oauth.json"},
		DiscoveryArgs:      []string{"--append-system-prompt", DiscoveryInstruction},
		Reporter:           ReporterFull,
		StatusArgs:         []string{"-e", CoordPlaceholder + "/" + agentstatus.PiExtensionName},
		StatusFiles:        map[string][]byte{agentstatus.PiExtensionName: agentstatus.PiExtension},
		NativeCoordination: true,
		// The vendor's install instruction adds --ignore-scripts.
		InstallScript: "if command -v npm >/dev/null 2>&1; then npm install -g --prefix \"$HOME/.local\" --ignore-scripts @earendil-works/pi-coding-agent; else echo \"npm is not in this environment's PATH, and pi installs through npm\" >&2; false; fi",
		// Not "pi update --self": it replaces files in place, under a
		// running or starting pi.
		UpdateScript: withAdapterUpdate(npmUpdateScript("@earendil-works/pi-coding-agent", "pi", `"$latest"`, "--ignore-scripts"), piACP),
	},
	// omp is a fork of pi and takes the same extension. It has a
	// permission prompt of its own, which --auto-approve bypasses.
	"omp": {
		Name:         "omp",
		DisplayName:  "oh-my-pi",
		TUIArgs:      []string{"omp", "--auto-approve", TaskPlaceholder},
		HeadlessArgs: []string{"omp", "-p", "--auto-approve", TaskPlaceholder},
		ACPArgs:      []string{"omp", "acp"},
		// omp acp and the TUI read and write one session store.
		ACPSessionShared: true,
		ACPDefault:       true,
		ResumeArgs:       []string{"omp", "--auto-approve", "--resume=" + SessionPlaceholder},
		EnvPassthrough:   []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		// omp keeps its login in a SQLite WAL database beside its config,
		// and a WAL database cannot be shared file by file.
		CredentialPaths: []string{".omp/agent"},
		LocalRoot:       ".omp",
		// The WAL and shm files hold provider keys too.
		DenyNames:          []string{"agent.db", "agent.db-wal", "agent.db-shm"},
		DiscoveryArgs:      []string{"--append-system-prompt", DiscoveryInstruction},
		Reporter:           ReporterFull,
		StatusArgs:         []string{"-e", CoordPlaceholder + "/" + agentstatus.PiExtensionName},
		StatusFiles:        map[string][]byte{agentstatus.PiExtensionName: agentstatus.PiExtension},
		NativeCoordination: true,
		InstallScript:      "curl -fsSL https://omp.sh/install | sh",
		UpdateScript:       "omp update",
	},
	"opencode": {
		Name:            "opencode",
		DisplayName:     "OpenCode",
		TUIArgs:         []string{"opencode", "--prompt=" + TaskPlaceholder},
		HeadlessArgs:    []string{"opencode", "run", TaskPlaceholder},
		ACPArgs:         []string{"opencode", "acp"},
		ACPDefault:      true,
		ResumeArgs:      []string{"opencode", "--session=" + SessionPlaceholder},
		EnvPassthrough:  []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialPaths: []string{".local/share/opencode/auth.json"},
		LocalRoot:       ".config/opencode",
		DenyNames:       []string{"auth.json", "token.json", "tokens.json"},
		// The TUI accepts steered text into its editor on the first
		// Enter and sends on the second.
		SteerSubmit: "\r\r",
		// opencode has no plugin flag, but it merges OPENCODE_CONFIG_CONTENT over
		// the member's config and concatenates plugin lists, so this adds the
		// reporter to whatever the member already loads.
		Reporter: ReporterFull,
		StatusEnv: map[string]string{
			"OPENCODE_CONFIG_CONTENT": `{"plugin":["file://` + CoordPlaceholder + "/" + agentstatus.OpenCodePluginName + `"]}`,
		},
		StatusFiles:        map[string][]byte{agentstatus.OpenCodePluginName: agentstatus.OpenCodePlugin},
		NativeCoordination: true,
		// Discovery must work without the reporter; the scheduler merges this
		// overlay with StatusEnv when both apply.
		DiscoveryEnv: map[string]string{
			"OPENCODE_CONFIG_CONTENT": `{"instructions":["` + CoordPlaceholder + "/" + DiscoveryFileName + `"]}`,
		},
		DiscoveryFiles: map[string][]byte{DiscoveryFileName: []byte(DiscoveryInstruction + "\n")},
		InstallScript:  "curl -fsSL https://opencode.ai/install | bash",
		// No UpdateScript: an upgrade can cross a major version that the
		// managed OpenCode launch wrapper refuses.
	},
	"custom": {Name: "custom"},
}

func Lookup(name string) (Profile, bool) {
	p, ok := profiles[name]
	return p, ok
}

func SubmitSequence(name string) string {
	if p, ok := Lookup(name); ok {
		return p.SteerSuffix()
	}
	return "\r"
}

func Profiles() []Profile {
	out := make([]Profile, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Profile) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// SetupHarnesses lists the harnesses that may drive environment setup, in
// display order. It is the single authority for the wizard, the inventory
// engine and the docs.
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

// An empty task is a taskless launch: tokens carrying the placeholder are
// dropped whole, so a prompt-only flag such as opencode's
// "--prompt={task}" does not dangle.
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

func (p Profile) DiscoveryLaunchArgs(dir string) []string {
	if len(p.DiscoveryArgs) == 0 || dir == "" {
		return nil
	}
	out := make([]string, 0, len(p.DiscoveryArgs))
	for _, a := range p.DiscoveryArgs {
		out = append(out, strings.ReplaceAll(a, CoordPlaceholder, dir))
	}
	return out
}

func (p Profile) DiscoveryLaunchEnv(dir string) map[string]string {
	if len(p.DiscoveryEnv) == 0 || dir == "" {
		return nil
	}
	out := make(map[string]string, len(p.DiscoveryEnv))
	for name, value := range p.DiscoveryEnv {
		out[name] = strings.ReplaceAll(value, CoordPlaceholder, dir)
	}
	return out
}

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

// HomeDir is /root for root and /home/aether otherwise, since typical
// images make /root untraversable for non-root users.
func HomeDir(user string) string {
	uid, _, _ := strings.Cut(user, ":")
	if uid == "" || uid == "0" {
		return "/root"
	}
	return "/home/aether"
}

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

// BorrowRoots are mounted read-only from the owner's home for a borrowing
// launch. bin and lib travel together because npm links a launcher into
// ../lib; InstallPaths cover installers that link by absolute path.
func (p Profile) BorrowRoots() []string {
	return append([]string{".local/bin", ".local/lib"}, p.InstallPaths...)
}

// LoginPaths returns CredentialPaths relative to the container home, each
// strictly below it, so a share never exposes the home or anything outside it.
func (p Profile) LoginPaths() ([]string, error) {
	out := make([]string, 0, len(p.CredentialPaths))
	for _, raw := range p.CredentialPaths {
		rel := HomeRelative(raw)
		if path.Clean(raw) != raw || rel == "." || !filepath.IsLocal(rel) || strings.ContainsAny(rel, "\\\x00") {
			return nil, fmt.Errorf("harness: %s login path %q is not a path below the home", p.Name, raw)
		}
		out = append(out, rel)
	}
	return out, nil
}

func (p Profile) ContainerLocalRoot(user string) string {
	if p.LocalRoot == "" {
		return ""
	}
	if path.IsAbs(p.LocalRoot) {
		return path.Clean(p.LocalRoot)
	}
	return path.Join(HomeDir(user), p.LocalRoot)
}

// ResolveUser resolves the numeric uid:gid the container and host-side
// ownership share; override wins. A bare uid implies gid = uid so the
// checkout is never handed to the root group, and a named image user needs
// the profile to supply the mapping.
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
