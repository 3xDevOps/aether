package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/mcpbridge"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// fakeCoordinator stands in for *coord.Service: it owns a directory per run
// and records what was released.
type fakeCoordinator struct {
	root string
	err  error

	mu       sync.Mutex
	released []domain.RunID
}

func (f *fakeCoordinator) Provision(_ context.Context, run domain.RunID, _ []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	dir := filepath.Join(f.root, string(run))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
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

// fakeServerBinary points the stager at a stand-in for /proc/self/exe.
func fakeServerBinary(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aether-server")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake server binary: %v", err)
	}
	old := selfExe
	selfExe = path
	t.Cleanup(func() { selfExe = old })
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
	fakeServerBinary(t, "#!/bin/sh\necho aether\n")
	e := newTestEnv(t, nil)
	coord, binDir := withCoordination(t, e)
	sub := e.subscribe(t)

	run, container := e.launchFake(t, "add OAuth login")

	bin, ok := mountFor(container.spec, mcpbridge.BinaryPath)
	if !ok || !bin.ReadOnly {
		t.Fatalf("no read-only bridge mount in %+v", container.spec.Mounts)
	}
	dir, ok := mountFor(container.spec, mcpbridge.MountDir)
	if !ok || !dir.ReadOnly {
		t.Fatalf("no read-only coordination mount in %+v", container.spec.Mounts)
	}

	digest, err := hashFile(selfExe)
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
	waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	// The assets outlive the status change on purpose: they are only let go
	// once the container itself has been destroyed.
	waitFor(t, "coordination release", func() bool {
		return slices.Contains(coord.releasedRuns(), run.ID)
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
	fakeServerBinary(t, "current build")
	e := newTestEnv(t, nil)
	_, binDir := withCoordination(t, e)

	seam := e.sched.coordinationSeam()
	current, currentPath, err := seam.stage()
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	held := filepath.Join(binDir, bridgePrefix+"deadbeef")
	orphan := filepath.Join(binDir, bridgePrefix+"0badcafe")
	// A dot-prefixed temp file is what a crash mid-install leaves behind;
	// no sidecar can ever reference it, so collection reclaims it too.
	crashed := filepath.Join(binDir, "."+bridgePrefix+"1234abcd")
	for _, p := range []string{held, orphan, crashed} {
		if werr := os.WriteFile(p, []byte("older build"), 0o555); werr != nil {
			t.Fatalf("write %s: %v", p, werr)
		}
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
	if _, serr := os.Stat(orphan); !os.IsNotExist(serr) {
		t.Fatalf("unreferenced build survived: %v", serr)
	}
	if _, serr := os.Stat(crashed); !os.IsNotExist(serr) {
		t.Fatalf("crashed install's temp file survived: %v", serr)
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

// TestArgvOverrideDropsMCPRegistration: a Config.Harnesses override is
// respected verbatim. The registry's MCP flag belongs to the CLI the
// registry ships, so an overridden harness gets no MCP args appended and
// degrades to notice-only coordination, while the rest of the registry
// profile still applies.
func TestArgvOverrideDropsMCPRegistration(t *testing.T) {
	s := &Scheduler{harnesses: map[string]HarnessSpec{
		"claude": {TUIArgs: []string{"claude-shim", harness.TaskPlaceholder}},
	}}
	argv, profile, err := s.command(t.Context(), "", "claude", domain.LaunchTUI, "add OAuth login")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if want := []string{"claude-shim", "add OAuth login"}; !slices.Equal(argv, want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	if args := profile.MCPArgs("/run/aether/mcp.json"); len(args) != 0 {
		t.Fatalf("override kept the registry MCP registration: %v", args)
	}
	if len(profile.CredentialPaths) == 0 {
		t.Fatal("override lost the registry credential paths")
	}
}

// TestStagingIsFailClosed proves the mount is never a guess: a bridge that
// cannot be staged and verified is simply not mounted, the run launches
// without coordination, and the reason is on the run's timeline rather than
// in a log nobody reads.
func TestStagingIsFailClosed(t *testing.T) {
	fakeServerBinary(t, "unused")
	selfExe = filepath.Join(t.TempDir(), "not-a-binary")
	e := newTestEnv(t, nil)
	withCoordination(t, e)
	sub := e.subscribe(t)

	run, container := e.launchFake(t, "add OAuth login")
	if _, ok := mountFor(container.spec, mcpbridge.BinaryPath); ok {
		t.Fatal("a bridge that could not be staged was mounted anyway")
	}
	if _, ok := mountFor(container.spec, mcpbridge.MountDir); ok {
		t.Fatal("coordination was mounted without a bridge to serve it")
	}
	note := waitTimelineEvent(t, sub, run.ID, events.TimelineNote)
	if msg := note.Payload.(events.TimelinePayload).Message; !strings.Contains(msg, "coordination unavailable") {
		t.Fatalf("timeline note = %q", msg)
	}
}

// TestCoordinationOffLeavesContainersAlone covers both directions of the
// kill switch: with it off nothing is staged, mounted, or collected, and
// turning it on afterwards does not pretend a container that was created
// without the mounts has them.
func TestCoordinationOffLeavesContainersAlone(t *testing.T) {
	fakeServerBinary(t, "current build")
	e := newTestEnv(t, nil)

	binDir := filepath.Join(t.TempDir(), "runtime", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	old := filepath.Join(binDir, bridgePrefix+"deadbeef")
	if err := os.WriteFile(old, []byte("build from a previous boot"), 0o555); err != nil {
		t.Fatalf("write old build: %v", err)
	}

	run, container := e.launchFake(t, "add OAuth login")
	if len(container.spec.Mounts) != 0 {
		t.Fatalf("coordination is off but the container got mounts: %+v", container.spec.Mounts)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("an old staged build was touched with coordination off: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if sc.CoordDir != "" || sc.BridgeDigest != "" {
		t.Fatalf("sidecar claims coordination assets: %+v", sc)
	}

	// Off -> on. The container already exists; nothing retrofits it.
	coord, _ := withCoordination(t, e)
	if len(container.spec.Mounts) != 0 {
		t.Fatalf("an existing container gained mounts: %+v", container.spec.Mounts)
	}
	if released := coord.releasedRuns(); len(released) != 0 {
		t.Fatalf("a run that was never provisioned was released: %v", released)
	}
}
