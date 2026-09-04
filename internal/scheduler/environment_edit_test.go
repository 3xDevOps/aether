package scheduler

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// The pair the scripted edit container writes on the happy path. Valid
// under the envdef contract and distinct from the predecessor so the
// stored proposal is visibly the agent's output.
const editedEnvDockerfile = "FROM ubuntu:24.04\nRUN apt-get update && apt-get install -y golang-go\n"

const editedEnvManifestJSON = `[
  {
    "name": "go",
    "version": "1.22",
    "reason": "requested in the edit",
    "start_line": 2,
    "end_line": 2,
    "check_command": "go version"
  }
]`

// newEditTestEnv wires a testEnv with the persistent member home and the
// edit scratch root.
func newEditTestEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, func(c *Config) {
		c.EnvEditDir = filepath.Join(t.TempDir(), "env-edits")
	})
	return e
}

// grantLoginState installs the resolved agent executable in the member's
// persistent home, which is what a completed terminal setup leaves behind.
func grantLoginState(t *testing.T, e *testEnv, harnessName string) {
	t.Helper()
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatalf("member home: %v", err)
	}
	executable := harnessName
	if p, ok := harness.Lookup(harnessName); ok && len(p.HeadlessArgs) > 0 {
		executable = p.HeadlessArgs[0]
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatalf("create agent bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, executable), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write agent executable: %v", err)
	}
}

// saveMirrorDefinition stores a predecessor whose provenance the proposal
// must carry over.
func saveMirrorDefinition(t *testing.T, e *testEnv) *domain.EnvironmentDefinition {
	t.Helper()
	def := &domain.EnvironmentDefinition{
		WorkspaceID: e.ws.ID,
		Dockerfile:  envTestDockerfile,
		Manifest: []domain.ManifestItem{{
			Name:         "node",
			Version:      "20.0",
			StartLine:    2,
			EndLine:      2,
			CheckCommand: "node --version",
		}},
		Source:  domain.EnvironmentSourceMirror,
		Harness: "codex",
		Status:  domain.EnvironmentSaved,
	}
	if err := e.db.SaveEnvironmentDefinition(t.Context(), def); err != nil {
		t.Fatalf("save predecessor definition: %v", err)
	}
	return def
}

// waitEditStatus reads sub until an environment.edit event with the
// wanted status arrives, reporting whether any line event passed by.
func waitEditStatus(t *testing.T, sub events.Subscription, status events.EnvironmentEditStatus) (events.EnvironmentEditPayload, bool) {
	t.Helper()
	sawLine := false
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("event stream closed while waiting for environment.edit %s", status)
			}
			p, isEdit := ev.Payload.(events.EnvironmentEditPayload)
			if !isEdit {
				continue
			}
			if p.Line != "" {
				sawLine = true
			}
			if p.Status == status {
				return p, sawLine
			}
		case <-deadline:
			t.Fatalf("timed out waiting for environment.edit %s", status)
		}
	}
}

func scratchEntries(t *testing.T, e *testEnv) int {
	t.Helper()
	entries, err := os.ReadDir(e.cfg.EnvEditDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read scratch root: %v", err)
	}
	return len(entries)
}

func TestEditEnvironmentRejectsNonSetupHarness(t *testing.T) {
	e := newEditTestEnv(t)
	saveMirrorDefinition(t, e)

	_, err := e.sched.EditEnvironment(t.Context(), e.ws.ID, e.member.ID, "opencode", "add go")
	if !errors.Is(err, ErrEnvironmentEditPreflight) {
		t.Fatalf("error = %v, want ErrEnvironmentEditPreflight", err)
	}
	if want := "aether agent add opencode"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to name %q", err, want)
	}
}

func TestEditEnvironmentRequiresLoginState(t *testing.T) {
	e := newEditTestEnv(t)
	saveMirrorDefinition(t, e)
	sub := e.subscribe(t)

	_, err := e.sched.EditEnvironment(t.Context(), e.ws.ID, e.member.ID, "claude", "add go")
	if !errors.Is(err, ErrEnvironmentEditPreflight) {
		t.Fatalf("error = %v, want ErrEnvironmentEditPreflight", err)
	}
	want := "aether agent add claude"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to name %q", err, want)
	}
	failed, _ := waitEditStatus(t, sub, events.EnvironmentEditFailed)
	if !strings.Contains(failed.Detail, want) {
		t.Errorf("failed event detail = %q, want it to name %q", failed.Detail, want)
	}
	if failed.Harness != "claude" {
		t.Errorf("failed event harness = %q, want claude", failed.Harness)
	}
}

func TestEditEnvironmentHappyPath(t *testing.T) {
	e := newEditTestEnv(t)
	predecessor := saveMirrorDefinition(t, e)
	grantLoginState(t, e, "claude")

	var mu sync.Mutex
	var spec runtime.Spec
	e.rt.startHook = func(c *fakeContainer) {
		mu.Lock()
		spec = c.spec
		mu.Unlock()
		for _, m := range c.spec.Mounts {
			if m.ContainerPath != environmentEditOutputDir {
				continue
			}
			if werr := os.WriteFile(filepath.Join(m.HostPath, "Dockerfile"), []byte(editedEnvDockerfile), 0o644); werr != nil {
				t.Errorf("write edited Dockerfile: %v", werr)
			}
			if werr := os.WriteFile(filepath.Join(m.HostPath, "manifest.json"), []byte(editedEnvManifestJSON), 0o644); werr != nil {
				t.Errorf("write edited manifest: %v", werr)
			}
		}
		c.output("installing go 1.22\n")
		c.exitNow(0)
	}
	sub := e.subscribe(t)

	version, err := e.sched.EditEnvironment(t.Context(), e.ws.ID, e.member.ID, "claude", "add go 1.22")
	if err != nil {
		t.Fatalf("EditEnvironment: %v", err)
	}
	if version != predecessor.Version+1 {
		t.Errorf("proposed version = %d, want %d", version, predecessor.Version+1)
	}

	mu.Lock()
	captured := spec
	mu.Unlock()
	prompt := captured.Command[len(captured.Command)-1]
	for _, want := range []string{"add go 1.22", environmentEditOutputDir, envTestDockerfile} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt argv is missing %q", want)
		}
	}
	mountsByTarget := map[string]runtime.Mount{}
	for _, m := range captured.Mounts {
		mountsByTarget[m.ContainerPath] = m
	}
	if len(captured.Mounts) != 2 {
		t.Fatalf("mounts = %v, want home and scratch", captured.Mounts)
	}
	if home, ok := mountsByTarget["/root"]; !ok || home.ReadOnly {
		t.Errorf("mounts = %v, want the writable member home at /root", captured.Mounts)
	}
	if m, ok := mountsByTarget[environmentEditOutputDir]; !ok || m.ReadOnly {
		t.Errorf("mounts = %v, want a writable scratch mount at %s", captured.Mounts, environmentEditOutputDir)
	}

	stored := mustGetEnvDefinition(t, e, version)
	if stored.Status != domain.EnvironmentSaved {
		t.Errorf("status = %q, want %q", stored.Status, domain.EnvironmentSaved)
	}
	if stored.Source != domain.EnvironmentSourceMirror {
		t.Errorf("source = %q, want the predecessor's %q", stored.Source, domain.EnvironmentSourceMirror)
	}
	if stored.Harness != "claude" {
		t.Errorf("harness = %q, want the editing claude", stored.Harness)
	}
	if stored.Dockerfile != editedEnvDockerfile {
		t.Errorf("stored Dockerfile = %q, want the edited pair", stored.Dockerfile)
	}

	proposed, sawLine := waitEditStatus(t, sub, events.EnvironmentEditProposed)
	if proposed.Version != version {
		t.Errorf("proposed event version = %d, want %d", proposed.Version, version)
	}
	if !sawLine {
		t.Error("no agent output line reached the event feed")
	}
	if got := scratchEntries(t, e); got != 0 {
		t.Errorf("scratch root has %d leftover entries, want 0", got)
	}
}

func TestEditEnvironmentRetriesOnceThenFails(t *testing.T) {
	e := newEditTestEnv(t)
	saveMirrorDefinition(t, e)
	grantLoginState(t, e, "claude")

	creates := 0
	e.rt.createHook = func() { creates++ }
	// The agent never writes manifest.json, so validation fails twice.
	e.rt.startHook = func(c *fakeContainer) {
		for _, m := range c.spec.Mounts {
			if m.ContainerPath == environmentEditOutputDir {
				_ = os.WriteFile(filepath.Join(m.HostPath, "Dockerfile"), []byte(editedEnvDockerfile), 0o644)
			}
		}
		c.exitNow(0)
	}
	sub := e.subscribe(t)

	_, err := e.sched.EditEnvironment(t.Context(), e.ws.ID, e.member.ID, "claude", "add go")
	if err == nil || !strings.Contains(err.Error(), "manifest.json") {
		t.Fatalf("error = %v, want the missing manifest named", err)
	}
	if creates != 2 {
		t.Errorf("containers created = %d, want one retry after the first invalid output", creates)
	}
	waitEditStatus(t, sub, events.EnvironmentEditRetrying)
	failed, _ := waitEditStatus(t, sub, events.EnvironmentEditFailed)
	if !strings.Contains(failed.Detail, "manifest.json") {
		t.Errorf("failed event detail = %q, want the missing manifest named", failed.Detail)
	}
	if got := scratchEntries(t, e); got != 0 {
		t.Errorf("scratch root has %d leftover entries, want 0", got)
	}
	if defs, lerr := e.db.ListEnvironmentDefinitions(t.Context(), e.ws.ID); lerr != nil || len(defs) != 1 {
		t.Errorf("definitions = %d (err %v), want only the predecessor after a failed edit", len(defs), lerr)
	}
}

func TestEditEnvironmentFakeHarnessEndToEnd(t *testing.T) {
	e := newEditTestEnv(t)
	predecessor := saveMirrorDefinition(t, e)
	sub := e.subscribe(t)

	version, err := e.sched.EditEnvironment(t.Context(), e.ws.ID, e.member.ID, "fake", "swap node for jq")
	if err != nil {
		t.Fatalf("EditEnvironment: %v", err)
	}
	if version != predecessor.Version+1 {
		t.Errorf("proposed version = %d, want %d", version, predecessor.Version+1)
	}
	if got := len(e.rt.allContainers()); got != 0 {
		t.Errorf("fake edit created %d containers, want none", got)
	}
	stored := mustGetEnvDefinition(t, e, version)
	if stored.Source != domain.EnvironmentSourceMirror {
		t.Errorf("source = %q, want the predecessor's %q", stored.Source, domain.EnvironmentSourceMirror)
	}
	if stored.Harness != "fake" {
		t.Errorf("harness = %q, want fake", stored.Harness)
	}
	if stored.Status != domain.EnvironmentSaved {
		t.Errorf("status = %q, want %q", stored.Status, domain.EnvironmentSaved)
	}
	proposed, _ := waitEditStatus(t, sub, events.EnvironmentEditProposed)
	if proposed.Version != version {
		t.Errorf("proposed event version = %d, want %d", proposed.Version, version)
	}
}

func TestEditEnvironmentSerializesWithBuild(t *testing.T) {
	e := newEditTestEnv(t)
	def := saveEnvDefinition(t, e)
	driveVerification(e, "v20.0.1")

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	e.rt.buildHook = func(string) {
		entered <- struct{}{}
		<-release
	}

	buildDone := make(chan error, 1)
	go func() { buildDone <- e.sched.BuildEnvironment(t.Context(), e.ws.ID, def.Version) }()
	<-entered

	editDone := make(chan error, 1)
	var version int
	go func() {
		v, err := e.sched.EditEnvironment(t.Context(), e.ws.ID, e.member.ID, "fake", "add jq")
		version = v
		editDone <- err
	}()
	select {
	case err := <-editDone:
		t.Fatalf("edit finished while the build held the lock (err %v)", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	if err := <-buildDone; err != nil {
		t.Fatalf("BuildEnvironment: %v", err)
	}
	if err := <-editDone; err != nil {
		t.Fatalf("EditEnvironment after the build: %v", err)
	}
	if version != def.Version+1 {
		t.Errorf("proposed version = %d, want %d", version, def.Version+1)
	}
}
