package harness

import (
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRegistryShipsFiveProfiles(t *testing.T) {
	want := []string{"aider", "claude", "codex", "custom", "opencode"}
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
		"aider":    "--yes-always",
		"opencode": "", // opencode has no permission prompt flag to bypass
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
			if !slices.Contains(argv, TaskPlaceholder) {
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
		"aider":    ".aider",
		"opencode": ".local/share/opencode",
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
	} {
		t.Run(name, func(t *testing.T) {
			if err := def.Validate(); err == nil {
				t.Fatal("definition accepted")
			}
		})
	}
}

func validWith(base Definition, mutate func(*Definition)) Definition {
	mutate(&base)
	return base
}
