package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/coordhooks"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
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

	// remind answers RemindReport; nil refuses every report reminder.
	remind func(context.Context, domain.RunID, string) error

	mu             sync.Mutex
	onWake         func(domain.RunID)
	released       []domain.RunID
	idleWakes      []domain.RunID
	provisional    []provisionalCall
	opened         []domain.RunID
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

func (f *fakeCoordinator) EnhancedSessionOpened(run domain.RunID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, run)
}

func (f *fakeCoordinator) RemindReport(ctx context.Context, run domain.RunID, prompt string) error {
	if f.remind == nil {
		return errors.New("fakeCoordinator: no waker")
	}
	return f.remind(ctx, run, prompt)
}

func (f *fakeCoordinator) ProvisionalReport(_ context.Context, run domain.RunID, since time.Time, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisional = append(f.provisional, provisionalCall{run: run, since: since, message: message})
	return nil
}

type provisionalCall struct {
	run     domain.RunID
	since   time.Time
	message string
}

func (f *fakeCoordinator) provisionalReports() []provisionalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.provisional)
}

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

// TestStagingRereadsOnlyAChangedCopy proves staging opens neither the server
// binary nor its staged copy while that copy is unchanged, and verifies and
// replaces a copy that changed.
func TestStagingRereadsOnlyAChangedCopy(t *testing.T) {
	t.Parallel()
	self := fakeServerBinary(t, "current build")
	e := newTestEnv(t, withServerBinary(self))
	withCoordination(t, e)
	seam := e.sched.coordinationSeam()
	_, path, err := seam.stage()
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	opens, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Fatalf("inotify: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(opens) })
	for _, p := range []string{self, path} {
		if _, err = syscall.InotifyAddWatch(opens, p, syscall.IN_OPEN); err != nil {
			t.Fatalf("watch %s: %v", p, err)
		}
	}
	var again string
	if _, again, err = seam.stage(); err != nil || again != path {
		t.Fatalf("second stage = %q, %v; want %q", again, err, path)
	}
	if n, readErr := syscall.Read(opens, make([]byte, 4096)); !errors.Is(readErr, syscall.EAGAIN) {
		t.Fatalf("second stage opened a binary: %d bytes of inotify events, error %v", n, readErr)
	}

	if err = os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, bridgeMode); err != nil {
		t.Fatal(err)
	}
	if _, again, err = seam.stage(); err != nil || again != path {
		t.Fatalf("stage after a change = %q, %v; want %q", again, err, path)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "current build" {
		t.Fatalf("restaged copy = %q, %v; want the server binary", got, readErr)
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
			argv, profile, _, err := s.command(t.Context(), "", "", "", name, domain.LaunchTUI, "add OAuth login", "", "")
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
			if profile.Capabilities.Status != harness.ReporterNone || len(profile.StatusFiles) != 0 {
				t.Fatalf("override kept reporter %s with %d status files", profile.Capabilities.Status, len(profile.StatusFiles))
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

// Disabling peer coordination retains authenticated transport, while
// lifecycle reporting stays disabled.
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
			// Instructions are still launch-scoped; no fake user task or
			// lifecycle callback may be substituted to get them delivered.
			card := e.sched.runInstructions(coordhooks.Guide{RunID: string(run.ID)})
			if strings.Contains(card, "--outcome") {
				t.Fatalf("instructions without lifecycle reporting carry the report rule: %q", card)
			}
			for _, arg := range profile.InstructionLaunchArgs(coordtransport.MountDir, card) {
				if !slices.Contains(spec.Command, arg) {
					t.Fatalf("instruction argument %q missing from %v", arg, spec.Command)
				}
			}
		})
	}
}

// Every launch that does not speak ACP carries the role card: in context
// the agent keeps through compaction where it has a place for it, else
// after the task.
func TestEveryLaunchCarriesTheRoleCard(t *testing.T) {
	for _, name := range []string{"claude", "codex", "pi", "omp", "opencode", "copilot", "gemini", "cursor", "antigravity", "fake"} {
		for _, tc := range []struct {
			mode domain.LaunchMode
			task string
		}{{domain.LaunchTUI, ""}, {domain.LaunchTUI, "fix the login"}, {domain.LaunchHeadless, "fix the login"}} {
			t.Run(name+"/"+string(tc.mode)+"/"+map[bool]string{false: "taskless", true: "task"}[tc.task != ""], func(t *testing.T) {
				t.Parallel()
				e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "role card cli")))
				root := t.TempDir()
				coord := &recordingCoordinator{
					fakeCoordinator: fakeCoordinator{root: filepath.Join(root, "coord")},
					files:           make(map[domain.RunID]map[string][]byte),
				}
				e.sched.UseCoordination(coord, filepath.Join(root, "bin"), true)
				const memberInstructions = "Prefer small diffs."
				if name == "codex" {
					home, err := e.cfg.Homes.Path(e.member.ID)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("developer_instructions = \""+memberInstructions+"\"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, tc.task, name, tc.mode)
				if err != nil {
					t.Fatal(err)
				}
				g := coordhooks.Guide{RunID: string(run.ID), Role: coordhooks.RoleInteractive}
				if tc.mode == domain.LaunchHeadless {
					g.Role, g.Background = coordhooks.RoleBackground, true
				}
				command := strings.Join(e.rt.byName(string(run.ID)).spec.Command, "\x00")
				// Gemini's interactive UI asks to trust every included directory.
				if name == "gemini" && strings.Contains(command, "--include-directories") {
					t.Fatalf("gemini command %q includes a directory", command)
				}
				profile, _ := harness.Lookup(name)
				env := e.rt.byName(string(run.ID)).spec.Env
				if background := env[coordtransport.BackgroundEnv] == "1"; background != g.Background {
					t.Fatalf("%s = %q in a %s launch", coordtransport.BackgroundEnv, env[coordtransport.BackgroundEnv], tc.mode)
				}
				g.Wake = env[coordtransport.WakeEnv] == "1"
				// The server wakes a terminal attached to its app-server, with or
				// without a task.
				native := profile.Capabilities.IdleWake && tc.mode == domain.LaunchTUI && (tc.task != "" || profile.Capabilities.Steer)
				if g.Wake != native {
					t.Fatalf("%s = %q, want it only on a native launch (%v)", coordtransport.WakeEnv, env[coordtransport.WakeEnv], native)
				}
				if !profile.CarriesInstructions() {
					if tc.task == "" {
						if strings.Contains(command, "You are Aether run") || env[coordtransport.RoleCardEnv] != "" {
							t.Fatalf("taskless launch %q gained a seeded prompt", command)
						}
						return
					}
					if want := withInstructions(tc.task, e.sched.runInstructions(coordhooks.Guide{Role: g.Role, Background: g.Background})); !strings.Contains(command, want) {
						t.Fatalf("command %q lacks the task with the role card", command)
					}
					return
				}
				if env[coordtransport.RoleCardEnv] != "1" {
					t.Fatalf("a launch carrying the card in its system prompt lacks %s", coordtransport.RoleCardEnv)
				}
				card := e.sched.runInstructions(g)
				if !strings.Contains(card, string(run.ID)) || !strings.Contains(card, "waiting") || !strings.Contains(card, coordhooks.PeerRule) {
					t.Fatalf("role card %q lacks the session, the waiting outcome or the peer rule", card)
				}
				if strings.Contains(card, "- waiting:") != g.Wake || strings.Contains(card, "- needs-input:") == g.Background {
					t.Fatalf("role card %q offers outcomes this %s launch cannot use (wake %v)", card, tc.mode, g.Wake)
				}
				if name == "codex" {
					card = memberInstructions + "\n\n" + card
				}
				if strings.Contains(command, "\x00"+tc.task+"\n\n") {
					t.Fatalf("command %q repeats the instructions after the task", command)
				}
				if profile.InstructionsFile != "" {
					if file := coord.file(run.ID, profile.InstructionsFile); string(file) != card+"\n" {
						t.Fatalf("instructions file %s = %q, want the role card", profile.InstructionsFile, file)
					}
				}
				switch name {
				case "copilot":
					if env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"] != coordtransport.MountDir {
						t.Fatalf("COPILOT_CUSTOM_INSTRUCTIONS_DIRS = %q, want %s", env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"], coordtransport.MountDir)
					}
					return
				case "opencode":
					var config struct {
						Instructions []string `json:"instructions"`
					}
					if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil || !slices.Contains(config.Instructions, coordtransport.MountDir+"/"+harness.InstructionsFileName) {
						t.Fatalf("OPENCODE_CONFIG_CONTENT %q does not load the instructions file: %v", env["OPENCODE_CONFIG_CONTENT"], err)
					}
					return
				}
				if args := strings.Join(profile.InstructionLaunchArgs(coordtransport.MountDir, card), "\x00"); !strings.Contains(command, args) {
					t.Fatalf("command %q lacks the instruction arguments %q", command, args)
				}
			})
		}
	}
}

func TestCodexLaunchesPastALinkedConfig(t *testing.T) {
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "linked codex config")))
	root := t.TempDir()
	e.sched.UseCoordination(&fakeCoordinator{root: filepath.Join(root, "coord")}, filepath.Join(root, "bin"), true)
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "config.toml")
	if err = os.WriteFile(shared, []byte(`developer_instructions = "Prefer small diffs."`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(shared, filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "fix the login", "codex", domain.LaunchTUI)
	if err != nil {
		t.Fatal(err)
	}
	command := strings.Join(e.rt.byName(string(run.ID)).spec.Command, " ")
	if !strings.Contains(command, "developer_instructions=") || strings.Contains(command, "Prefer small diffs.") {
		t.Fatalf("command %q, want the role card without the linked config's instructions", command)
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
				t.Fatalf("instruction files = %v", config.Instructions)
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
