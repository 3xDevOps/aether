package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// fakeCoordinator stands in for *coord.Service: it owns a directory per run
// and records what was released.
type fakeCoordinator struct {
	root string
	err  error

	// beforeWrite runs inside WriteCoAuthors, after the caller has read the
	// steerers and before the list lands, which is the window a test can
	// hold a writer in.
	beforeWrite func()

	mu             sync.Mutex
	onWake         func(domain.RunID)
	released       []domain.RunID
	idleWakes      []domain.RunID
	coAuthors      map[domain.RunID][]string
	coAuthorWrites map[domain.RunID]int
}

func (f *fakeCoordinator) Provision(_ context.Context, run domain.RunID, _ map[string][]byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	dir := filepath.Join(f.root, string(run))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (f *fakeCoordinator) WriteCoAuthors(run domain.RunID, trailers []string) error {
	if f.err != nil {
		return f.err
	}
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.coAuthors == nil {
		f.coAuthors = make(map[domain.RunID][]string)
		f.coAuthorWrites = make(map[domain.RunID]int)
	}
	f.coAuthors[run] = trailers
	f.coAuthorWrites[run]++
	return nil
}

func (f *fakeCoordinator) WriteFiles(run domain.RunID, files map[string][]byte) error {
	if f.err != nil {
		return f.err
	}
	dir := filepath.Join(f.root, string(run))
	for name, body := range files {
		tmp := filepath.Join(dir, "."+name)
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeCoordinator) trailers(run domain.RunID) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.coAuthors[run]
}

func (f *fakeCoordinator) writes(run domain.RunID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.coAuthorWrites[run]
}

func (f *fakeCoordinator) Release(run domain.RunID) error {
	f.mu.Lock()
	f.released = append(f.released, run)
	f.mu.Unlock()
	return os.RemoveAll(filepath.Join(f.root, string(run)))
}

func (f *fakeCoordinator) WakeIdle(run domain.RunID) {
	f.mu.Lock()
	f.idleWakes = append(f.idleWakes, run)
	onWake := f.onWake
	f.mu.Unlock()
	if onWake != nil {
		onWake(run)
	}
}

func (f *fakeCoordinator) EnhancedSessionOpened(context.Context, domain.RunID) {}

func (f *fakeCoordinator) idleWakeRuns() []domain.RunID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.idleWakes)
}

func (f *fakeCoordinator) releasedRuns() []domain.RunID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.released)
}

// fakeServerBinary writes a stand-in for the running server binary and
// returns its path, for Config.ServerBinary.
func fakeServerBinary(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aether-server")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake server binary: %v", err)
	}
	return path
}

// withServerBinary points a test scheduler's stager at path.
func withServerBinary(path string) func(*Config) {
	return func(c *Config) { c.ServerBinary = path }
}

// withCoordination attaches a coordinator and a staged-bridge directory to
// the env's scheduler and returns both.
func withCoordination(t *testing.T, e *testEnv) (*fakeCoordinator, string) {
	t.Helper()
	dir := t.TempDir()
	c := &fakeCoordinator{root: filepath.Join(dir, "coord")}
	binDir := filepath.Join(dir, "runtime", "bin")
	e.sched.UseCoordination(c, binDir)
	return c, binDir
}

func mountFor(spec runtime.Spec, containerPath string) (runtime.Mount, bool) {
	for _, m := range spec.Mounts {
		if m.ContainerPath == containerPath {
			return m, true
		}
	}
	return runtime.Mount{}, false
}

// TestRunCarriesCoordinationAssets is the whole host-side contract in one
// pass: the bridge binary is staged under its own hash and mounted
// read-only beside the run's coordination directory, both references are
// durable before the container exists, and both are cleaned up only after
// the container is destroyed.
func TestRunCarriesCoordinationAssets(t *testing.T) {
	t.Parallel()
	staged := fakeServerBinary(t, "#!/bin/sh\necho aether\n")
	e := newTestEnv(t, withServerBinary(staged))
	coord, binDir := withCoordination(t, e)
	sub := e.subscribe(t)

	run, container := e.launchFake(t, "add OAuth login")

	bin, ok := mountFor(container.spec, coordtransport.BinaryPath)
	if !ok || !bin.ReadOnly {
		t.Fatalf("no read-only staged bridge mount in %+v", container.spec.Mounts)
	}
	cli, ok := mountFor(container.spec, coordtransport.CLIPath)
	if !ok || !cli.ReadOnly {
		t.Fatalf("no read-only coordination CLI mount in %+v", container.spec.Mounts)
	}
	if cli.HostPath != bin.HostPath {
		t.Fatalf("CLI mount source = %q, want staged bridge source %q", cli.HostPath, bin.HostPath)
	}
	dir, ok := mountFor(container.spec, coordtransport.MountDir)
	if !ok || !dir.ReadOnly {
		t.Fatalf("no read-only coordination mount in %+v", container.spec.Mounts)
	}

	digest, err := hashFile(staged)
	if err != nil {
		t.Fatalf("hash source binary: %v", err)
	}
	if want := filepath.Join(binDir, bridgePrefix+digest); bin.HostPath != want {
		t.Fatalf("bridge mount source = %q, want %q", bin.HostPath, want)
	}
	info, err := os.Stat(bin.HostPath)
	if err != nil {
		t.Fatalf("stat staged bridge: %v", err)
	}
	if info.Mode().Perm() != bridgeMode.Perm() {
		t.Fatalf("staged bridge mode = %v, want %v", info.Mode().Perm(), bridgeMode.Perm())
	}
	if staged, herr := hashFile(bin.HostPath); herr != nil || staged != digest {
		t.Fatalf("staged bridge does not match its digest: %v / %q", herr, staged)
	}

	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if sc.BridgeDigest != digest || sc.BridgePath != bin.HostPath || sc.CoordDir != dir.HostPath {
		t.Fatalf("sidecar coordination reference = %+v", sc)
	}

	container.exitNow(0)
	waitStatusEvent(t, sub, run.ID, domain.RunCompleted)
	// The assets outlive the status change on purpose: they are only let go
	// once the container itself has been destroyed.
	waitFor(t, "coordination release", func() bool {
		return slices.Contains(coord.releasedRuns(), run.ID)
	})
	waitFor(t, "coordination directory removal", func() bool {
		_, err := os.Stat(dir.HostPath)
		return os.IsNotExist(err)
	})
	if _, err := os.Stat(dir.HostPath); !os.IsNotExist(err) {
		t.Fatalf("coordination directory survived the container: %v", err)
	}
	// This server's own build stays: the next launch mounts the same bytes,
	// and re-copying a binary nothing is using is pure churn.
	if _, err := os.Stat(bin.HostPath); err != nil {
		t.Fatalf("this server's staged build was collected: %v", err)
	}
}

// TestStagedBridgesAreCollectedOnlyWhenUnreferenced covers the retention
// rule and the recovery path that rebuilds it: the sidecars on disk are the
// references, so a build a surviving container still holds is kept and one
// nothing names is collected.
func TestStagedBridgesAreCollectedOnlyWhenUnreferenced(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "current build")))
	_, binDir := withCoordination(t, e)

	seam := e.sched.coordinationSeam()
	current, currentPath, err := seam.stage()
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	held := filepath.Join(binDir, bridgePrefix+"deadbeef")
	orphan := filepath.Join(binDir, bridgePrefix+"0badcafe")
	legacyOrphan := filepath.Join(binDir, legacyBridgePrefix+"feedface")
	// Dot-prefixed temp files are what a crash mid-install leaves behind;
	// collection reclaims both the current and pre-upgrade forms too.
	crashed := filepath.Join(binDir, "."+bridgePrefix+"1234abcd")
	legacyCrashed := filepath.Join(binDir, "."+legacyBridgePrefix+"5678abcd")
	unrelated := filepath.Join(binDir, "not-a-staged-bridge")
	for _, p := range []string{held, orphan, legacyOrphan, crashed, legacyCrashed} {
		if werr := os.WriteFile(p, []byte("older build"), 0o555); werr != nil {
			t.Fatalf("write %s: %v", p, werr)
		}
	}
	if werr := os.WriteFile(unrelated, []byte("leave me alone"), 0o555); werr != nil {
		t.Fatalf("write unrelated file: %v", werr)
	}
	if werr := e.sched.writeSidecar(sidecar{RunID: "run_survivor", BridgeDigest: "deadbeef"}); werr != nil {
		t.Fatalf("write sidecar: %v", werr)
	}

	e.sched.collectStagedBridges()
	for _, p := range []string{currentPath, held} {
		if _, serr := os.Stat(p); serr != nil {
			t.Fatalf("collected a referenced build %s: %v", p, serr)
		}
	}
	for _, p := range []string{orphan, legacyOrphan, crashed, legacyCrashed} {
		if _, serr := os.Stat(p); !os.IsNotExist(serr) {
			t.Fatalf("unreferenced staged asset survived: %v", serr)
		}
	}
	if _, serr := os.Stat(unrelated); serr != nil {
		t.Fatalf("collector touched unrelated file: %v", serr)
	}

	// The surviving run finishes: its reference goes, and so does the build
	// only it held.
	e.sched.removeSidecar("run_survivor")
	if _, serr := os.Stat(held); !os.IsNotExist(serr) {
		t.Fatalf("build survived the last sidecar referencing it: %v", serr)
	}
	if _, serr := os.Stat(currentPath); serr != nil {
		t.Fatalf("this server's own staged build was collected: %v", serr)
	}
	if current == "" {
		t.Fatal("stage returned an empty digest")
	}
}

// TestArgvOverrideRespectsHarnessCommand proves a Config.Harnesses override is
// respected verbatim. Registry-owned MCP registration and status reporting
// are not synthesized for an override; the mounted CLI remains the explicit
// coordination surface.
func TestArgvOverrideRespectsHarnessCommand(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"claude", "opencode"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shim := name + "-shim"
			s := &Scheduler{harnesses: map[string]HarnessSpec{
				name: {TUIArgs: []string{shim, harness.TaskPlaceholder}},
			}}
			argv, profile, _, err := s.command(t.Context(), "", "", name, domain.LaunchTUI, "add OAuth login")
			if err != nil {
				t.Fatalf("command: %v", err)
			}
			if want := []string{shim, "add OAuth login"}; !slices.Equal(argv, want) {
				t.Fatalf("argv = %v, want %v", argv, want)
			}
			if args := profile.StatusLaunchArgs(coordtransport.MountDir); len(args) != 0 {
				t.Fatalf("override kept the registry status arguments: %v", args)
			}
			if env := profile.StatusLaunchEnv(coordtransport.MountDir); len(env) != 0 {
				t.Fatalf("override kept the registry status environment: %v", env)
			}
			if profile.Reporter != harness.ReporterNone || len(profile.StatusFiles) != 0 {
				t.Fatalf("override kept reporter %s with %d status files", profile.Reporter, len(profile.StatusFiles))
			}
			if len(profile.CredentialPaths) == 0 {
				t.Fatal("override lost the registry credential paths")
			}
		})
	}
}

// TestStagingIsFailClosed proves a missing or unreadable server binary rejects
// container creation rather than launching a run that only appears
// uncoordinated.
func TestStagingIsFailClosed(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(filepath.Join(t.TempDir(), "not-a-binary")))
	withCoordination(t, e)

	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID,
		"add OAuth login", "fake", domain.LaunchTUI)
	if err == nil || !strings.Contains(err.Error(), "stage coordination CLI") {
		t.Fatalf("Launch with an unstaged server binary = %v, want staging error", err)
	}
}

// Disabling peer coordination retains authenticated transport, while mission
// admission and lifecycle reporting stay disabled.
func TestCoordinationOffRetainsRunTransport(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "current build")))
	coord, binDir := withCoordination(t, e)
	e.sched.UseCoordination(coord, binDir, false)

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir stage dir: %v", err)
	}
	old := filepath.Join(binDir, "unrelated-old-build")
	if err := os.WriteFile(old, []byte("build from a previous boot"), 0o555); err != nil {
		t.Fatalf("write old build: %v", err)
	}

	run, container := e.launchFake(t, "add OAuth login")
	cli, ok := mountFor(container.spec, coordtransport.CLIPath)
	if !ok || !cli.ReadOnly {
		t.Fatalf("coordination is off but the container lacks a read-only CLI mount: %+v", container.spec.Mounts)
	}
	if _, bridgeMounted := mountFor(container.spec, coordtransport.BinaryPath); !bridgeMounted {
		t.Fatalf("run lacks bridge mount: %+v", container.spec.Mounts)
	}
	dir, ok := mountFor(container.spec, coordtransport.MountDir)
	if !ok || !dir.ReadOnly {
		t.Fatalf("run lacks read-only identity mount: %+v", container.spec.Mounts)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("an old staged build was touched with coordination off: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if sc.CoordDir != dir.HostPath || sc.BridgeDigest == "" || sc.BridgePath != cli.HostPath {
		t.Fatalf("sidecar transport reference = %+v", sc)
	}

	if _, err := e.sched.RequireCoordination(); err == nil {
		t.Fatal("development transport enabled mission admission")
	}
	if sc.Reporter != harness.ReporterNone {
		t.Fatalf("disabled lifecycle reporter = %s", sc.Reporter)
	}
	bob := newSteerer(t, e, "Bob", "Bob Steer", "bob@example.com")
	e.sched.RecordSteer(t.Context(), run.ID, bob.ID)
	if got := coord.trailers(run.ID); !slices.Contains(got, "Co-authored-by: Bob Steer <bob@example.com>") {
		t.Fatalf("co-authors not updated through disabled coordination transport: %v", got)
	}
}

func TestDisabledCoordinationKeepsTasklessDiscoveryWithoutLifecycleHooks(t *testing.T) {
	for _, name := range []string{"claude", "codex", "pi", "omp", "opencode", "fake"} {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "taskless cli")))
			root := t.TempDir()
			coord := &recordingCoordinator{
				fakeCoordinator: fakeCoordinator{root: filepath.Join(root, "coord")},
				files:           make(map[domain.RunID]map[string][]byte),
			}
			e.sched.UseCoordination(coord, filepath.Join(root, "bin"), false)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", name, domain.LaunchTUI)
			if err != nil {
				t.Fatal(err)
			}
			if run.Task != "" {
				t.Fatalf("taskless launch gained a synthetic task: %q", run.Task)
			}
			spec := e.rt.byName(string(run.ID)).spec
			if _, ok := mountFor(spec, coordtransport.MountDir); !ok {
				t.Fatal("taskless run lost authenticated transport")
			}
			if got := e.reporterOf(t, run.ID); got != harness.ReporterNone {
				t.Fatalf("disabled lifecycle reporter = %s", got)
			}
			profile, _ := harness.Lookup(name)
			for file := range profile.StatusFiles {
				if coord.file(run.ID, file) != nil {
					t.Fatalf("disabled lifecycle asset %q was provisioned", file)
				}
			}
			if name == "fake" {
				if len(coord.files[run.ID]) != 0 {
					t.Fatal("custom taskless harness received invented discovery assets")
				}
				return
			}
			// Native discovery is still launch-scoped; no fake user task or
			// lifecycle callback may be substituted to get the hint delivered.
			for _, arg := range profile.DiscoveryLaunchArgs(coordtransport.MountDir) {
				if !slices.Contains(spec.Command, arg) {
					t.Fatalf("native discovery argument %q missing from %v", arg, spec.Command)
				}
			}
		})
	}
}

func TestOpenCodeDiscoveryReferencesOnlyProvisionedAssets(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "identity-only", true: "with-reporter"}[enabled], func(t *testing.T) {
			e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "opencode discovery")))
			root := t.TempDir()
			coord := &recordingCoordinator{
				fakeCoordinator: fakeCoordinator{root: filepath.Join(root, "coord")},
				files:           make(map[domain.RunID]map[string][]byte),
			}
			e.sched.UseCoordination(coord, filepath.Join(root, "bin"), enabled)
			run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "", "opencode", domain.LaunchTUI)
			if err != nil {
				t.Fatal(err)
			}
			spec := e.rt.byName(string(run.ID)).spec
			var config struct {
				Plugins      []string `json:"plugin"`
				Instructions []string `json:"instructions"`
				Permission   string   `json:"permission"`
			}
			if err := json.Unmarshal([]byte(spec.Env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
				t.Fatal(err)
			}
			if config.Permission != "allow" {
				t.Fatalf("discovery and status overlays dropped the permission setting: %q", config.Permission)
			}
			if len(config.Instructions) != 1 {
				t.Fatalf("discovery instruction files = %v", config.Instructions)
			}
			wantPlugins := 0
			if enabled {
				wantPlugins = 1
			}
			if len(config.Plugins) != wantPlugins {
				t.Fatalf("status plugins = %v with policy enabled=%v", config.Plugins, enabled)
			}
			for _, path := range append(config.Instructions, config.Plugins...) {
				path = strings.TrimPrefix(path, "file://")
				if filepath.Dir(path) != coordtransport.MountDir || coord.file(run.ID, filepath.Base(path)) == nil {
					t.Fatalf("launch references unprovisioned asset %q", path)
				}
			}
		})
	}
}

func TestPrepareVerificationRuntimeReferenceLifetime(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "verification cli")))
	coord, binDir := withCoordination(t, e)
	key := "verification-lifetime-key"
	spec := runtime.Spec{
		Image: "busybox:1.36", Command: []string{"true"},
		User: "1000:1000", CreationKey: key,
	}
	release, err := e.sched.PrepareVerificationRuntime(t.Context(), &spec)
	if err != nil {
		t.Fatalf("PrepareVerificationRuntime: %v", err)
	}
	defer release()
	cli, ok := mountFor(spec, coordtransport.CLIPath)
	if !ok || !cli.ReadOnly {
		t.Fatalf("verification CLI mount = %+v, want read-only mount", cli)
	}
	if _, ok := mountFor(spec, coordtransport.BinaryPath); ok {
		t.Fatal("verification runtime received the coordination bridge mount")
	}
	if _, ok := mountFor(spec, coordtransport.MountDir); ok {
		t.Fatal("verification runtime received the coordination socket mount")
	}
	if spec.User != "1000:1000" {
		t.Fatalf("verification runtime user = %q, want non-root user preserved", spec.User)
	}
	if !strings.Contains(":"+spec.Env["PATH"]+":", ":"+filepath.Dir(coordtransport.CLIPath)+":") {
		t.Fatalf("verification PATH = %q, missing staged CLI directory", spec.Env["PATH"])
	}
	if _, ok := spec.Env["AETHER_RUN_ID"]; ok {
		t.Fatalf("verification runtime gained a run identity: %#v", spec.Env)
	}
	ref, err := e.sched.readVerificationBridgeRef(key)
	if err != nil {
		t.Fatalf("read durable verification reference: %v", err)
	}
	if ref.BridgePath != cli.HostPath {
		t.Fatalf("reference bridge path = %q, want %q", ref.BridgePath, cli.HostPath)
	}
	var sawReferenceAtCreate bool
	e.rt.createHook = func() {
		_, sawErr := e.sched.readVerificationBridgeRef(key)
		sawReferenceAtCreate = sawErr == nil
	}
	containerID, err := e.rt.Create(t.Context(), spec)
	if err != nil {
		t.Fatalf("fake runtime Create: %v", err)
	}
	release()
	if !sawReferenceAtCreate {
		t.Fatal("verification bridge reference was not durable before Runtime.Create")
	}
	if err := e.rt.Destroy(t.Context(), containerID); err != nil {
		t.Fatalf("destroy fake verification runtime: %v", err)
	}
	// The durable reference remains live until explicit cleanup release, even
	// after a scheduler restart with no in-memory current digest.
	reboot := e.newScheduler(t, e.rt, newFakePTY())
	reboot.UseCoordination(coord, binDir)
	reboot.collectStagedBridges()
	if _, err := os.Stat(cli.HostPath); err != nil {
		t.Fatalf("reference-protected collection removed staged CLI: %v", err)
	}
	if _, err := reboot.readVerificationBridgeRef(key); err != nil {
		t.Fatalf("verification reference did not survive restart: %v", err)
	}
	if err := reboot.ReleaseVerificationRuntime(t.Context(), key); err != nil {
		t.Fatalf("ReleaseVerificationRuntime: %v", err)
	}
	if _, err := os.Stat(reboot.verificationBridgeRefPath(key)); !os.IsNotExist(err) {
		t.Fatalf("verification reference survived release: %v", err)
	}
	if _, err := os.Stat(cli.HostPath); !os.IsNotExist(err) {
		t.Fatalf("released staged CLI survived collection: %v", err)
	}
}

func TestPrepareVerificationRuntimeDisabledCoordinationKeepsNonRootCLI(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "verification cli")))
	coord, binDir := withCoordination(t, e)
	e.sched.UseCoordination(coord, binDir, false)
	key := "verification-disabled-key"
	spec := runtime.Spec{
		Image: "busybox:1.36", Command: []string{"true"},
		Env:  map[string]string{"PATH": "/usr/bin"},
		User: "1000:1000", CreationKey: key,
	}
	release, err := e.sched.PrepareVerificationRuntime(t.Context(), &spec)
	if err != nil {
		t.Fatalf("PrepareVerificationRuntime with coordination disabled: %v", err)
	}
	defer release()
	if spec.User != "1000:1000" {
		t.Fatalf("verification runtime user = %q, want non-root user", spec.User)
	}
	cli, ok := mountFor(spec, coordtransport.CLIPath)
	if !ok || !cli.ReadOnly {
		t.Fatalf("disabled coordination CLI mount = %+v, want read-only mount", cli)
	}
	if _, ok := mountFor(spec, coordtransport.BinaryPath); ok {
		t.Fatal("disabled coordination mounted the bridge binary")
	}
	if _, ok := mountFor(spec, coordtransport.MountDir); ok {
		t.Fatal("disabled coordination mounted a run socket directory")
	}
	if !strings.Contains(":"+spec.Env["PATH"]+":", ":"+filepath.Dir(coordtransport.CLIPath)+":") {
		t.Fatalf("disabled coordination PATH = %q, missing staged CLI directory", spec.Env["PATH"])
	}
	if err := e.sched.ReleaseVerificationRuntime(t.Context(), key); err != nil {
		t.Fatalf("ReleaseVerificationRuntime: %v", err)
	}
}

func TestPrepareVerificationRuntimeFailsClosedWhenStagingFails(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(filepath.Join(t.TempDir(), "missing-server")))
	withCoordination(t, e)
	key := "verification-staging-failure-key"

	spec := runtime.Spec{
		Image: "busybox:1.36", Command: []string{"true"},
		CreationKey: key,
	}
	_, err := e.sched.PrepareVerificationRuntime(t.Context(), &spec)
	if err == nil || !strings.Contains(err.Error(), "stage verification coordination CLI") {
		t.Fatalf("PrepareVerificationRuntime error = %v, want staging refusal", err)
	}
	if len(spec.Mounts) != 0 {
		t.Fatalf("failed preparation mutated mounts: %+v", spec.Mounts)
	}
	if _, err := os.Stat(e.sched.verificationBridgeRefPath(key)); !os.IsNotExist(err) {
		t.Fatalf("failed preparation left a durable reference: %v", err)
	}
}

func TestVerificationBridgeCollectionRetainsMalformedPublishedReference(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "verification cli")))
	withCoordination(t, e)
	seam := e.sched.coordinationSeam()
	_, stagedPath, err := seam.stage()
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	seam.mu.Lock()
	seam.staged = ""
	seam.mu.Unlock()
	if mkdirErr := os.MkdirAll(e.sched.verificationBridgeRefDir(), 0o755); mkdirErr != nil {
		t.Fatalf("create verification reference dir: %v", mkdirErr)
	}
	finalRef := e.sched.verificationBridgeRefPath("unknown")
	if writeErr := os.WriteFile(finalRef, []byte(`{"creation_key":"unknown"`), 0o600); writeErr != nil {
		t.Fatalf("write malformed published verification reference: %v", writeErr)
	}
	e.sched.collectStagedBridges()
	if _, statErr := os.Stat(stagedPath); statErr != nil {
		t.Fatalf("unknown verification cleanup outcome reclaimed %s: %v", stagedPath, statErr)
	}
}

func TestVerificationCacheOwnerSurvivesCreationGapAndFailedCleanupRestart(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("non-root cache ownership requires Linux")
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1001
	}
	user := fmt.Sprintf("%d:%d", uid, gid)
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "verification cache")))
	coord, binDir := withCoordination(t, e)
	inspected := &cacheImageRuntime{fakeRuntime: e.rt, user: user}
	e.sched.cfg.Runtime = inspected
	plan, err := e.sched.BuildEnvironmentPlan(t.Context(), &domain.Run{Worktree: t.TempDir()}, e.ws, e.member, harness.Profile{}, EnvironmentPurposeRun)
	if err != nil {
		t.Fatal(err)
	}
	if plan.User != user {
		t.Fatalf("resolved verification user = %q, want %q", plan.User, user)
	}
	spec := runtime.Spec{Image: plan.Image, Env: plan.Env, Mounts: plan.Mounts, User: plan.User, CreationKey: "verification-cache-key", Command: []string{"true"}}
	replay := spec
	cache, ok := mountFor(spec, "/aether-cache")
	if !ok {
		t.Fatal("environment omitted runs cache")
	}
	// Planning is not publication: GC may reclaim this unowned pool. Preparing
	// must recreate it before a runtime can receive the mount.
	e.sched.sweepCaches(t.Context(), true)
	requireCacheExists(t, cache.HostPath, false)
	release, err := e.sched.PrepareVerificationRuntime(t.Context(), &spec)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	replayRelease, err := e.sched.PrepareVerificationRuntime(t.Context(), &replay)
	if err != nil {
		t.Fatalf("same-key preparation replay: %v", err)
	}
	replayRelease()
	requireCacheExists(t, cache.HostPath, true)
	cacheInfo, err := os.Stat(cache.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	stat := cacheInfo.Sys().(*syscall.Stat_t)
	if stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || cacheInfo.Mode().Perm() != 0o700 {
		t.Fatalf("cache ownership/mode = %d:%d %o, want %s 0700", stat.Uid, stat.Gid, cacheInfo.Mode().Perm(), user)
	}
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, ".npm", "_cacache")
	if mkdirErr := os.MkdirAll(legacy, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	assertProtected := func(s *Scheduler) {
		t.Helper()
		s.sweepCaches(t.Context(), true)
		requireCacheExists(t, cache.HostPath, true)
		requireCacheExists(t, legacy, true)
		info, cacheErr := s.cfg.Homes.ReadCache(e.member.ID, memberhome.CachePoolRuns)
		if cacheErr != nil || !slices.Contains(info.Owners, spec.CreationKey) {
			t.Fatalf("missing durable verification owner: %+v, %v", info, cacheErr)
		}
		ref, refErr := s.readVerificationBridgeRef(spec.CreationKey)
		if refErr != nil || ref.CacheMember != e.member.ID || ref.CachePool != memberhome.CachePoolRuns || ref.CacheUser != user {
			t.Fatalf("missing cleanup attribution: %+v, %v", ref, refErr)
		}
		s.mu.Lock()
		s.syncRunUserReservationsLocked()
		conflictErr := s.reservationConflictLocked(e.member.ID, "", "424242:424243", "conflicting live user")
		count := len(s.credentialUsers)
		s.mu.Unlock()
		if conflictErr == nil || count != 1 {
			t.Fatalf("verification lost exclusive user reservation: count=%d conflict=%v", count, conflictErr)
		}
	}
	// The key is not yet discoverable by the runtime; GC must not infer absence.
	assertProtected(e.sched)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if releaseErr := e.sched.ReleaseVerificationRuntime(cancelled, spec.CreationKey); !errors.Is(releaseErr, context.Canceled) {
		t.Fatalf("cancelled cleanup = %v", releaseErr)
	}
	assertProtected(e.sched)
	cid, err := e.rt.Create(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	release()
	failed := &destroyFailureRuntime{Runtime: e.rt, destroyErr: errors.New("destroy unavailable")}
	if destroyErr := failed.Destroy(t.Context(), cid); destroyErr == nil {
		t.Fatal("expected cleanup failure")
	}
	reboot := e.newScheduler(t, e.rt, newFakePTY())
	reboot.cfg.Runtime = inspected
	reboot.UseCoordination(coord, binDir)
	assertProtected(reboot)
	found, err := e.rt.FindByCreationKey(t.Context(), spec.CreationKey)
	if err != nil || found != cid {
		t.Fatalf("restart lost runtime cleanup key: %q, %v", found, err)
	}
	if destroyErr := e.rt.Destroy(t.Context(), found); destroyErr != nil {
		t.Fatal(destroyErr)
	}
	before, err := reboot.cfg.Homes.ReadCache(e.member.ID, memberhome.CachePoolRuns)
	if err != nil {
		t.Fatal(err)
	}
	other := runtime.Spec{Image: plan.Image, Env: plan.Env, Mounts: plan.Mounts, User: plan.User, CreationKey: "another-verification", Command: []string{"true"}}
	otherRelease, err := reboot.PrepareVerificationRuntime(t.Context(), &other)
	if err != nil {
		t.Fatal(err)
	}
	otherRelease()
	if releaseErr := reboot.ReleaseVerificationRuntime(t.Context(), spec.CreationKey); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	after, err := reboot.cfg.Homes.ReadCache(e.member.ID, memberhome.CachePoolRuns)
	if err != nil || slices.Contains(after.Owners, spec.CreationKey) || after.LastUsed.Before(before.LastUsed) {
		t.Fatalf("owner release did not preserve last-use activity: %+v, %v", after, err)
	}
	if !slices.Contains(after.Owners, "another-verification") {
		t.Fatal("release removed another verification's owner")
	}
	if idempotentReleaseErr := reboot.ReleaseVerificationRuntime(t.Context(), spec.CreationKey); idempotentReleaseErr != nil {
		t.Fatalf("idempotent release: %v", idempotentReleaseErr)
	}
	reboot.mu.Lock()
	remaining := len(reboot.credentialUsers)
	reboot.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("release removed another verification's user reservation: %d", remaining)
	}
	if releaseErr := reboot.ReleaseVerificationRuntime(t.Context(), other.CreationKey); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	reboot.mu.Lock()
	remaining = len(reboot.credentialUsers)
	reboot.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("completed cleanup leaked %d user reservations", remaining)
	}
	reboot.sweepCaches(t.Context(), true)
	requireCacheExists(t, cache.HostPath, false)
	requireCacheExists(t, legacy, false)
}

func TestVerificationCacheRefusesConflictingLiveUserWithoutChown(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "verification conflict")))
	withCoordination(t, e)
	plan, err := e.sched.BuildEnvironmentPlan(t.Context(), &domain.Run{}, e.ws, e.member, harness.Profile{}, EnvironmentPurposeRun)
	if err != nil {
		t.Fatal(err)
	}
	spec := runtime.Spec{Image: plan.Image, Mounts: plan.Mounts, User: "1000:1001", CreationKey: "verification-conflict"}
	cache, ok := mountFor(spec, "/aether-cache")
	if !ok {
		t.Fatal("environment omitted runs cache")
	}
	marker := filepath.Join(cache.HostPath, "live-owner-data")
	if writeErr := os.WriteFile(marker, []byte("keep live cache"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	before, err := os.Stat(cache.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	live := &supervised{runID: "other-live-user", memberID: e.member.ID, runUser: "2000:2001"}
	e.sched.mu.Lock()
	e.sched.runs[live.runID] = live
	e.sched.mu.Unlock()
	release, err := e.sched.PrepareVerificationRuntime(t.Context(), &spec)
	if err == nil || !strings.Contains(err.Error(), "reserved by live run") || release != nil {
		t.Fatalf("conflicting preparation = %v, want reservation refusal", err)
	}
	after, err := os.Stat(cache.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	a, b := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if a.Uid != b.Uid || a.Gid != b.Gid || before.Mode() != after.Mode() {
		t.Fatalf("refused verification changed live cache ownership: %d:%d -> %d:%d", a.Uid, a.Gid, b.Uid, b.Gid)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "keep live cache" {
		t.Fatalf("refused verification changed live cache data: %q, %v", data, readErr)
	}
	if _, refErr := e.sched.readVerificationBridgeRef(spec.CreationKey); !errors.Is(refErr, os.ErrNotExist) {
		t.Fatalf("conflicting verification published a journal: %v", refErr)
	}
	e.sched.mu.Lock()
	reservations := len(e.sched.credentialUsers)
	e.sched.mu.Unlock()
	if reservations != 1 || e.sched.capacityReservations != 0 {
		t.Fatalf("conflicting preparation leaked reservations: users=%d capacity=%d", reservations, e.sched.capacityReservations)
	}
}

func TestVerificationFailedCachePreparationReleasesUser(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "verification cache failure")))
	withCoordination(t, e)
	plan, err := e.sched.BuildEnvironmentPlan(t.Context(), &domain.Run{}, e.ws, e.member, harness.Profile{}, EnvironmentPurposeRun)
	if err != nil {
		t.Fatal(err)
	}
	spec := runtime.Spec{Image: plan.Image, Mounts: plan.Mounts, User: "1000:1001", CreationKey: "verification-cache-failure"}
	cache, ok := mountFor(spec, "/aether-cache")
	if !ok {
		t.Fatal("environment omitted runs cache")
	}
	if removeErr := os.Remove(cache.HostPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	if writeErr := os.WriteFile(cache.HostPath, []byte("not a directory"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if release, prepareErr := e.sched.PrepareVerificationRuntime(t.Context(), &spec); prepareErr == nil || release != nil {
		t.Fatalf("invalid cache preparation = %v, want refusal", prepareErr)
	}
	e.sched.mu.Lock()
	reservations := len(e.sched.credentialUsers)
	e.sched.mu.Unlock()
	if reservations != 0 || e.sched.capacityReservations != 0 {
		t.Fatalf("failed preparation leaked reservations: users=%d capacity=%d", reservations, e.sched.capacityReservations)
	}
	if _, refErr := e.sched.readVerificationBridgeRef(spec.CreationKey); !errors.Is(refErr, os.ErrNotExist) {
		t.Fatalf("failed cache preparation published a journal: %v", refErr)
	}
	if releaseErr := e.sched.ReleaseVerificationRuntime(t.Context(), spec.CreationKey); releaseErr != nil {
		t.Fatal(releaseErr)
	}
}

func TestVerificationUserRecoveryRefusesUnknownJournal(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if err := os.MkdirAll(e.sched.verificationBridgeRefDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.sched.verificationBridgeRefPath("malformed-final"), []byte(`{"cache_user":`), 0o600); err != nil {
		t.Fatal(err)
	}
	reboot, err := New(e.cfg)
	if err == nil {
		_ = reboot.Close()
		t.Fatal("scheduler admitted ownership changes with unknown verification user")
	}
	if !strings.Contains(err.Error(), "restore verification users") {
		t.Fatalf("unexpected recovery refusal: %v", err)
	}
}

func TestVerificationRecoveryIgnoresUnpublishedTemporary(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "verification recovery")))
	coord, binDir := withCoordination(t, e)
	seam := e.sched.coordinationSeam()
	digest, stagedPath, err := seam.stage()
	if err != nil {
		t.Fatal(err)
	}
	seam.mu.Lock()
	seam.staged = ""
	seam.mu.Unlock()
	ref := verificationBridgeRef{
		CreationKey: "published-verification", BridgeDigest: digest, BridgePath: stagedPath,
		CacheMember: e.member.ID, CachePool: memberhome.CachePoolRuns, CacheUser: "1000:1001",
	}
	if writeErr := e.sched.writeVerificationBridgeRef(ref); writeErr != nil {
		t.Fatal(writeErr)
	}
	// A crash during the next atomic write cannot have created that runtime.
	// It must neither block startup nor hide the prior published owner.
	tempRef := filepath.Join(e.sched.verificationBridgeRefDir(), ".aether-verification-crash")
	if writeErr := os.WriteFile(tempRef, []byte(`{"cache_user":`), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	reboot := e.newScheduler(t, e.rt, newFakePTY())
	reboot.UseCoordination(coord, binDir)
	reboot.mu.Lock()
	count := len(reboot.credentialUsers)
	var restored *credentialUserReservation
	for reservation := range reboot.credentialUsers {
		restored = reservation
	}
	conflictErr := reboot.reservationConflictLocked(e.member.ID, "", "2000:2001", "later run")
	reboot.mu.Unlock()
	if count != 1 || restored == nil || restored.verificationKey != ref.CreationKey ||
		restored.home != ref.CacheMember || restored.user != ref.CacheUser || conflictErr == nil {
		t.Fatalf("published verification reservation not restored: count=%d reservation=%+v conflict=%v", count, restored, conflictErr)
	}
	reboot.collectStagedBridges()
	if _, statErr := os.Stat(stagedPath); statErr != nil {
		t.Fatalf("published reference lost its staged CLI: %v", statErr)
	}
	// With the published owner released, the incomplete temporary cannot
	// manufacture another credential reservation or retain staged bytes.
	if releaseErr := reboot.ReleaseVerificationRuntime(t.Context(), ref.CreationKey); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if _, statErr := os.Stat(stagedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unpublished temporary retained unowned staged CLI: %v", statErr)
	}
	reboot.mu.Lock()
	count = len(reboot.credentialUsers)
	reboot.mu.Unlock()
	if count != 0 {
		t.Fatalf("unpublished temporary manufactured %d reservations", count)
	}
}

func TestVerificationReleaseAfterJournalUnlinkKeepsOtherUserReservations(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	// Model a cleanup that removed the pool owner and unlinked its journal,
	// then failed the directory fsync. Retrying must release only that key.
	for _, key := range []string{"unlinked-verification", "other-verification"} {
		ref := verificationBridgeRef{CreationKey: key, CacheMember: e.member.ID, CacheUser: "1000:1001"}
		if _, _, err := e.sched.reserveVerificationUser(ref); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.sched.ReleaseVerificationRuntime(t.Context(), "unlinked-verification"); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	count := len(e.sched.credentialUsers)
	var remaining string
	for reservation := range e.sched.credentialUsers {
		remaining = reservation.verificationKey
	}
	e.sched.mu.Unlock()
	if count != 1 || remaining != "other-verification" {
		t.Fatalf("cleanup released wrong user reservations: count=%d remaining=%q", count, remaining)
	}
	if err := e.sched.ReleaseVerificationRuntime(t.Context(), remaining); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	count = len(e.sched.credentialUsers)
	e.sched.mu.Unlock()
	if count != 0 {
		t.Fatalf("cleanup retry leaked %d user reservations", count)
	}
}

func TestVerificationAdmissionDefaultsOverridesAndAllowanceRelease(t *testing.T) {
	e, rt := newAdmissionEnv(t, withServerBinary(fakeServerBinary(t, "verification admission")))
	withCoordination(t, e)
	host := healthyAdmissionHost()
	host.MemoryAvailableBytes = 1
	rt.setHost(host)
	spec := runtime.Spec{Image: "busybox", Command: []string{"true"}, CreationKey: "verification-pressure"}
	if release, err := e.sched.PrepareVerificationRuntime(t.Context(), &spec); !errors.Is(err, ErrMemoryPressure) || release != nil {
		t.Fatalf("memory pressure was not refused before publication: %v", err)
	}
	if _, err := os.Stat(e.sched.verificationBridgeRefPath(spec.CreationKey)); !os.IsNotExist(err) {
		t.Fatalf("refused provisioning published runtime ownership: %v", err)
	}
	rt.setHost(healthyAdmissionHost())
	release, err := e.sched.PrepareVerificationRuntime(t.Context(), &spec)
	if err != nil {
		t.Fatal(err)
	}
	if spec.CPULimit != e.sched.cfg.RunCPULimit || spec.MemoryLimitBytes != e.sched.cfg.RunMemoryBytes || spec.PidsLimit != e.sched.cfg.RunPidsLimit {
		t.Fatalf("verification bypassed resolved budgets: %+v", spec)
	}
	if e.sched.capacityReservations != 1 {
		t.Fatal("preparation did not reserve provisioning capacity")
	}
	release()
	release()
	if e.sched.capacityReservations != 0 {
		t.Fatal("allowance release leaked or underflowed")
	}
	trusted := runtime.Spec{Image: "busybox", Command: []string{"true"}, CreationKey: "verification-trusted", CPULimit: 0.5, MemoryLimitBytes: 256 << 20, PidsLimit: 64}
	release, err = e.sched.PrepareVerificationRuntime(t.Context(), &trusted)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if trusted.CPULimit != 0.5 || trusted.MemoryLimitBytes != 256<<20 || trusted.PidsLimit != 64 {
		t.Fatalf("trusted overrides changed: %+v", trusted)
	}
	bad := runtime.Spec{Image: "busybox", Command: []string{"true"}, CreationKey: "verification-bad-mount", Mounts: []runtime.Mount{{ContainerPath: coordtransport.CLIPath}}}
	if badRelease, prepareErr := e.sched.PrepareVerificationRuntime(t.Context(), &bad); prepareErr == nil || badRelease != nil {
		t.Fatalf("invalid preparation accepted: %v", prepareErr)
	}
	if e.sched.capacityReservations != 0 {
		t.Fatal("failed preparation stranded later jobs")
	}
	later, err := e.sched.reserveCapacity(t.Context())
	if err != nil {
		t.Fatalf("later provisioning stranded: %v", err)
	}
	later()
}
