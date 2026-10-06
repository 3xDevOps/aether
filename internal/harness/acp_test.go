package harness

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
)

// A pin bumped in acpregistry.json and not in the snapshot would install a
// version the snapshot does not describe.
func TestACPRegistryPins(t *testing.T) {
	raw, err := os.ReadFile("acpregistry.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Agents []struct {
			ID           string `json:"id"`
			Version      string `json:"version"`
			License      string `json:"license"`
			Repository   string `json:"repository"`
			Distribution struct {
				NPX struct {
					Package string `json:"package"`
				} `json:"npx"`
			} `json:"distribution"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	pinned := map[string]*ACPInstall{}
	for _, p := range Profiles() {
		if p.ACPInstall != nil {
			pinned[p.ACPInstall.RegistryID] = p.ACPInstall
		}
	}
	for _, entry := range snapshot.Agents {
		install, ok := pinned[entry.ID]
		if !ok {
			t.Errorf("snapshot entry %q is not used by any profile", entry.ID)
			continue
		}
		delete(pinned, entry.ID)
		if want := install.Package + "@" + install.Version; entry.Distribution.NPX.Package != want || entry.Version != install.Version {
			t.Errorf("%s: snapshot pins %s (version %s), profile installs %s", entry.ID, entry.Distribution.NPX.Package, entry.Version, want)
		}
		if entry.License == "" || entry.Repository == "" {
			t.Errorf("%s: snapshot entry lacks its license or repository", entry.ID)
		}
	}
	for id := range pinned {
		t.Errorf("profile adapter %q is missing from acpregistry.json", id)
	}
}

func TestEnhancedSupport(t *testing.T) {
	want := map[string]Enhanced{
		"claude": EnhancedAdapter, "codex": EnhancedAdapter, "pi": EnhancedAdapter,
		"omp": EnhancedNative, "opencode": EnhancedNative, "custom": EnhancedNone,
	}
	for _, p := range Profiles() {
		if got := p.EnhancedSupport(); got != want[p.Name] {
			t.Errorf("%s: EnhancedSupport = %q, want %q", p.Name, got, want[p.Name])
		}
		if p.ACPInstall != nil && !slices.Equal(p.ACPArgs, []string{p.ACPInstall.Binary}) {
			t.Errorf("%s: ACPArgs %v do not run the adapter %q", p.Name, p.ACPArgs, p.ACPInstall.Binary)
		}
		for _, arg := range p.ACPArgs {
			if strings.Contains(arg, TaskPlaceholder) {
				t.Errorf("%s: ACPArgs carry the task, which travels over the protocol", p.Name)
			}
		}
		if len(p.ResumeArgs) > 0 && (p.ResumeArgs[0] != p.TUIArgs[0] || !slices.ContainsFunc(p.ResumeArgs, func(a string) bool {
			return strings.Contains(a, SessionPlaceholder)
		})) {
			t.Errorf("%s: ResumeArgs %v must run the TUI with the session placeholder", p.Name, p.ResumeArgs)
		}
	}
	custom := Definition{Name: "aider", Executable: "aider", TUIArgs: []string{"aider"}, HeadlessArgs: []string{"aider"},
		ACPArgs: []string{"aider-acp", "--stdio"}}
	if err := custom.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := custom.Profile().EnhancedSupport(); got != EnhancedNative {
		t.Fatalf("definition with acp argv: EnhancedSupport = %q, want native", got)
	}
	for _, bad := range [][]string{{"/usr/bin/aider-acp"}, {"aider-acp", ""}} {
		custom.ACPArgs = bad
		if err := custom.Validate(); err == nil {
			t.Errorf("Validate accepted acp argv %q", bad)
		}
	}
}

func TestInstallCommand(t *testing.T) {
	claude, _ := Lookup("claude")
	if got := claude.InstallCommand(false); got != claude.InstallScript {
		t.Fatalf("InstallCommand(false) = %q", got)
	}
	want := claude.InstallScript + ` && npm install -g --prefix "$HOME/.local" @agentclientprotocol/claude-agent-acp@0.86.0`
	if got := claude.InstallCommand(true); got != want {
		t.Fatalf("InstallCommand(true) = %q, want %q", got, want)
	}
	omp, _ := Lookup("omp")
	if got := omp.InstallCommand(true); got != omp.InstallScript {
		t.Fatalf("native agent InstallCommand(true) = %q, want its own install", got)
	}
}

// The adapter half of an update script leaves a member without the adapter
// alone, and moves an installed one to the pin through the same staged
// exchange as the agent itself.
func TestAdapterUpdate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("updates run in Linux containers")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("the update reads the installed version with node")
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adapter := &ACPInstall{Package: "@example/agent-acp", Version: "2.0.0", Binary: "agent-acp"}
	run := func(t *testing.T, installed string) string {
		t.Helper()
		home := t.TempDir()
		stubs := filepath.Join(home, "stubs")
		pkgDir := filepath.Join(home, ".local", "lib", "node_modules", adapter.Package)
		for _, dir := range []string{stubs, filepath.Dir(pkgDir)} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if installed != "" {
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"x","version":"`+installed+`"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		npm := `#!/bin/sh
[ "$1" = install ] || exit 1
while [ $# -gt 0 ]; do
	[ "$1" = --prefix ] && { shift; prefix=$1; }
	last=$1
	shift
done
echo "$last" >> "$HOME/npm-installs"
dir="$prefix/lib/node_modules/${last%@*}"
mkdir -p "$dir" && printf '{"version":"%s"}' "${last##*@}" > "$dir/package.json"
`
		if err := os.WriteFile(filepath.Join(stubs, "npm"), []byte(npm), 0o755); err != nil {
			t.Fatal(err)
		}
		script := strings.ReplaceAll(withAdapterUpdate("true", adapter), agentstatus.ReporterCommand, testBinary)
		cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", script)
		node, _ := exec.LookPath("node")
		cmd.Env = []string{"HOME=" + home, "PATH=" + stubs + ":" + filepath.Dir(node) + ":/usr/bin:/bin"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("update: %v\n%s", err, out)
		}
		installs, _ := os.ReadFile(filepath.Join(home, "npm-installs"))
		if installed != "" {
			got, _ := os.ReadFile(filepath.Join(pkgDir, "package.json"))
			if !strings.Contains(string(got), `"2.0.0"`) {
				t.Fatalf("adapter after update = %s", got)
			}
		} else if _, err := os.Stat(pkgDir); err == nil {
			t.Fatal("update installed an adapter the member never asked for")
		}
		return string(installs)
	}
	if got := run(t, ""); got != "" {
		t.Fatalf("missing adapter: npm ran %q", got)
	}
	if got := run(t, "2.0.0"); got != "" {
		t.Fatalf("current adapter: npm ran %q", got)
	}
	if got := run(t, "1.0.0"); got != "@example/agent-acp@2.0.0\n" {
		t.Fatalf("stale adapter: npm ran %q", got)
	}
}
