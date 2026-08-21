// Package harness is the static registry of agent launch profiles: how
// Aether starts each supported agent CLI. A profile carries the argv
// templates for tui and headless modes (auto/full-permission flags applied
// by default), the environment variables that pass plain API keys from
// server-side config into run containers, the container-side paths holding
// the harness's native login state (persisted per member under
// <data>/homes/<member-id>/<harness>/ and bind-mounted read-write into
// every run so token refreshes persist), an explicit numeric uid:gid
// mapping for images whose configured user is named rather than numeric,
// and whether the harness can be pointed at an MCP server config at launch
// (how conflict coordination reaches the agent; see docs/mcp-bridge.md).
//
// The registry is a map and a few functions, not a plugin system.
package harness

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/3xDevOps/Aether/internal/runtime"
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

// ValidateMemberDefinition applies Validate plus the restrictions that hold
// only for member-supplied definitions: no path may live under the
// tool-snapshot mount (~/.local). Runs mount the immutable snapshot
// read-only there, so a member credential path beneath it would either be
// shadowed or punch a writable hole through snapshot immutability. Trusted
// administrator definitions keep the capability (the opencode pattern).
func ValidateMemberDefinition(d Definition) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.ProfileRoot != "" {
		if err := rejectToolPath(d.ProfileRoot); err != nil {
			return fmt.Errorf("harness: profile root: %w", err)
		}
	}
	for _, credential := range d.CredentialPaths {
		if err := rejectToolPath(credential); err != nil {
			return fmt.Errorf("harness: credential path: %w", err)
		}
	}
	return nil
}

func rejectToolPath(raw string) error {
	clean := path.Clean(raw)
	for _, root := range []string{"/root/.local", "/home/aether/.local"} {
		if isPathWithin(clean, root) {
			return fmt.Errorf("path %q is under the tool mount %s", raw, root)
		}
	}
	return nil
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
	// CredentialPaths are home-relative container-side paths holding the
	// harness's native login state (e.g. ".claude"). Each is persisted
	// per member on the host and mounted read-write into the member's
	// runs at HomeDir(user)/<path>. Directories only: a bind source that
	// does not exist yet is created as a directory before the container
	// starts. This is the login-home list; it is independent of LocalRoot.
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
	// ResumeFlag is the harness's flag for continuing the conversation it
	// last had in the working directory, used when a run interrupted by a
	// server reboot is relaunched. Empty means the harness has no such
	// flag and a relaunch starts the agent fresh - the failure table's
	// "where the adapter supports them".
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
		ResumeFlag:      "--continue",
		MCPConfigFlag:   "--mcp-config",
	},
	"codex": {
		Name:            "codex",
		TUIArgs:         []string{"codex", "--dangerously-bypass-approvals-and-sandbox", TaskPlaceholder},
		HeadlessArgs:    []string{"codex", "exec", "--json", "--dangerously-bypass-approvals-and-sandbox", TaskPlaceholder},
		EnvPassthrough:  []string{"OPENAI_API_KEY"},
		CredentialPaths: []string{".codex"},
		LocalRoot:       ".codex",
		DenyNames:       []string{"auth.json", "keychain", "token.json"},
	},
	"aider": {
		Name:            "aider",
		TUIArgs:         []string{"aider", "--yes-always", "--message", TaskPlaceholder},
		HeadlessArgs:    []string{"aider", "--yes-always", "--no-pretty", "--no-stream", "--message", TaskPlaceholder},
		EnvPassthrough:  []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialPaths: []string{".aider"},
		LocalRoot:       ".aider",
		DenyNames:       []string{".env", "api_key", "api_keys"},
	},
	"opencode": {
		Name:            "opencode",
		TUIArgs:         []string{"opencode", "--prompt", TaskPlaceholder},
		HeadlessArgs:    []string{"opencode", "run", TaskPlaceholder},
		EnvPassthrough:  []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		CredentialPaths: []string{".local/share/opencode"},
		LocalRoot:       ".local/share/opencode",
		DenyNames:       []string{"auth.json", "token.json", "tokens.json"},
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

// Argv instantiates an argv template with the run's task.
func Argv(template []string, task string) []string {
	out := make([]string, len(template))
	for i, a := range template {
		out[i] = strings.ReplaceAll(a, TaskPlaceholder, task)
	}
	return out
}

// ResumeArgv returns argv with the harness's resume flag inserted directly
// behind the executable, which is where every CLI that has one accepts it.
// An empty flag or an empty argv returns argv unchanged: a harness with no
// resume support relaunches from scratch rather than being handed a flag it
// does not know.
func ResumeArgv(argv []string, flag string) []string {
	if flag == "" || len(argv) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+1)
	out = append(out, argv[0], flag)
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

func containerHomePath(containerHome, configured string) string {
	if path.IsAbs(configured) {
		return path.Clean(configured)
	}
	return path.Join(containerHome, configured)
}

func hostCredentialPath(hostHome, containerHome, configured string) string {
	relative := configured
	if path.IsAbs(configured) {
		cleaned := path.Clean(configured)
		home := path.Clean(containerHome)
		relative = strings.TrimPrefix(cleaned, home+"/")
		if relative == cleaned {
			switch {
			case isPathWithin(cleaned, "/root"):
				relative = strings.TrimPrefix(cleaned, "/root/")
			case isPathWithin(cleaned, "/home/aether"):
				relative = strings.TrimPrefix(cleaned, "/home/aether/")
			}
		}
	}
	return filepath.Join(hostHome, filepath.FromSlash(relative))
}

// CredentialMounts maps the profile's home-relative or absolute credential
// paths into a member's harness home on the host and the run user's home in
// the container. Absolute paths are restricted by Definition.Validate.
func (p Profile) CredentialMounts(hostHome, containerHome string) []runtime.Mount {
	if hostHome == "" || len(p.CredentialPaths) == 0 {
		return nil
	}
	mounts := make([]runtime.Mount, 0, len(p.CredentialPaths))
	for _, cp := range p.CredentialPaths {
		mounts = append(mounts, runtime.Mount{
			HostPath:      hostCredentialPath(hostHome, containerHome, cp),
			ContainerPath: containerHomePath(containerHome, cp),
		})
	}
	return mounts
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
