package harness

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/pelletier/go-toml/v2"
)

func TestRegistryShipsTenProfiles(t *testing.T) {
	want := []string{"antigravity", "claude", "codex", "copilot", "cursor", "custom", "gemini", "omp", "opencode", "pi"}
	var got []string
	for _, p := range Profiles() {
		got = append(got, p.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Profiles() = %v, want %v", got, want)
	}
}

// Every harness with a command must carry the task placeholder in both
// modes and run its executable, which need not be its name; login-flow
// harnesses must declare their credential paths.
func TestProfileDefaults(t *testing.T) {
	executables := map[string]string{"antigravity": "agy", "cursor": "agent"}
	for _, name := range []string{"antigravity", "claude", "codex", "copilot", "cursor", "gemini", "omp", "opencode", "pi"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) missing", name)
		}
		executable := cmp.Or(executables[name], name)
		for mode, argv := range map[string][]string{"tui": p.TUIArgs, "headless": p.HeadlessArgs} {
			if len(argv) == 0 {
				t.Errorf("%s %s: no argv", name, mode)
				continue
			}
			if argv[0] != executable {
				t.Errorf("%s %s argv[0] = %q, want %q", name, mode, argv[0], executable)
			}
			if !slices.ContainsFunc(argv, func(a string) bool {
				return strings.Contains(a, TaskPlaceholder)
			}) {
				t.Errorf("%s %s argv %v missing task placeholder", name, mode, argv)
			}
		}
		if p.EnhancedSupport() == EnhancedNative && p.ACPArgs[0] != executable {
			t.Errorf("%s native acp argv[0] = %q, want %q", name, p.ACPArgs[0], executable)
		}
		if len(p.CredentialPaths) == 0 {
			t.Errorf("%s: no credential paths", name)
		}
		if logins, err := p.LoginPaths(); err != nil || !slices.Equal(logins, p.CredentialPaths) {
			t.Errorf("%s login paths = %v, %v; want the home-relative %v", name, logins, err, p.CredentialPaths)
		}
		for _, pinned := range p.PinnedLogins {
			if !slices.Contains(p.CredentialPaths, pinned) {
				t.Errorf("%s pins %q, which is not a login path", name, pinned)
			}
		}
	}
}

// The custom escape hatch ships no command of its own: the deployment's
// run/workspace config supplies it.
func TestCustomProfileIsEmpty(t *testing.T) {
	p, ok := Lookup("custom")
	if !ok {
		t.Fatal("custom profile missing")
	}
	if len(p.TUIArgs) != 0 || len(p.HeadlessArgs) != 0 || len(p.CredentialPaths) != 0 || len(p.EnvPassthrough) != 0 || p.User != "" || p.LocalRoot != "" || len(p.DenyNames) != 0 {
		t.Fatalf("custom profile must be empty, got %+v", p)
	}
}

func TestSetupHarnesses(t *testing.T) {
	want := []string{"claude", "codex", "pi"}
	var got []string
	for _, p := range SetupHarnesses() {
		got = append(got, p.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SetupHarnesses() names = %v, want %v", got, want)
	}
	for _, excluded := range []string{"opencode", "custom", "fake"} {
		if slices.Contains(got, excluded) {
			t.Errorf("SetupHarnesses() must not offer %q", excluded)
		}
	}
	for _, p := range SetupHarnesses() {
		registered, ok := Lookup(p.Name)
		if !ok {
			t.Fatalf("setup harness %q is not in the registry", p.Name)
		}
		if !slices.Equal(p.HeadlessArgs, registered.HeadlessArgs) {
			t.Errorf("%s: setup profile headless argv %v differs from registry %v", p.Name, p.HeadlessArgs, registered.HeadlessArgs)
		}
	}
}

// The setup-capable profiles must carry the same invariants the rest of the
// registry holds: home-relative credential paths, a deny list covering their
// token files, and an install script that stays inside ~/.local.
func TestPiProfile(t *testing.T) {
	pi, ok := Lookup("pi")
	if !ok {
		t.Fatal("pi profile missing")
	}
	if !slices.Equal(pi.EnvPassthrough, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}) {
		t.Errorf("pi env passthrough = %v", pi.EnvPassthrough)
	}
	// pi stores OAuth tokens and API keys under ~/.pi/agent/.
	for _, denied := range []string{"auth.json", "oauth.json"} {
		if !slices.Contains(pi.DenyNames, denied) {
			t.Errorf("pi deny names %v missing %q", pi.DenyNames, denied)
		}
	}
	if !strings.Contains(pi.InstallScript, "--ignore-scripts") {
		t.Errorf("pi install script %q must pass --ignore-scripts per the vendor's instruction", pi.InstallScript)
	}

	for _, p := range []Profile{pi} {
		if !strings.Contains(p.InstallScript, "--prefix \"$HOME/.local\"") {
			t.Errorf("%s install script %q must install into ~/.local", p.Name, p.InstallScript)
		}
	}
}

// omp is a fork of pi with its own executable, credential home and
// permission prompt, and it is not offered for environment setup.
func TestOmpProfile(t *testing.T) {
	omp, ok := Lookup("omp")
	if !ok {
		t.Fatal("omp profile missing")
	}
	if !slices.Equal(omp.EnvPassthrough, []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY"}) {
		t.Errorf("omp env passthrough = %v", omp.EnvPassthrough)
	}
	// The credentials live in the agent database, write-ahead log included.
	for _, denied := range []string{"agent.db", "agent.db-wal", "agent.db-shm"} {
		if !slices.Contains(omp.DenyNames, denied) {
			t.Errorf("omp deny names %v missing %q", omp.DenyNames, denied)
		}
	}
	if !strings.Contains(omp.InstallScript, "omp.sh/install") {
		t.Errorf("omp install script %q must be the vendor's own", omp.InstallScript)
	}
	for _, p := range SetupHarnesses() {
		if p.Name == "omp" {
			t.Error("SetupHarnesses() must not offer omp: it runs no local profile scan")
		}
	}
}

// Codex, pi and Cursor read no base-URL variable from the environment.
func TestEnvPassthrough(t *testing.T) {
	for name, want := range map[string][]string{
		"claude":      {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
		"codex":       {"CODEX_API_KEY", "OPENAI_API_KEY"},
		"omp":         {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
		"opencode":    {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "OPENAI_BASE_URL"},
		"copilot":     {"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY"},
		"gemini":      {"GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL"},
		"antigravity": {"GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL"},
	} {
		p, _ := Lookup(name)
		for _, key := range want {
			if !slices.Contains(p.EnvPassthrough, key) {
				t.Errorf("%s env passthrough %v missing %q", name, p.EnvPassthrough, key)
			}
		}
	}
}

// Which harnesses report their own state, and what each is pointed at. The
// launch arguments and the launch environment are both appended to what the
// CLI already carries, so a profile that names a file has to name it inside
// the coordination directory, in one of the two, and ship the bytes that
// land there.
func TestStatusReporters(t *testing.T) {
	want := map[string]Reporter{
		"claude":      ReporterFull,
		"codex":       ReporterFull,
		"omp":         ReporterFull,
		"pi":          ReporterFull,
		"opencode":    ReporterFull,
		"antigravity": ReporterNone,
		"copilot":     ReporterNone,
		"gemini":      ReporterNone,
		"cursor":      ReporterNone,
		"custom":      ReporterNone,
	}
	for name, reporter := range want {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) missing", name)
		}
		if p.Capabilities.Status != reporter {
			t.Errorf("%s reporter = %s, want %s", name, p.Capabilities.Status, reporter)
		}
		pointers := p.StatusLaunchArgs("/run/aether")
		pointers = append(pointers, slices.Collect(maps.Values(p.StatusLaunchEnv("/run/aether")))...)
		if reporter == ReporterNone {
			if len(pointers) != 0 || len(p.StatusFiles) != 0 {
				t.Errorf("%s reports nothing but carries %v and %d files", name, pointers, len(p.StatusFiles))
			}
			continue
		}
		// The app-server reports for its terminal.
		if len(pointers) == 0 && !p.Capabilities.Steer {
			t.Errorf("%s reports but is launched with nothing", name)
		}
		for _, a := range pointers {
			if strings.Contains(a, CoordPlaceholder) {
				t.Errorf("%s status launch %v kept the placeholder", name, pointers)
			}
		}
		for file, body := range p.StatusFiles {
			if len(body) == 0 {
				t.Errorf("%s ships an empty %s", name, file)
			}
			named := slices.ContainsFunc(pointers, func(a string) bool {
				return strings.Contains(a, "/run/aether/"+file)
			})
			if !named {
				t.Errorf("%s ships %s but its launch %v does not name it", name, file, pointers)
			}
		}
	}
}

// Every shipped CLI but Gemini and Cursor carries run instructions in context it keeps
// through compaction, through a per-launch mechanism, never a member-home or
// repository instruction file.
func TestInstructionMechanisms(t *testing.T) {
	const text = "line one\nsay \"<hi>\""
	for _, name := range []string{"claude", "codex", "pi", "omp", "opencode", "copilot"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) missing", name)
		}
		if !p.CarriesInstructions() {
			t.Fatalf("%s has no system prompt mechanism", name)
		}
		args := p.InstructionLaunchArgs("/run/aether", text)
		env := p.InstructionLaunchEnv("/run/aether")
		for _, arg := range args {
			if strings.Contains(arg, "{") {
				t.Errorf("%s instruction args kept a placeholder: %v", name, args)
			}
		}
		if (len(env) > 0) != (p.InstructionsFile != "") {
			t.Errorf("%s loads instructions from a file %v, but writes InstructionsFile %q", name, len(env) > 0, p.InstructionsFile)
		}
		switch name {
		case "codex":
			var config struct {
				DeveloperInstructions string `toml:"developer_instructions"`
			}
			if len(args) != 2 || toml.Unmarshal([]byte(args[1]), &config) != nil || config.DeveloperInstructions != text {
				t.Errorf("codex instruction args %q do not decode to the text", args)
			}
		case "opencode":
			if !strings.Contains(env["OPENCODE_CONFIG_CONTENT"], `"/run/aether/`+p.InstructionsFile+`"`) {
				t.Errorf("opencode instruction env %v does not load %s", env, p.InstructionsFile)
			}
		case "copilot":
			if env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"] != "/run/aether" || !strings.HasSuffix(p.InstructionsFile, ".instructions.md") {
				t.Errorf("copilot instruction env %v does not load %s", env, p.InstructionsFile)
			}
		default:
			if !slices.Equal(args, []string{"--append-system-prompt", text}) {
				t.Errorf("%s instruction args = %q", name, args)
			}
		}
	}
	for _, name := range []string{"gemini", "cursor", "custom"} {
		if p, _ := Lookup(name); p.CarriesInstructions() {
			t.Fatalf("%s profile must not carry instruction wiring: %+v", name, p)
		}
	}
}

// A managed Claude launch's settings register the inbox hooks, SessionStart
// among them with no matcher, so it fires on startup, resume, compact and
// clear. Stop runs the status reporter and the idle-wake waiter only: Claude
// Code runs Stop hooks in parallel, so an inbox block for unread mail would
// continue a turn the reporter had already ended.
func TestClaudeSettingsRegisterInboxHooks(t *testing.T) {
	p, _ := Lookup("claude")
	native, err := p.PrepareNativeLaunch("/run/aether", []string{"claude", "a task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(native.Files[agentstatus.ClaudeSettingsName], &settings); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PostToolBatch", "SubagentStart"} {
		found := false
		for _, entry := range settings.Hooks[event] {
			for _, h := range entry.Hooks {
				if h.Command == "/usr/local/bin/aether-internal hook claude "+event {
					found = true
					if entry.Matcher != "" {
						t.Errorf("%s matcher = %q; SessionStart must fire on startup, resume, compact and clear", event, entry.Matcher)
					}
				}
			}
		}
		if !found {
			t.Errorf("claude settings lack the %s inbox hook", event)
		}
	}
	var stop []string
	for _, entry := range settings.Hooks["Stop"] {
		for _, h := range entry.Hooks {
			stop = append(stop, h.Command)
		}
	}
	if want := []string{"/opt/aether/aether-server report claude", "/usr/local/bin/aether-internal hook claude wake"}; !slices.Equal(stop, want) {
		t.Errorf("Stop hooks = %v, want %v", stop, want)
	}
}

func TestArgv(t *testing.T) {
	template := []string{"claude", "-p", TaskPlaceholder, "prefix-{task}-suffix"}
	got := Argv(template, "fix the bug", nil)
	want := []string{"claude", "-p", "fix the bug", "prefix-fix the bug-suffix"}
	if !slices.Equal(got, want) {
		t.Fatalf("Argv = %v, want %v", got, want)
	}
	if template[2] != TaskPlaceholder {
		t.Fatal("Argv mutated the template")
	}
}

// A taskless launch (empty task) drops every placeholder-bearing token so the
// agent starts its bare interactive TUI: claude's trailing positional simply
// disappears, and opencode's "--prompt={task}" leaves whole rather than
// dangling with an empty value the CLI would reject.
func TestArgvTaskless(t *testing.T) {
	claude, _ := Lookup("claude")
	if got, want := Argv(claude.TUIArgs, "", nil), []string{"claude", "--dangerously-skip-permissions"}; !slices.Equal(got, want) {
		t.Fatalf("claude taskless argv = %v, want %v", got, want)
	}
	opencode, _ := Lookup("opencode")
	if got, want := Argv(opencode.TUIArgs, "", nil), []string{"opencode"}; !slices.Equal(got, want) {
		t.Fatalf("opencode taskless argv = %v, want %v", got, want)
	}
	// A non-empty task still couples opencode's prompt into one token.
	if got, want := Argv(opencode.TUIArgs, "do it", nil), []string{"opencode", "--prompt=do it"}; !slices.Equal(got, want) {
		t.Fatalf("opencode argv = %v, want %v", got, want)
	}
}

func TestArgvModel(t *testing.T) {
	claude, _ := Lookup("claude")
	opencode, _ := Lookup("opencode")
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"before the task", Argv(claude.TUIArgs, "do it", claude.LaunchArgs("opus", "")),
			[]string{"claude", "--dangerously-skip-permissions", "--model", "opus", "do it"}},
		{"before a prompt flag", Argv(opencode.TUIArgs, "do it", opencode.LaunchArgs("anthropic/claude-sonnet-4-5", "")),
			[]string{"opencode", "--model", "anthropic/claude-sonnet-4-5", "--prompt=do it"}},
		{"appended without a task", Argv(claude.TUIArgs, "", claude.LaunchArgs("opus", "")),
			[]string{"claude", "--dangerously-skip-permissions", "--model", "opus"}},
		{"appended to resume", claude.ResumeCommand("s1", "opus", ""),
			[]string{"claude", "--dangerously-skip-permissions", "--resume", "s1", "--model", "opus"}},
	}
	for _, tt := range tests {
		if !slices.Equal(tt.got, tt.want) {
			t.Errorf("%s: argv = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestArgvEffort(t *testing.T) {
	claude, _ := Lookup("claude")
	codex, _ := Lookup("codex")
	copilot, _ := Lookup("copilot")
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"claude after the model, before the task", Argv(claude.TUIArgs, "do it", claude.LaunchArgs("opus", "high")),
			[]string{"claude", "--dangerously-skip-permissions", "--model", "opus", "--effort", "high", "do it"}},
		{"claude appended without a task", Argv(claude.TUIArgs, "", claude.LaunchArgs("", "max")),
			[]string{"claude", "--dangerously-skip-permissions", "--effort", "max"}},
		{"claude appended to resume", claude.ResumeCommand("s1", "", "low"),
			[]string{"claude", "--dangerously-skip-permissions", "--resume", "s1", "--effort", "low"}},
		{"claude before a headless resume's prompt", claude.HeadlessResumeCommand("s1", "go on", "", "low"),
			[]string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--resume", "s1", "--effort", "low", "go on"}},
		{"codex before the task", Argv(codex.HeadlessArgs, "do it", codex.LaunchArgs("", "xhigh")),
			[]string{"codex", "exec", "--json", "--dangerously-bypass-approvals-and-sandbox", "-c", "model_reasoning_effort=xhigh", "do it"}},
		{"codex appended without a task", Argv(codex.TUIArgs, "", codex.LaunchArgs("", "low")),
			[]string{"codex", "--dangerously-bypass-approvals-and-sandbox", "-c", "model_reasoning_effort=low"}},
		{"codex appended to resume", codex.ResumeCommand("s1", "gpt-5", "high"),
			[]string{"codex", "resume", "s1", "--model", "gpt-5", "-c", "model_reasoning_effort=high"}},
		{"copilot before a prompt flag", Argv(copilot.HeadlessArgs, "do it", copilot.LaunchArgs("", "high")),
			[]string{"copilot", "--allow-all", "--reasoning-effort", "high", "--prompt=do it"}},
		{"copilot appended without a task", Argv(copilot.TUIArgs, "", copilot.LaunchArgs("", "none")),
			[]string{"copilot", "--allow-all", "--reasoning-effort", "none"}},
	}
	for _, tt := range tests {
		if !slices.Equal(tt.got, tt.want) {
			t.Errorf("%s: argv = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestCheckEffort(t *testing.T) {
	claude, _ := Lookup("claude")
	opencode, _ := Lookup("opencode")
	tests := []struct {
		name    string
		profile Profile
		effort  string
		acp     bool
		want    string
	}{
		{"no effort", opencode, "", false, ""},
		{"a listed level", claude, "xhigh", false, ""},
		{"an unlisted level", claude, "ultra", true, `takes effort low, medium, high, xhigh, max, not "ultra"`},
		{"no flag outside Enhanced", opencode, "high", false, "has no effort flag; launch it without an effort"},
		{"the session option in Enhanced", opencode, "high", true, ""},
	}
	for _, tt := range tests {
		err := tt.profile.CheckEffort(tt.effort, tt.acp)
		if got := fmt.Sprint(err); (tt.want == "" && err != nil) || (tt.want != "" && got != tt.want) {
			t.Errorf("%s: CheckEffort = %v, want %q", tt.name, err, tt.want)
		}
	}
	for _, p := range Profiles() {
		count := 0
		for _, a := range p.EffortArgs {
			count += strings.Count(a, EffortPlaceholder)
		}
		if len(p.EffortArgs) > 0 && (count != 1 || len(p.EffortLevels) == 0) || p.ACPEffortArgs && len(p.EffortArgs) == 0 {
			t.Errorf("%s effort args %v carry %d placeholders with levels %v", p.Name, p.EffortArgs, count, p.EffortLevels)
		}
	}
}

func TestEveryShippedAgentHasAModelFlag(t *testing.T) {
	for _, p := range Profiles() {
		count := 0
		for _, a := range p.ModelArgs {
			count += strings.Count(a, ModelPlaceholder)
		}
		want := 1
		if p.Name == "custom" {
			want = 0
		}
		if count != want {
			t.Errorf("%s ModelArgs %v carry %d model placeholders, want %d", p.Name, p.ModelArgs, count, want)
		}
	}
}

func TestHomeDir(t *testing.T) {
	tests := []struct{ user, want string }{
		{"", "/root"},
		{"0:0", "/root"},
		{"1000:1000", "/home/aether"},
		{"1000:0", "/home/aether"},
	}
	for _, tt := range tests {
		if got := HomeDir(tt.user); got != tt.want {
			t.Errorf("HomeDir(%q) = %q, want %q", tt.user, got, tt.want)
		}
	}
}
func TestHomeRelative(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"root home path", "/root/.claude", ".claude"},
		{"aether home path", "/home/aether/.config/omp", ".config/omp"},
		{"root itself", "/root", "."},
		{"aether home itself", "/home/aether", "."},
		{"relative path is cleaned", "foo/../bar", "bar"},
		{"outside home stays absolute", "/var/lib/omp", "/var/lib/omp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HomeRelative(tt.path); got != tt.want {
				t.Errorf("HomeRelative(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveUser(t *testing.T) {
	tests := []struct {
		name      string
		override  string
		imageUser string
		want      string
		wantErr   bool
	}{
		{"empty image user is root", "", "", "0:0", false},
		{"numeric uid implies same gid", "", "1000", "1000:1000", false},
		{"numeric uid:gid accepted", "", "1000:100", "1000:100", false},
		{"named user rejected", "", "node", "", true},
		{"named group rejected", "", "1000:staff", "", true},
		{"empty uid rejected", "", ":100", "", true},
		{"profile override wins", "1234:1234", "node", "1234:1234", false},
		{"profile override normalized", "1234", "node", "1234:1234", false},
		{"named profile override rejected", "node:node", "", "", true},
		{"chown sentinel uid rejected", "", "4294967295", "", true},
		{"chown sentinel gid rejected", "", "1000:4294967295", "", true},
		{"uid past 32 bits rejected", "", "4294967296", "", true},
		{"max valid uid accepted", "", "4294967294", "4294967294:4294967294", false},
		{"sentinel profile override rejected", "4294967295:0", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveUser(tt.override, tt.imageUser)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveUser(%q, %q) = %q, want error", tt.override, tt.imageUser, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveUser(%q, %q): %v", tt.override, tt.imageUser, err)
			}
			if got != tt.want {
				t.Fatalf("ResolveUser(%q, %q) = %q, want %q", tt.override, tt.imageUser, got, tt.want)
			}
		})
	}
}

func TestDefinitionValidation(t *testing.T) {
	valid := Definition{
		Name:            "omp",
		TUIArgs:         []string{"omp", "{task}"},
		HeadlessArgs:    []string{"omp", "-p", "{task}"},
		Executable:      "omp",
		ProfileRoot:     "/home/aether/.omp",
		CredentialPaths: []string{"/home/aether/.omp"},
		DenyNames:       []string{"auth.json"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
	for name, def := range map[string]Definition{
		"host executable":            validWith(valid, func(d *Definition) { d.Executable = "/usr/bin/omp" }),
		"relative profile":           validWith(valid, func(d *Definition) { d.ProfileRoot = ".omp" }),
		"credential escapes profile": validWith(valid, func(d *Definition) { d.CredentialPaths = []string{"/home/aether/.other"} }),
		"denied path":                validWith(valid, func(d *Definition) { d.DenyNames = []string{"nested/auth.json"} }),
		"argv mismatch":              validWith(valid, func(d *Definition) { d.HeadlessArgs = []string{"other", "{task}"} }),
		// The name becomes a host path segment under <homes>/<member>;
		// a traversal name would address another member's credentials.
		"traversal name":            validWith(valid, func(d *Definition) { d.Name = "../victim/claude" }),
		"path name":                 validWith(valid, func(d *Definition) { d.Name = "a/b" }),
		"spaced name":               validWith(valid, func(d *Definition) { d.Name = "a b" }),
		"acp mode without acp args": validWith(valid, func(d *Definition) { d.ACPMode = "yolo" }),
		"spaced acp mode": validWith(valid, func(d *Definition) {
			d.ACPArgs, d.ACPMode = []string{"omp", "acp"}, "a b"
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := def.Validate(); err == nil {
				t.Fatal("definition accepted")
			}
		})
	}
}

func TestDefinitionACPModeBeatsTheRegisteredProfile(t *testing.T) {
	def := Definition{Name: "claude", ACPArgs: []string{"claude-agent-acp"}, ACPMode: "plan"}
	if got := def.Profile().ACPMode; got != "plan" {
		t.Errorf("ACPMode = %q, want the definition's plan", got)
	}
	def.ACPMode = ""
	if got := def.Profile().ACPMode; got != "bypassPermissions" {
		t.Errorf("ACPMode = %q, want the registered bypassPermissions", got)
	}
}

// Member-supplied definitions use the same path policy as administrator
// definitions, including paths under ~/.local.
func TestValidateMemberDefinition(t *testing.T) {
	valid := Definition{
		Name:            "omp",
		TUIArgs:         []string{"omp", "{task}"},
		HeadlessArgs:    []string{"omp", "-p", "{task}"},
		Executable:      "omp",
		ProfileRoot:     "/home/aether/.local",
		CredentialPaths: []string{"/home/aether/.local/bin", "/home/aether/.local/share/omp"},
	}
	if err := ValidateMemberDefinition(valid); err != nil {
		t.Fatalf("valid member definition rejected: %v", err)
	}
	rootDefinition := validWith(valid, func(d *Definition) {
		d.ProfileRoot = "/root/.local"
		d.CredentialPaths = []string{"/root/.local/bin", "/root/.local/share/omp"}
	})
	if err := ValidateMemberDefinition(rootDefinition); err != nil {
		t.Fatalf("valid root member definition rejected: %v", err)
	}
}

func validWith(base Definition, mutate func(*Definition)) Definition {
	mutate(&base)
	return base
}

// An admin override that renames a shipped harness's executable keeps the
// registry's environment contract: the key passthrough and the variables
// the CLI needs to start at all. Losing them turns a working override into
// an agent that cannot authenticate or cannot launch, with nothing in the
// argv to explain it.
func TestDefinitionProfileKeepsRegistryEnvironment(t *testing.T) {
	claude, ok := Lookup("claude")
	if !ok {
		t.Fatal("claude is not in the registry")
	}
	if len(claude.EnvPassthrough) == 0 || len(claude.Env) == 0 {
		t.Fatalf("claude declares no environment to carry: %+v", claude)
	}
	override := Definition{
		Name:         "claude",
		TUIArgs:      []string{"claude-wrapper", TaskPlaceholder},
		HeadlessArgs: []string{"claude-wrapper", "-p", TaskPlaceholder},
		Executable:   "claude-wrapper",
	}.Profile()
	if !slices.Equal(override.EnvPassthrough, claude.EnvPassthrough) {
		t.Errorf("override passthrough = %v, want %v", override.EnvPassthrough, claude.EnvPassthrough)
	}
	if !maps.Equal(override.Env, claude.Env) {
		t.Errorf("override env = %v, want %v", override.Env, claude.Env)
	}
	if override.ACPMode != claude.ACPMode {
		t.Errorf("override enhanced mode = %q, want %q", override.ACPMode, claude.ACPMode)
	}
	if !slices.Equal(override.ModelArgs, claude.ModelArgs) {
		t.Errorf("override model args = %v, want %v", override.ModelArgs, claude.ModelArgs)
	}
	if !slices.Equal(override.EffortArgs, claude.EffortArgs) || !slices.Equal(override.EffortLevels, claude.EffortLevels) {
		t.Errorf("override effort = %v %v, want %v %v", override.EffortArgs, override.EffortLevels, claude.EffortArgs, claude.EffortLevels)
	}
	// The copy must not alias the registry: a caller editing an override's
	// environment would otherwise change every later launch.
	override.Env["IS_SANDBOX"] = "0"
	if again, _ := Lookup("claude"); again.Env["IS_SANDBOX"] != "1" {
		t.Fatalf("registry claude env mutated through an override: %v", again.Env)
	}
	// An unshipped name has no registry entry to inherit from.
	if unknown := (Definition{Name: "aider", Executable: "aider"}).Profile(); len(unknown.Env) != 0 || len(unknown.EnvPassthrough) != 0 || len(unknown.ModelArgs) != 0 || len(unknown.EffortLevels) != 0 {
		t.Fatalf("unshipped definition inherited environment: %+v", unknown)
	}
	// The scheduler would run the shipped CLI's updater against a program
	// the definition may not even launch.
	if override.UpdateScript != "" {
		t.Fatalf("override inherited the registry update script %q", override.UpdateScript)
	}
}

// The scheduler updates a harness it can find in ~/.local/bin by its TUI
// executable, so an update script without those is never run, and opencode
// and custom must never be updated.
func TestUpdateScripts(t *testing.T) {
	var updated []string
	for _, p := range Profiles() {
		if p.UpdateScript == "" {
			continue
		}
		updated = append(updated, p.Name)
		if p.InstallScript == "" || len(p.TUIArgs) == 0 {
			t.Errorf("%s has an update script but no install script or TUI argv", p.Name)
		}
	}
	if want := []string{"antigravity", "claude", "codex", "copilot", "cursor", "gemini", "omp", "pi"}; !slices.Equal(updated, want) {
		t.Fatalf("profiles with an update script = %v, want %v", updated, want)
	}
	if pi, _ := Lookup("pi"); strings.Contains(pi.UpdateScript, "update --self") {
		t.Errorf("pi update script %q must not use pi's own updater, which replaces files in place", pi.UpdateScript)
	}
}

// The dashboard cannot import this registry, so its glyph map repeats the
// shipped names; nothing else catches a missing one.
func TestDashboardListsEveryShippedHarness(t *testing.T) {
	for _, file := range []string{
		"../../web/src/components/ui/agent-glyph.tsx",
	} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, p := range Profiles() {
			name := regexp.QuoteMeta(p.Name)
			listed := regexp.MustCompile(`'` + name + `'|(?m)^\s*` + name + `\s*:`)
			if !listed.MatchString(string(source)) {
				t.Errorf("%s does not list the shipped harness %q", file, p.Name)
			}
		}
	}
}

// LoginPaths is the one place a login path is checked: whatever spelling a
// definition uses, only a clean path strictly below the home is shareable.
func TestLoginPaths(t *testing.T) {
	got, err := Profile{Name: "aider", CredentialPaths: []string{"/root/.aider/auth.json", "/home/aether/.config/aider", ".aider.key"}}.LoginPaths()
	if err != nil {
		t.Fatalf("LoginPaths: %v", err)
	}
	if want := []string{".aider/auth.json", ".config/aider", ".aider.key"}; !slices.Equal(got, want) {
		t.Fatalf("LoginPaths = %v, want %v", got, want)
	}
	for _, bad := range []string{"/root", "/home/aether", "/root/", ".", "..", "../other/.ssh", "/root/../etc", "/etc/passwd", "a/../b", "./a", `a\b`, "a\x00b"} {
		_, err := Profile{Name: "aider", CredentialPaths: []string{".aider/auth.json", bad}}.LoginPaths()
		if err == nil || !strings.Contains(err.Error(), "aider") || !strings.Contains(err.Error(), fmt.Sprintf("%q", bad)) {
			t.Errorf("LoginPaths(%q) = %v, want an error naming the harness and path", bad, err)
		}
	}
}
