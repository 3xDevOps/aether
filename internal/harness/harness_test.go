package harness

import (
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestRegistryShipsSixProfiles(t *testing.T) {
	want := []string{"claude", "codex", "custom", "omp", "opencode", "pi"}
	var got []string
	for _, p := range Profiles() {
		got = append(got, p.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Profiles() = %v, want %v", got, want)
	}
}

// Every harness with a command must default to its auto/full-permission
// flags in both modes and carry the task placeholder; login-flow harnesses
// must declare their credential paths.
func TestProfileDefaults(t *testing.T) {
	autoFlags := map[string]string{
		"claude":   "--dangerously-skip-permissions",
		"codex":    "--dangerously-bypass-approvals-and-sandbox",
		"omp":      "--auto-approve",
		"opencode": "", // opencode has no permission prompt flag to bypass
		"pi":       "", // pi has no permission prompt flag to bypass
	}
	for name, flag := range autoFlags {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) missing", name)
		}
		for mode, argv := range map[string][]string{"tui": p.TUIArgs, "headless": p.HeadlessArgs} {
			if len(argv) == 0 {
				t.Errorf("%s %s: no argv", name, mode)
				continue
			}
			if argv[0] != name {
				t.Errorf("%s %s argv[0] = %q", name, mode, argv[0])
			}
			if flag != "" && !slices.Contains(argv, flag) {
				t.Errorf("%s %s argv %v missing auto flag %q", name, mode, argv, flag)
			}
			if !slices.ContainsFunc(argv, func(a string) bool {
				return strings.Contains(a, TaskPlaceholder)
			}) {
				t.Errorf("%s %s argv %v missing task placeholder", name, mode, argv)
			}
		}
		if len(p.CredentialPaths) == 0 {
			t.Errorf("%s: no credential paths", name)
		}
		for _, cp := range p.CredentialPaths {
			if strings.HasPrefix(cp, "/") || strings.HasPrefix(cp, "..") {
				t.Errorf("%s credential path %q must be home-relative", name, cp)
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

func TestLocalRootAndDenyNames(t *testing.T) {
	wantRoot := map[string]string{
		"claude":   ".claude",
		"codex":    ".codex",
		"omp":      ".omp",
		"opencode": ".local/share/opencode",
		"pi":       ".pi",
		"custom":   "",
	}
	for name, root := range wantRoot {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) missing", name)
		}
		if p.LocalRoot != root {
			t.Errorf("%s LocalRoot = %q, want %q", name, p.LocalRoot, root)
		}
		if name == "custom" {
			continue
		}
		if len(p.DenyNames) == 0 {
			t.Errorf("%s: no DenyNames", name)
		}
		if p.ContainerLocalRoot("") != path.Join("/root", root) {
			t.Errorf("%s ContainerLocalRoot(root) = %q", name, p.ContainerLocalRoot(""))
		}
	}
}

// TestSetupHarnesses pins the environment-setup subset: exactly claude,
// codex, pi, in that order. Later tasks and the dashboard treat this
// list as the authority on which harnesses may drive environment setup;
// opencode, custom, and fake stay launchable for runs but are never offered
// here.
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
	// Each entry is the full registry profile, not a name-only stub.
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
	if !slices.Equal(omp.EnvPassthrough, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}) {
		t.Errorf("omp env passthrough = %v", omp.EnvPassthrough)
	}
	// The credentials live in the agent database, write-ahead log included.
	for _, denied := range []string{"agent.db", "agent.db-wal", "agent.db-shm"} {
		if !slices.Contains(omp.DenyNames, denied) {
			t.Errorf("omp deny names %v missing %q", omp.DenyNames, denied)
		}
	}
	if omp.ResumeFlag != "--continue" {
		t.Errorf("omp resume flag = %q, want --continue", omp.ResumeFlag)
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

// Which harnesses report their own state, and what each is pointed at. The
// launch arguments and the launch environment are both appended to what the
// CLI already carries, so a profile that names a file has to name it inside
// the coordination directory, in one of the two, and ship the bytes that
// land there.
func TestStatusReporters(t *testing.T) {
	want := map[string]Reporter{
		"claude":   ReporterFull,
		"codex":    ReporterTurnEnd,
		"omp":      ReporterFull,
		"pi":       ReporterFull,
		"opencode": ReporterFull,
		"custom":   ReporterNone,
	}
	for name, reporter := range want {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) missing", name)
		}
		if p.Reporter != reporter {
			t.Errorf("%s reporter = %s, want %s", name, p.Reporter, reporter)
		}
		// Whatever the CLI's own mechanism is, this is everything the
		// launch says about the reporter.
		pointers := p.StatusLaunchArgs("/run/aether")
		pointers = append(pointers, slices.Collect(maps.Values(p.StatusLaunchEnv("/run/aether")))...)
		if reporter == ReporterNone {
			if len(pointers) != 0 || len(p.StatusFiles) != 0 {
				t.Errorf("%s reports nothing but carries %v and %d files", name, pointers, len(p.StatusFiles))
			}
			continue
		}
		if len(pointers) == 0 {
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
	// Codex needs no file: the override carries the whole command.
	codex, _ := Lookup("codex")
	if len(codex.StatusFiles) != 0 {
		t.Errorf("codex ships %d status files, want none", len(codex.StatusFiles))
	}
}

func TestArgv(t *testing.T) {
	template := []string{"claude", "-p", TaskPlaceholder, "prefix-{task}-suffix"}
	got := Argv(template, "fix the bug")
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
	if got, want := Argv(claude.TUIArgs, ""), []string{"claude", "--dangerously-skip-permissions"}; !slices.Equal(got, want) {
		t.Fatalf("claude taskless argv = %v, want %v", got, want)
	}
	opencode, _ := Lookup("opencode")
	if got, want := Argv(opencode.TUIArgs, ""), []string{"opencode"}; !slices.Equal(got, want) {
		t.Fatalf("opencode taskless argv = %v, want %v", got, want)
	}
	// A non-empty task still couples opencode's prompt into one token.
	if got, want := Argv(opencode.TUIArgs, "do it"), []string{"opencode", "--prompt=do it"}; !slices.Equal(got, want) {
		t.Fatalf("opencode argv = %v, want %v", got, want)
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
		"traversal name": validWith(valid, func(d *Definition) { d.Name = "../victim/claude" }),
		"path name":      validWith(valid, func(d *Definition) { d.Name = "a/b" }),
		"spaced name":    validWith(valid, func(d *Definition) { d.Name = "a b" }),
	} {
		t.Run(name, func(t *testing.T) {
			if err := def.Validate(); err == nil {
				t.Fatal("definition accepted")
			}
		})
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
	// The opencode registry profile keeps its under-.local credentials.
	opencode, _ := Lookup("opencode")
	if opencode.LocalRoot != ".local/share/opencode" {
		t.Fatalf("opencode profile root moved: %q", opencode.LocalRoot)
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
	// The copy must not alias the registry: a caller editing an override's
	// environment would otherwise change every later launch.
	override.Env["IS_SANDBOX"] = "0"
	if again, _ := Lookup("claude"); again.Env["IS_SANDBOX"] != "1" {
		t.Fatalf("registry claude env mutated through an override: %v", again.Env)
	}
	// An unshipped name has no registry entry to inherit from.
	if unknown := (Definition{Name: "aider", Executable: "aider"}).Profile(); len(unknown.Env) != 0 || len(unknown.EnvPassthrough) != 0 {
		t.Fatalf("unshipped definition inherited environment: %+v", unknown)
	}
}

// The dashboard cannot import this registry, so it repeats the shipped
// names in two places: the picker a template's harness is chosen from, and
// the glyph map that decides what a run card shows. A name missing from
// either is a harness members cannot schedule, or one that renders as an
// anonymous bot - and nothing else catches it, because both lists are valid
// TypeScript whatever they hold. The name has to be a quoted string or an
// object key, so a harness that is only mentioned in a comment or a class
// name still counts as missing.
func TestDashboardListsEveryShippedHarness(t *testing.T) {
	for _, file := range []string{
		"../../web/src/routes/templates/index.tsx",
		"../../web/src/routes/board/harness-glyph.tsx",
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
