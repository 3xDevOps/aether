package scheduler

import (
	"context"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/runtime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
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
	released       []domain.RunID
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
			argv, profile, err := s.command(t.Context(), "", name, domain.LaunchTUI, "add OAuth login")
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

// TestCoordinationOffLeavesContainersAlone covers both directions of the
// kill switch: with it off a run gets the version-matched CLI but no socket
// or coordination directory, and turning it on afterwards does not retrofit
// the already-created container.
func TestCoordinationOffLeavesContainersAlone(t *testing.T) {
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
	if _, ok := mountFor(container.spec, coordtransport.BinaryPath); ok {
		t.Fatalf("coordination is off but the container got a bridge mount: %+v", container.spec.Mounts)
	}
	if _, ok := mountFor(container.spec, coordtransport.MountDir); ok {
		t.Fatalf("coordination is off but the container got a socket mount: %+v", container.spec.Mounts)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("an old staged build was touched with coordination off: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if sc.CoordDir != "" || sc.BridgeDigest == "" || sc.BridgePath != cli.HostPath {
		t.Fatalf("sidecar coordination reference = %+v, want CLI only", sc)
	}

	// Off -> on. The container already exists; nothing retrofits it.
	e.sched.UseCoordination(coord, binDir, true)
	if _, ok := mountFor(container.spec, coordtransport.BinaryPath); ok {
		t.Fatalf("an existing container gained a bridge mount")
	}
	if _, ok := mountFor(container.spec, coordtransport.MountDir); ok {
		t.Fatalf("an existing container gained a socket mount")
	}
	if released := coord.releasedRuns(); len(released) != 0 {
		t.Fatalf("a run that was never provisioned was released: %v", released)
	}
}
