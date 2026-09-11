package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	coordpkg "github.com/3xDevOps/Aether/internal/coord"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
)

// recordingCoordinator is the fake coordinator plus the asset files each
// run was provisioned with - where the harness MCP registration and the
// status reporter land.
type recordingCoordinator struct {
	fakeCoordinator

	mu    sync.Mutex
	files map[domain.RunID]map[string][]byte
}

func (r *recordingCoordinator) Provision(ctx context.Context, run domain.RunID, files map[string][]byte) (string, error) {
	r.mu.Lock()
	r.files[run] = files
	r.mu.Unlock()
	return r.fakeCoordinator.Provision(ctx, run, files)
}

func (r *recordingCoordinator) file(run domain.RunID, name string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.files[run][name]
}

// TestHarnessMCPRegistration is the launch-time half of the registration
// contract: a harness whose profile carries the flag is given a config
// naming the staged bridge and the argument pointing at it, and one that
// does not is launched untouched - notice-only, with no config written.
func TestHarnessMCPRegistration(t *testing.T) {
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	dir := t.TempDir()
	coord := &recordingCoordinator{
		fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
		files:           make(map[domain.RunID]map[string][]byte),
	}
	e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))

	registered, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "add OAuth login", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("launch claude run: %v", err)
	}
	argv := e.rt.byName(string(registered.ID)).spec.Command
	if i := slices.Index(argv, "--mcp-config"); i < 0 || i+1 >= len(argv) || argv[i+1] != mcpConfigPath {
		t.Fatalf("registered harness argv = %v, want --mcp-config %s in it", argv, mcpConfigPath)
	}

	var doc struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(coord.file(registered.ID, coordpkg.ConfigName), &doc); err != nil {
		t.Fatalf("decode written MCP config: %v", err)
	}
	srv, ok := doc.Servers[mcpbridge.ServerName]
	if !ok || srv.Type != "stdio" || srv.Command != mcpbridge.BinaryPath || !slices.Equal(srv.Args, []string{bridgeSubcommand}) {
		t.Fatalf("MCP config = %+v, want a stdio %s server running %s %s",
			doc.Servers, mcpbridge.ServerName, mcpbridge.BinaryPath, bridgeSubcommand)
	}

	unsupported, _ := e.launchFake(t, "fix the auth bug")
	argv = e.rt.byName(string(unsupported.ID)).spec.Command
	if slices.Contains(argv, "--mcp-config") {
		t.Fatalf("unsupported harness argv = %v, want no MCP registration", argv)
	}
	if cfg := coord.file(unsupported.ID, coordpkg.ConfigName); cfg != nil {
		t.Fatalf("unsupported harness had an MCP config written: %s", cfg)
	}
}

// TestHarnessStatusReporterRegistration is the same contract for the
// status reporter: an interactive run on a harness that can report is
// given the settings asset and the argument pointing at it, a headless one
// is not - it never waits for anyone - and a harness with no reporter is
// launched untouched. What the run records about its reporter follows the
// asset, not the harness name: it is the claim a restart reads back, and
// the scheduler holds a parked run for its member on the strength of it.
func TestHarnessStatusReporterRegistration(t *testing.T) {
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	dir := t.TempDir()
	coord := &recordingCoordinator{
		fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
		files:           make(map[domain.RunID]map[string][]byte),
	}
	e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))
	settingsPath := path.Join(mcpbridge.MountDir, agentstatus.ClaudeSettingsName)

	tui, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "add OAuth login", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("launch claude run: %v", err)
	}
	argv := e.rt.byName(string(tui.ID)).spec.Command
	if i := slices.Index(argv, "--settings"); i < 0 || i+1 >= len(argv) || argv[i+1] != settingsPath {
		t.Fatalf("interactive claude argv = %v, want --settings %s in it", argv, settingsPath)
	}
	if got := coord.file(tui.ID, agentstatus.ClaudeSettingsName); !bytes.Equal(got, agentstatus.ClaudeSettings) {
		t.Fatalf("settings written for the run = %s, want the embedded asset", got)
	}
	if got := e.reporterOf(t, tui.ID); got != harness.ReporterFull {
		t.Fatalf("interactive claude run recorded reporter %s, want %s", got, harness.ReporterFull)
	}
	// The asset is a leaf package's bytes and the binary path is the
	// scheduler's, so nothing but this pins the two together: a hook that
	// names a path the run container does not carry reports nothing, and
	// silently.
	wantCommand := mcpbridge.BinaryPath + " report claude"
	if !bytes.Contains(agentstatus.ClaudeSettings, []byte(`"`+wantCommand+`"`)) {
		t.Fatalf("%s runs something other than %q; the staged binary is what the container has",
			agentstatus.ClaudeSettingsName, wantCommand)
	}

	headless, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "ship it", "claude", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("launch headless claude run: %v", err)
	}
	if argv := e.rt.byName(string(headless.ID)).spec.Command; slices.Contains(argv, "--settings") {
		t.Fatalf("headless claude argv = %v, want no status reporter", argv)
	}
	if got := coord.file(headless.ID, agentstatus.ClaudeSettingsName); got != nil {
		t.Fatalf("headless run had settings written: %s", got)
	}
	// The registry calls claude a full reporter, but this container was
	// given no hooks and can never report. Recording the harness's kind
	// here would make a restart hold the run for a member the agent is
	// never going to ask for.
	if got := e.reporterOf(t, headless.ID); got != harness.ReporterNone {
		t.Fatalf("headless claude run recorded reporter %s, want %s", got, harness.ReporterNone)
	}

	unreported, _ := e.launchFake(t, "fix the auth bug")
	if argv := e.rt.byName(string(unreported.ID)).spec.Command; slices.Contains(argv, "--settings") {
		t.Fatalf("harness without a reporter argv = %v, want no status reporter", argv)
	}
	if got := coord.file(unreported.ID, agentstatus.ClaudeSettingsName); got != nil {
		t.Fatalf("harness without a reporter had settings written: %s", got)
	}
	if got := e.reporterOf(t, unreported.ID); got != harness.ReporterNone {
		t.Fatalf("run on a harness without a reporter recorded %s, want %s", got, harness.ReporterNone)
	}
}

// reporterOf is the status reporter the scheduler recorded for a live run:
// what its sidecar carries and what a restart hands back to checkStalls.
func (e *testEnv) reporterOf(t *testing.T, run domain.RunID) harness.Reporter {
	t.Helper()
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	entry := e.sched.runs[run]
	if entry == nil {
		t.Fatalf("run %s is not supervised", run)
	}
	return entry.reporter
}

// TestOpenCodeStatusReporterRegistration is the same contract for a
// harness whose reporter rides in the environment instead of on the
// command line: the plugin lands in the run's coordination directory and
// opencode is told to load it from there, an interactive run alone, and
// the argv it was launched with is untouched.
func TestOpenCodeStatusReporterRegistration(t *testing.T) {
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	dir := t.TempDir()
	coord := &recordingCoordinator{
		fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
		files:           make(map[domain.RunID]map[string][]byte),
	}
	e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))

	tui, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "add OAuth login", "opencode", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("launch opencode run: %v", err)
	}
	if got := coord.file(tui.ID, agentstatus.OpenCodePluginName); !bytes.Equal(got, agentstatus.OpenCodePlugin) {
		t.Fatalf("plugin written for the run = %s, want the embedded asset", got)
	}
	spec := e.rt.byName(string(tui.ID)).spec
	wantConfig := `{"plugin":["file://` + path.Join(mcpbridge.MountDir, agentstatus.OpenCodePluginName) + `"]}`
	if got := spec.Env["OPENCODE_CONFIG_CONTENT"]; got != wantConfig {
		t.Fatalf("OPENCODE_CONFIG_CONTENT = %q, want %q", got, wantConfig)
	}
	// opencode has no flag for a plugin, so the launch command is exactly
	// what a run without a reporter would have had: nothing on it names
	// the coordination directory.
	if argv := spec.Command; len(argv) != 2 || slices.ContainsFunc(argv, func(a string) bool {
		return strings.Contains(a, agentstatus.OpenCodePluginName)
	}) {
		t.Fatalf("opencode argv = %v, want the launch command untouched", argv)
	}
	if got := e.reporterOf(t, tui.ID); got != harness.ReporterFull {
		t.Fatalf("interactive opencode run recorded reporter %s, want %s", got, harness.ReporterFull)
	}
	// The plugin is a leaf package's bytes and the binary path is the
	// scheduler's; nothing but this pins the two together.
	wantCommand := `"` + mcpbridge.BinaryPath + `"`
	if !bytes.Contains(agentstatus.OpenCodePlugin, []byte(wantCommand)) {
		t.Fatalf("%s spawns something other than %s; the staged binary is what the container has",
			agentstatus.OpenCodePluginName, wantCommand)
	}

	headless, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "ship it", "opencode", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("launch headless opencode run: %v", err)
	}
	if got := coord.file(headless.ID, agentstatus.OpenCodePluginName); got != nil {
		t.Fatalf("headless run had the plugin written: %s", got)
	}
	if got := e.rt.byName(string(headless.ID)).spec.Env["OPENCODE_CONFIG_CONTENT"]; got != "" {
		t.Fatalf("headless opencode run carries OPENCODE_CONFIG_CONTENT = %q, want none", got)
	}
	if got := e.reporterOf(t, headless.ID); got != harness.ReporterNone {
		t.Fatalf("headless opencode run recorded reporter %s, want %s", got, harness.ReporterNone)
	}
}
