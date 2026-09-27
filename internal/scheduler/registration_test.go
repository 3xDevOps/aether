package scheduler

import (
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
)

// recordingCoordinator is the fake coordinator plus the asset files each
// run was provisioned with - status and taskless discovery assets.
type recordingCoordinator struct {
	fakeCoordinator

	mu    sync.Mutex
	files map[domain.RunID]map[string][]byte
}

func (r *recordingCoordinator) Provision(ctx context.Context, run domain.RunID, files map[string][]byte) (string, error) {
	r.mu.Lock()
	r.files[run] = files
	r.mu.Unlock()
	dir, err := r.fakeCoordinator.Provision(ctx, run, files)
	if err != nil {
		return "", err
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o444); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func (r *recordingCoordinator) file(run domain.RunID, name string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.files[run][name]
}

// TestHarnessProvisioningDoesNotRegisterMCP proves provisioning leaves
// user-controlled MCP configuration alone. The staged CLI and lifecycle
// assets are separate surfaces.
func TestHarnessProvisioningDoesNotRegisterMCP(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	dir := t.TempDir()
	coord := &recordingCoordinator{
		fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
		files:           make(map[domain.RunID]map[string][]byte),
	}
	e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))

	for _, name := range []string{"claude", "fake"} {
		run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "add OAuth login", name, domain.LaunchTUI)
		if err != nil {
			t.Fatalf("launch %s run: %v", name, err)
		}
		argv := e.rt.byName(string(run.ID)).spec.Command
		if slices.Contains(argv, "--mcp-config") {
			t.Fatalf("%s argv = %v, want no automatic MCP registration", name, argv)
		}
		if cfg := coord.file(run.ID, "mcp.json"); cfg != nil {
			t.Fatalf("%s run had an MCP config written: %s", name, cfg)
		}
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
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	dir := t.TempDir()
	coord := &recordingCoordinator{
		fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
		files:           make(map[domain.RunID]map[string][]byte),
	}
	e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))
	settingsPath := path.Join(coordtransport.MountDir, agentstatus.ClaudeSettingsName)

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

func tuiHarnessCommand(t *testing.T, command []string) []string {
	t.Helper()
	if len(command) < 4 || command[0] != "/bin/sh" || command[1] != "-c" {
		t.Fatalf("TUI command = %v, want POSIX supervisor", command)
	}
	return command[4:]
}

// Both OpenCode API generations get separate native and status assets, while
// launch-time version selection leaves the member's configuration untouched.
func TestOpenCodeNativeAndStatusRegistration(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	dir := t.TempDir()
	coord := &recordingCoordinator{
		fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
		files:           make(map[domain.RunID]map[string][]byte),
	}
	e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))
	const workspaceConfig = `{"model":"anthropic/claude-sonnet-4-5"}`
	e.ws.Environment.Variables["OPENCODE_CONFIG_CONTENT"] = workspaceConfig
	if err := e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}

	tui, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "add OAuth login", "opencode", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("launch opencode run: %v", err)
	}
	if got := coord.file(tui.ID, agentstatus.OpenCodePluginName); !bytes.Equal(got, agentstatus.OpenCodePlugin) {
		t.Fatalf("plugin written for the run = %s, want the embedded asset", got)
	}
	spec := e.rt.byName(string(tui.ID)).spec
	if got := spec.Env["OPENCODE_CONFIG_CONTENT"]; got != workspaceConfig {
		t.Fatalf("workspace inline config changed before version selection: %q", got)
	}
	for id, file := range map[string]string{
		"aether-mailbox": "opencode-v2.js",
		"aether-status":  agentstatus.OpenCodeV2PluginName,
	} {
		target := filepath.Join(harness.OpenCodeNativeDiscoveryRoot, id+"-"+string(tui.ID), "index.js")
		mount, ok := mountFor(spec, target)
		if !ok || !mount.ReadOnly {
			t.Fatalf("missing read-only native discovery package %s", target)
		}
		body, readErr := os.ReadFile(mount.HostPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(body, coord.file(tui.ID, file)) {
			t.Fatalf("%s discovery package points at the wrong API or status asset", id)
		}
	}
	if got := e.reporterOf(t, tui.ID); got != harness.ReporterFull {
		t.Fatalf("interactive opencode run recorded reporter %s, want %s", got, harness.ReporterFull)
	}

	headless, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "ship it", "opencode", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("launch headless opencode run: %v", err)
	}
	if got := coord.file(headless.ID, agentstatus.OpenCodePluginName); got != nil {
		t.Fatalf("headless run had the plugin written: %s", got)
	}
	// A headless run gets no reporter at all, so nothing replaces the
	// workspace's own value of the variable there.
	if got := e.rt.byName(string(headless.ID)).spec.Env["OPENCODE_CONFIG_CONTENT"]; got != workspaceConfig {
		t.Fatalf("headless opencode run carries OPENCODE_CONFIG_CONTENT = %q, want the workspace value %q", got, workspaceConfig)
	}
	if got := e.reporterOf(t, headless.ID); got != harness.ReporterNone {
		t.Fatalf("headless opencode run recorded reporter %s, want %s", got, harness.ReporterNone)
	}
}

// TestReporterRegistrationPerHarness is the same contract for the three
// harnesses that carry a reporter of their own. Codex needs no asset - the
// whole command rides a configuration override - while pi and omp share
// one extension file, so each has to be launched with what it was given
// and recorded as what it can actually say.
func TestReporterRegistrationPerHarness(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "#!/bin/sh\necho aether\n")))
	dir := t.TempDir()
	coord := &recordingCoordinator{
		fakeCoordinator: fakeCoordinator{root: filepath.Join(dir, "coord")},
		files:           make(map[domain.RunID]map[string][]byte),
	}
	e.sched.UseCoordination(coord, filepath.Join(dir, "runtime", "bin"))

	codex, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "review the diff", "codex", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("launch codex run: %v", err)
	}
	argv := tuiHarnessCommand(t, e.rt.byName(string(codex.ID)).spec.Command)
	if i := slices.Index(argv, "-c"); i < 0 || i+1 >= len(argv) || argv[i+1] != agentstatus.CodexNotifySetting {
		t.Fatalf("interactive codex argv = %v, want -c %s in it", argv, agentstatus.CodexNotifySetting)
	}
	if got := e.reporterOf(t, codex.ID); got != harness.ReporterTurnEnd {
		t.Fatalf("interactive codex run recorded reporter %s, want %s", got, harness.ReporterTurnEnd)
	}

	extension := path.Join(coordtransport.MountDir, agentstatus.PiExtensionName)
	for _, name := range []string{"pi", "omp"} {
		run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "add a test", name, domain.LaunchTUI)
		if err != nil {
			t.Fatalf("launch %s run: %v", name, err)
		}
		argv := tuiHarnessCommand(t, e.rt.byName(string(run.ID)).spec.Command)
		if i := slices.Index(argv, "-e"); i < 0 || i+1 >= len(argv) || argv[i+1] != extension {
			t.Fatalf("interactive %s argv = %v, want -e %s in it", name, argv, extension)
		}
		if got := coord.file(run.ID, agentstatus.PiExtensionName); !bytes.Equal(got, agentstatus.PiExtension) {
			t.Fatalf("%s extension written for the run = %s, want the embedded asset", name, got)
		}
		if got := e.reporterOf(t, run.ID); got != harness.ReporterFull {
			t.Fatalf("interactive %s run recorded reporter %s, want %s", name, got, harness.ReporterFull)
		}
	}
}
