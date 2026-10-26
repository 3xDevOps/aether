package coordcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hookInstallFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHookInstallationEnvironmentRoots(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	override := filepath.Join(t.TempDir(), "custom home")
	values := map[string]string{
		"CLAUDE_CONFIG_DIR": override, "CODEX_HOME": override,
		"COPILOT_HOME": override, "GEMINI_CLI_HOME": override,
		"PI_CODING_AGENT_DIR": override, "OMP_PROFILE": "work", "PI_PROFILE": "ignored",
	}
	plans := hookInstallationPlans(hookInstallInputs{home: home, cwd: project, env: func(key string) string { return values[key] }})
	want := map[string]string{
		"claude":  filepath.Join(override, "settings.json"),
		"codex":   filepath.Join(override, "hooks.json"),
		"copilot": filepath.Join(override, "hooks", "aether.json"),
		"gemini":  filepath.Join(override, ".gemini", "settings.json"),
		"pi":      filepath.Join(override, "extensions", "aether.ts"),
		"omp":     filepath.Join(home, ".omp", "profiles", "work", "agent", "extensions", "aether.ts"),
	}
	for _, plan := range plans {
		if destination, ok := want[plan.id]; ok && plan.paths[0] != destination {
			t.Errorf("%s destination = %q, want %q", plan.id, plan.paths[0], destination)
		}
	}
}

func TestHookInstallationRejectsMisleadingRegistrations(t *testing.T) {
	command := "/usr/local/bin/aether-internal hook claude Stop"
	want := []hookInstallEntry{{Hooks: []hookInstallEntry{{Type: "command", Command: command}}}}
	for _, tc := range []struct {
		name  string
		entry hookInstallEntry
	}{
		{"mention only", hookInstallEntry{Hooks: []hookInstallEntry{{Type: "command", Command: "echo " + command}}}},
		{"wrong event argument", hookInstallEntry{Hooks: []hookInstallEntry{{Type: "command", Command: strings.Replace(command, "Stop", "SessionStart", 1)}}}},
		{"restricted group", hookInstallEntry{Matcher: "Bash", Hooks: []hookInstallEntry{{Type: "command", Command: command}}}},
		{"asynchronous child", hookInstallEntry{Hooks: []hookInstallEntry{{Type: "command", Command: command, Async: true}}}},
		{"disabled group", hookInstallEntry{Disabled: true, Hooks: []hookInstallEntry{{Type: "command", Command: command}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if hookEventInstalled([]hookInstallEntry{tc.entry}, want) {
				t.Fatal("non-equivalent registration reported configured")
			}
		})
	}
}

func TestHookInstallationPartialAcrossScopes(t *testing.T) {
	dir := t.TempDir()
	user, project := filepath.Join(dir, "user.json"), filepath.Join(dir, "project.json")
	plan := hookInstallPlan{id: "gemini", assets: []string{"gemini.json"}, paths: []string{user, project}}
	fixture := func(events ...string) string {
		doc := hookInstallDocument{Hooks: make(map[string][]hookInstallEntry)}
		for _, event := range events {
			doc.Hooks[event] = []hookInstallEntry{{Hooks: []hookInstallEntry{{Name: "aether-inbox", Type: "command", Command: "/usr/local/bin/aether-internal hook gemini " + event}}}}
		}
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	hookInstallFixture(t, user, fixture("BeforeAgent", "AfterTool"))
	result, err := inspectHookInstallation(plan)
	if err != nil || result.state != "missing" || !strings.Contains(strings.Join(result.details, " "), "AfterAgent") {
		t.Fatalf("partial install = %+v, %v", result, err)
	}
	hookInstallFixture(t, project, fixture("AfterAgent"))
	result, err = inspectHookInstallation(plan)
	if err != nil || result.state != "configured" {
		t.Fatalf("combined registrations = %+v, %v", result, err)
	}
	hookInstallFixture(t, project, `{"hooks":{"AfterAgent":"not an array"}}`)
	result, err = inspectHookInstallation(plan)
	if err != nil || result.state != "invalid" {
		t.Fatalf("invalid registration = %+v, %v", result, err)
	}
}

func TestHookInstallationUnrelatedSettingsAreNotInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	hookInstallFixture(t, path, `{"theme":"dark"}`)
	result, err := inspectHookInstallation(hookInstallPlan{id: "claude", assets: []string{"claude.json"}, paths: []string{path}})
	if err != nil || result.state != "missing" {
		t.Fatalf("unrelated valid settings = %+v, %v", result, err)
	}
}

func TestHookInstallationDisableControlsAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, id, asset, extension, lower, upper, want string
	}{
		{"copilot JSONC disabled", "copilot", "copilot.json", ".json", "{ // policy\n\"disableAllHooks\": true,\n}", "{}", "disabled"},
		{"copilot project override", "copilot", "copilot.json", ".json", `{"disableAllHooks":true}`, `{"disableAllHooks":false}`, "missing"},
		{"codex feature", "codex", "codex.json", ".toml", "[features]\nhooks = false\n", "", "disabled"},
		{"omp exact id", "omp", "omp.ts", ".yml", "disabledExtensions:\n  - extension-module:aether\n", "", "disabled"},
		{"omp unrelated id", "omp", "omp.ts", ".yml", "disabledExtensions:\n  - extension-module:other\n", "", "unverified"},
		{"gemini named disable", "gemini", "gemini.json", ".json", `{"hooksConfig":{"disabled":["aether-inbox"]}}`, "{}", "disabled"},
		{"gemini system enable override", "gemini", "gemini.json", ".json", `{"hooksConfig":{"enabled":false}}`, `{"hooksConfig":{"enabled":true}}`, "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			lower, upper := filepath.Join(dir, "lower"+tc.extension), filepath.Join(dir, "upper"+tc.extension)
			hookInstallFixture(t, lower, tc.lower)
			hookInstallFixture(t, upper, tc.upper)
			result, err := inspectHookInstallation(hookInstallPlan{id: tc.id, assets: []string{tc.asset}, controls: []string{lower, upper}})
			if err != nil || result.state != tc.want {
				t.Fatalf("inspection = %+v, %v, want %s", result, err, tc.want)
			}
		})
	}
}

func TestHookInstallationBoundsAndCustomSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.ts")
	plan := hookInstallPlan{id: "pi", assets: []string{"pi.ts"}, paths: []string{path}}
	for _, content := range []string{"export default () => {};", strings.Repeat("x", hookInstallMaxBytes+1)} {
		hookInstallFixture(t, path, content)
		result, err := inspectHookInstallation(plan)
		if err != nil || result.state != "unverified" {
			t.Fatalf("unverified source = %+v, %v", result, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != content {
			t.Fatal("read-only inspection changed the configuration")
		}
	}
	plan.paths = []string{t.TempDir()}
	result, err := inspectHookInstallation(plan)
	if err != nil || result.state != "unverified" {
		t.Fatalf("directory = %+v, %v", result, err)
	}
}

func TestHookSummaryReportsOnlyTheRunHarness(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	in := hookInstallInputs{home: home, cwd: project, env: func(string) string { return "" }}
	destinations := map[string]string{
		"claude":  filepath.Join(home, ".claude", "settings.json"),
		"codex":   filepath.Join(home, ".codex", "hooks.json"),
		"copilot": filepath.Join(home, ".copilot", "hooks", "aether.json"),
		"gemini":  filepath.Join(home, ".gemini", "settings.json"),
		"cursor":  filepath.Join(home, ".cursor", "hooks.json"),
	}
	for harness, destination := range destinations {
		t.Run(harness, func(t *testing.T) {
			var before strings.Builder
			if err := writeHookSummaryWithInputs(&before, harness, in); err != nil || !strings.Contains(before.String(), "Inbox hooks ("+harness+"): missing") {
				t.Fatalf("summary before install = %q, %v", before.String(), err)
			}
			var asset strings.Builder
			if code, err := hookFile(&asset, []string{harness + ".json"}); err != nil || code != ExitOK {
				t.Fatalf("export %s.json = %d, %v", harness, code, err)
			}
			hookInstallFixture(t, destination, asset.String())
			var after strings.Builder
			if err := writeHookSummaryWithInputs(&after, harness, in); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(after.String(), "Inbox hooks ("+harness+"): configured.\n") || strings.Count(after.String(), "Inbox hooks (") != 1 {
				t.Fatalf("summary after install = %q", after.String())
			}
		})
	}
	var opencode strings.Builder
	if err := writeHookSummaryWithInputs(&opencode, "opencode", in); err != nil || !strings.Contains(opencode.String(), "(opencode-v1)") || !strings.Contains(opencode.String(), "(opencode-v2)") {
		t.Fatalf("opencode summary = %q, %v", opencode.String(), err)
	}
}

// An Enhanced run gets its mail as a session prompt, so its hooks do nothing
// and are never reported missing.
func TestHookInstallManagedLaunchLoadsHooks(t *testing.T) {
	const managed = "Inbox hooks (opencode): loaded by this run's launch; do not install a manual copy.\n"
	const enhanced = "Inbox hooks: not used in Enhanced runs; Aether hands this run its mail as a session prompt. Do not install them.\n"
	for _, tt := range []struct {
		name    string
		env     map[string]string
		managed bool
	}{
		{"managed", map[string]string{"AETHER_MANAGED_HOOKS": "1"}, true},
		{"managed enhanced", map[string]string{"AETHER_MANAGED_HOOKS": "1", "AETHER_ENHANCED": "1"}, false},
		{"enhanced", map[string]string{"AETHER_ENHANCED": "1"}, false},
		{"unmanaged", map[string]string{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := tt.env
			env["AETHER_HARNESS"] = "opencode"
			in := hookInstallInputs{home: t.TempDir(), cwd: t.TempDir(), env: func(key string) string { return env[key] }}
			var summary, full strings.Builder
			if err := writeHookSummaryWithInputs(&summary, "opencode", in); err != nil {
				t.Fatal(err)
			}
			if err := writeHookInstallationWithInputs(&full, in); err != nil {
				t.Fatal(err)
			}
			if tt.managed {
				if summary.String() != managed {
					t.Fatalf("summary = %q, want %q", summary.String(), managed)
				}
				if !strings.HasPrefix(full.String(), managed+"\nHook installation (read-only)") {
					t.Fatalf("installation = %q", full.String())
				}
				return
			}
			if env["AETHER_ENHANCED"] == "1" {
				if summary.String() != enhanced || full.String() != "\n"+enhanced {
					t.Fatalf("summary = %q, installation = %q", summary.String(), full.String())
				}
				return
			}
			if !strings.Contains(summary.String(), "Install or inspect: aether-internal skill --hooks") || strings.Contains(summary.String(), "loaded by this run's launch") {
				t.Fatalf("summary = %q", summary.String())
			}
			if !strings.HasPrefix(full.String(), "\nHook installation (read-only)") {
				t.Fatalf("installation = %q", full.String())
			}
		})
	}
}
