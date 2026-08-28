package harness

import (
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRegistryShipsSixProfiles(t *testing.T) {
	want := []string{"amp", "claude", "codex", "custom", "opencode", "pi"}
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
		"opencode": "", // opencode has no permission prompt flag to bypass
		"pi":       "", // pi has no permission prompt flag to bypass
		"amp":      "--dangerously-allow-all",
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
		"opencode": ".local/share/opencode",
		"pi":       ".pi",
		"amp":      ".config/amp",
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
// codex, pi, amp, in that order. Later tasks and the dashboard treat this
// list as the authority on which harnesses may drive environment setup;
// opencode, custom, and fake stay launchable for runs but are never offered
// here.
func TestSetupHarnesses(t *testing.T) {
	want := []string{"claude", "codex", "pi", "amp"}
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

// The new setup-capable profiles must carry the same invariants the rest of
// the registry holds: home-relative credential paths, a deny list covering
// their token files, and an install script that stays inside ~/.local.
func TestPiAndAmpProfiles(t *testing.T) {
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
	if pi.ResumeFlag != "--continue" {
		t.Errorf("pi resume flag = %q, want --continue", pi.ResumeFlag)
	}
	if !strings.Contains(pi.InstallScript, "--ignore-scripts") {
		t.Errorf("pi install script %q must pass --ignore-scripts per the vendor's instruction", pi.InstallScript)
	}

	amp, ok := Lookup("amp")
	if !ok {
		t.Fatal("amp profile missing")
	}
	if !slices.Equal(amp.EnvPassthrough, []string{"AMP_API_KEY"}) {
		t.Errorf("amp env passthrough = %v", amp.EnvPassthrough)
	}
	// amp keeps settings under ~/.config/amp and XDG data (secrets.json,
	// state.json) under ~/.local/share/amp; both must persist per member.
	if !slices.Equal(amp.CredentialPaths, []string{".config/amp", ".local/share/amp"}) {
		t.Errorf("amp credential paths = %v", amp.CredentialPaths)
	}
	for _, denied := range []string{"secrets.json", "state.json"} {
		if !slices.Contains(amp.DenyNames, denied) {
			t.Errorf("amp deny names %v missing %q", amp.DenyNames, denied)
		}
	}
	for _, p := range []Profile{pi, amp} {
		if !strings.Contains(p.InstallScript, "--prefix \"$HOME/.local\"") {
			t.Errorf("%s install script %q must install into ~/.local", p.Name, p.InstallScript)
		}
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

func TestCredentialMounts(t *testing.T) {
	p, _ := Lookup("claude")
	home := filepath.Join("data", "homes", "mem_1", "claude")
	mounts := p.CredentialMounts(home, "/root")
	if len(mounts) != 1 {
		t.Fatalf("mounts = %v", mounts)
	}
	if mounts[0].ContainerPath != "/root/.claude" {
		t.Errorf("container path = %q", mounts[0].ContainerPath)
	}
	if want := filepath.Join(home, ".claude"); mounts[0].HostPath != want {
		t.Errorf("host path = %q, want %q", mounts[0].HostPath, want)
	}
	if mounts[0].ReadOnly {
		t.Error("credential mounts must be read-write")
	}
	// A non-root run resolves the same host home to a different container
	// home: login state persists across image-user changes.
	nonRoot := p.CredentialMounts(home, "/home/aether")
	if nonRoot[0].ContainerPath != "/home/aether/.claude" {
		t.Errorf("non-root container path = %q", nonRoot[0].ContainerPath)
	}
	if nonRoot[0].HostPath != mounts[0].HostPath {
		t.Errorf("host home changed with container home: %q vs %q", nonRoot[0].HostPath, mounts[0].HostPath)
	}
	if got := p.CredentialMounts("", "/root"); got != nil {
		t.Errorf("empty home mounts = %v, want nil", got)
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

// TestResumeArgvAddsTheHarnessResumeFlag pins the failure table's
// server-reboot promise that a relaunch resumes the agent's own session
// "where the adapter supports them": the flag rides directly behind the
// executable, the rest of the argv is untouched, and a harness that has no
// resume flag relaunches from scratch rather than growing a made-up one.
func TestResumeArgvAddsTheHarnessResumeFlag(t *testing.T) {
	claude, ok := Lookup("claude")
	if !ok {
		t.Fatal("claude is not in the registry")
	}
	if claude.ResumeFlag == "" {
		t.Fatal("claude has no resume flag; Claude Code resumes with --continue")
	}
	argv := ResumeArgv(Argv(claude.TUIArgs, "keep going"), claude.ResumeFlag)
	want := []string{"claude", "--continue", "--dangerously-skip-permissions", "keep going"}
	if !slices.Equal(argv, want) {
		t.Fatalf("resume argv = %v, want %v", argv, want)
	}

	plain := Argv(claude.TUIArgs, "keep going")
	if got := ResumeArgv(plain, ""); !slices.Equal(got, plain) {
		t.Fatalf("resume argv with no flag = %v, want the argv unchanged %v", got, plain)
	}
	if got := ResumeArgv(nil, "--continue"); got != nil {
		t.Fatalf("resume argv of an empty template = %v, want nil", got)
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

// Member-supplied definitions additionally reject paths under the tool
// mount: they would shadow or pierce the read-only snapshot at ~/.local.
// Administrator definitions and registry profiles (opencode) keep the
// capability.
func TestValidateMemberDefinition(t *testing.T) {
	valid := Definition{
		Name:            "omp",
		TUIArgs:         []string{"omp", "{task}"},
		HeadlessArgs:    []string{"omp", "-p", "{task}"},
		Executable:      "omp",
		ProfileRoot:     "/home/aether/.omp",
		CredentialPaths: []string{"/home/aether/.omp"},
	}
	if err := ValidateMemberDefinition(valid); err != nil {
		t.Fatalf("valid member definition rejected: %v", err)
	}
	for name, def := range map[string]Definition{
		"credential under tool mount": validWith(valid, func(d *Definition) {
			d.ProfileRoot = ""
			d.CredentialPaths = []string{"/home/aether/.local/bin"}
		}),
		"profile under tool mount": validWith(valid, func(d *Definition) {
			d.ProfileRoot = "/root/.local/share/omp"
			d.CredentialPaths = []string{"/root/.local/share/omp"}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateMemberDefinition(def); err == nil {
				t.Fatal("member definition accepted")
			}
		})
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
