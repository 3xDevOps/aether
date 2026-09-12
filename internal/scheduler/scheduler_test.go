package scheduler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/mirror"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

const waitTimeout = 10 * time.Second

// testEnv wires a scheduler to the real store, real event bus, fake git/pty,
// and an in-memory immutable base-capture seam.
type testEnv struct {
	t      *testing.T
	db     *store.DB
	bus    *events.InProc
	rt     *fakeRuntime
	git    *fakeGit
	pty    *fakePTY
	base   *fakeBaseCapture
	sched  *Scheduler
	cfg    Config
	ws     *domain.Workspace
	member *domain.Member
}

const testBaseCommit = "0123456789abcdef0123456789abcdef01234567"

type fakeBaseCapture struct {
	mu     sync.Mutex
	result mirror.CaptureResult
	err    error
	calls  []string
}

func (b *fakeBaseCapture) Capture(_ context.Context, workspace domain.WorkspaceID, cachedCommit string) (mirror.CaptureResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, cachedCommit)
	result := b.result
	if result.WorkspaceID == "" {
		result.WorkspaceID = workspace
	}
	return result, b.err
}

type scriptedWaitOutcome struct {
	status        runtime.ExitStatus
	err           error
	useUnderlying bool
}

// scriptedWaitRuntime controls each Wait call independently. It lets
// supervision tests model a daemon transport failure followed by the real
// container exit without mutating shared fake-runtime state concurrently.
type scriptedWaitRuntime struct {
	*fakeRuntime
	calls    chan runtime.ID
	outcomes chan scriptedWaitOutcome
}

func newScriptedWaitRuntime(base *fakeRuntime) *scriptedWaitRuntime {
	return &scriptedWaitRuntime{
		fakeRuntime: base,
		calls:       make(chan runtime.ID),
		outcomes:    make(chan scriptedWaitOutcome),
	}
}

func (r *scriptedWaitRuntime) Wait(ctx context.Context, id runtime.ID) (runtime.ExitStatus, error) {
	select {
	case r.calls <- id:
	case <-ctx.Done():
		return runtime.ExitStatus{}, ctx.Err()
	}
	select {
	case outcome := <-r.outcomes:
		if outcome.useUnderlying {
			return r.fakeRuntime.Wait(ctx, id)
		}
		return outcome.status, outcome.err
	case <-ctx.Done():
		return runtime.ExitStatus{}, ctx.Err()
	}
}

func testPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPub)), "\n")
}

func newTestEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	e := &testEnv{
		t:   t,
		db:  db,
		bus: bus,
		rt:  newFakeRuntime(),
		git: newFakeGit(filepath.Join(dir, "checkouts")),
		pty: newFakePTY(),
	}
	ctx := t.Context()
	e.ws = &domain.Workspace{
		Name:        "ws",
		BaseBranch:  "main",
		Environment: domain.WorkspaceEnvironment{Variables: map[string]string{"WS": "1"}},
	}
	if cerr := db.CreateWorkspace(ctx, e.ws); cerr != nil {
		t.Fatalf("create workspace: %v", cerr)
	}
	e.base = &fakeBaseCapture{result: mirror.CaptureResult{
		Commit:    testBaseCommit,
		Branch:    e.ws.BaseBranch,
		CheckedAt: time.Unix(1, 0).UTC(),
	}}
	e.member = &domain.Member{DisplayName: "Ada", PublicKey: testPublicKey(t), Color: "#e6194b", Role: domain.RoleCollaborator}
	if cerr := db.CreateMember(ctx, e.member); cerr != nil {
		t.Fatalf("create member: %v", cerr)
	}

	homes, err := memberhome.New(filepath.Join(dir, "homes"))
	if err != nil {
		t.Fatalf("memberhome.New: %v", err)
	}
	e.cfg = Config{
		Store:         db,
		Runtime:       e.rt,
		Bus:           bus,
		Git:           e.git,
		Bases:         e.base,
		PTY:           e.pty,
		StateDir:      filepath.Join(dir, "scheduler"),
		Homes:         homes,
		StandardImage: "busybox:1.36",
	}
	if mutate != nil {
		mutate(&e.cfg)
	}
	sched, err := New(e.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = sched.Close() })
	e.sched = sched
	return e
}

// newScheduler builds a second scheduler over the same store, state dir,
// and bus - a "rebooted server" - with fresh PTY state and the given
// runtime.
func (e *testEnv) newScheduler(t *testing.T, rt *fakeRuntime, pty *fakePTY) *Scheduler {
	t.Helper()
	cfg := e.cfg
	cfg.Runtime = rt
	cfg.PTY = pty
	sched, err := New(cfg)
	if err != nil {
		t.Fatalf("New (rebooted): %v", err)
	}
	t.Cleanup(func() { _ = sched.Close() })
	return sched
}

func (e *testEnv) subscribe(t *testing.T) events.Subscription {
	t.Helper()
	sub, err := e.bus.Subscribe(t.Context(), events.SubscribeOptions{
		Filter: events.Filter{Workspace: e.ws.ID},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

// launchFake launches a run on the deterministic fake harness and returns
// it together with its fake container.
func (e *testEnv) launchFake(t *testing.T, task string) (*domain.Run, *fakeContainer) {
	t.Helper()
	t.Setenv(fakeAgentEnv, "fake-agent {task}")
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	c := e.rt.byName(string(run.ID))
	if c == nil {
		t.Fatalf("no container created for run %s", run.ID)
	}
	return run, c
}
func readSavedTerminalImage(t *testing.T, e *testEnv, member domain.MemberID, returnedPath string) []byte {
	t.Helper()
	const imagePath = ".aether/terminal-images/"
	relativeStart := strings.Index(returnedPath, imagePath)
	if relativeStart < 0 {
		t.Fatalf("image path = %q, want a .aether terminal image path", returnedPath)
	}
	home, err := e.cfg.Homes.Path(member)
	if err != nil {
		t.Fatalf("member home: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(returnedPath[relativeStart:])))
	if err != nil {
		t.Fatalf("read saved image from %q: %v", returnedPath, err)
	}
	return data
}

func TestSaveTerminalImageUsesRunAccountHome(t *testing.T) {
	e := newTestEnv(t, nil)
	account := &domain.Member{
		DisplayName: "Grace", PublicKey: testPublicKey(t),
		Color: "#3cb44b", Role: domain.RoleCollaborator,
	}
	if err := e.db.CreateMember(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeAgentEnv, "fake-agent {task}")
	run, err := e.sched.Launch(
		t.Context(), e.ws.ID, e.member.ID, account.ID,
		"save image", "fake", domain.LaunchTUI,
	)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	image := []byte("run account image")
	path, err := e.sched.SaveTerminalImage(t.Context(), e.member.ID, run.ID, ".png", image)
	if err != nil {
		t.Fatalf("SaveTerminalImage: %v", err)
	}
	if got := readSavedTerminalImage(t, e, account.ID, path); string(got) != string(image) {
		t.Fatalf("saved image = %q, want %q", got, image)
	}

	launcherHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatalf("launcher home: %v", err)
	}
	launcherImages := filepath.Join(launcherHome, ".aether", "terminal-images")
	entries, err := os.ReadDir(launcherImages)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read launcher image directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("launcher home received terminal image: %v", entries)
	}
}

// waitStatusEvent reads sub until a run.status event with the wanted To
// status arrives and returns it.
func waitStatusEvent(t *testing.T, sub events.Subscription, run domain.RunID, to domain.RunStatus) events.Event {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("event stream closed while waiting for %s -> %s", run, to)
			}
			p, isStatus := ev.Payload.(events.RunStatusPayload)
			if isStatus && (run == "" || ev.RunID == run) && p.To == to {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for run.status %s on run %s", to, run)
		}
	}
}

func waitTimelineEvent(t *testing.T, sub events.Subscription, run domain.RunID, kind events.TimelineKind) events.Event {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("event stream closed while waiting for timeline %s", kind)
			}
			p, isTL := ev.Payload.(events.TimelinePayload)
			if isTL && ev.RunID == run && p.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for timeline %s on run %s", kind, run)
		}
	}
}

// waitStoreStatus polls the store until the run reaches status.
func (e *testEnv) waitStoreStatus(t *testing.T, run domain.RunID, status domain.RunStatus) *domain.Run {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		r, err := e.db.GetRun(t.Context(), run)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if r.Status == status {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s stuck at %s, want %s", run, r.Status, status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func waitScriptedWaitCall(t *testing.T, rt *scriptedWaitRuntime) runtime.ID {
	t.Helper()
	timer := time.NewTimer(waitTimeout)
	defer timer.Stop()
	select {
	case id := <-rt.calls:
		return id
	case <-timer.C:
		t.Fatalf("timed out waiting for scripted Wait call")
		return ""
	}
}

func sendScriptedWaitOutcome(t *testing.T, rt *scriptedWaitRuntime, outcome scriptedWaitOutcome) {
	t.Helper()
	timer := time.NewTimer(waitTimeout)
	defer timer.Stop()
	select {
	case rt.outcomes <- outcome:
	case <-timer.C:
		t.Fatalf("timed out sending scripted Wait outcome")
	}
}

func TestHappyPath(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	ctx := t.Context()

	run, c := e.launchFake(t, "fix the auth bug")
	if run.Status != domain.RunRunning {
		t.Fatalf("run status after launch = %s, want running", run.Status)
	}
	if run.StartedAt == nil {
		t.Fatal("run.StartedAt not set")
	}
	if run.Branch != "aether/run-"+string(run.ID) {
		t.Fatalf("run.Branch = %q", run.Branch)
	}
	if run.Worktree != e.git.checkoutPath(run.ID) {
		t.Fatalf("run.Worktree = %q", run.Worktree)
	}
	if !c.spec.TTY {
		t.Fatal("container spec must set TTY")
	}
	if c.spec.Env["AETHER_RUN_ID"] != string(run.ID) || c.spec.Env["TERM"] != "xterm-256color" {
		t.Fatalf("container env = %v", c.spec.Env)
	}
	if c.spec.Env["AETHER_WORKSPACE_ID"] != string(e.ws.ID) {
		t.Fatalf("container workspace env = %q, want %q", c.spec.Env["AETHER_WORKSPACE_ID"], e.ws.ID)
	}
	if c.spec.WorktreeHostPath != run.Worktree || c.spec.WorktreeMountPath != "/workspace" {
		t.Fatalf("worktree mount = %q -> %q", c.spec.WorktreeHostPath, c.spec.WorktreeMountPath)
	}
	if _, err := os.Stat(e.sched.sidecarPath(run.ID)); err != nil {
		t.Fatalf("sidecar missing while running: %v", err)
	}
	if ws, watching := e.git.watchingFor(run.ID); !watching || ws != e.ws.ID {
		t.Fatalf("diff watch scope = %q (watching=%v), want %q", ws, watching, e.ws.ID)
	}

	prov := waitStatusEvent(t, sub, run.ID, domain.RunProvisioning)
	if p := prov.Payload.(events.RunStatusPayload); p.From != domain.RunQueued {
		t.Fatalf("provisioning event From = %s, want queued", p.From)
	}
	if prov.ActorID != e.member.ID {
		t.Fatalf("provisioning event actor = %s, want %s", prov.ActorID, e.member.ID)
	}
	waitStatusEvent(t, sub, run.ID, domain.RunRunning)

	c.output("agent working\r\n")
	waitFor(t, "pty output", func() bool {
		sess := e.pty.session(run.ID)
		return sess != nil && strings.Contains(sess.output(), "agent working")
	})

	c.exitNow(0)
	ev := waitStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("completed reason = %q", p.Reason)
	}
	fresh := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if fresh.FinishedAt == nil {
		t.Fatal("completed run must set FinishedAt")
	}
	completedAt := *fresh.FinishedAt
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: fix the auth bug" {
		t.Fatalf("commits = %v", got)
	}
	if e.git.publishedCount(run.ID) == 0 {
		t.Fatal("run branch never published")
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(e.sched.sidecarPath(run.ID))
		return os.IsNotExist(err)
	})
	if _, err := os.Stat(run.Worktree); err != nil {
		t.Fatalf("checkout must be preserved after exit: %v", err)
	}

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	closed := waitStatusEvent(t, sub, run.ID, domain.RunMerged)
	if closed.ActorID != e.member.ID {
		t.Fatalf("close actor = %s", closed.ActorID)
	}
	final := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if final.FinishedAt == nil || !final.FinishedAt.Equal(completedAt) {
		t.Fatalf("merged FinishedAt = %v, want completion time %v", final.FinishedAt, completedAt)
	}
}

func TestHeadlessContainerKeepsTheAgentAsTheMainProcess(t *testing.T) {
	e := newTestEnv(t, nil)
	run := &domain.Run{ID: "run-headless", Mode: domain.LaunchHeadless}
	plan := &EnvironmentPlan{Env: map[string]string{}}
	spec := e.sched.containerSpec(run, e.member, []string{"agent", "--json"}, plan)
	if want := []string{"agent", "--json"}; !slices.Equal(spec.Command, want) {
		t.Fatalf("headless container command = %v, want %v", spec.Command, want)
	}
}

func TestTUIContainerUsesSafePersistentSupervisor(t *testing.T) {
	e := newTestEnv(t, nil)
	run := &domain.Run{ID: "run-tui", WorkspaceID: e.ws.ID, MemberID: e.member.ID, Mode: domain.LaunchTUI}
	plan := &EnvironmentPlan{Env: map[string]string{}}
	argv := []string{"agent", "--task", `$(touch compromised)`}
	spec := e.sched.containerSpec(run, e.member, argv, plan)
	if len(spec.Command) < 5 || spec.Command[0] != "/bin/sh" || spec.Command[1] != "-c" {
		t.Fatalf("TUI command = %v, want POSIX supervisor", spec.Command)
	}
	script := spec.Command[2]
	if !strings.Contains(script, `"${@}"`) && !strings.Contains(script, `"$@"`) {
		t.Fatalf("TUI supervisor does not execute positional argv safely: %q", script)
	}
	if !strings.Contains(script, "while :") || !strings.Contains(script, "/bin/bash -l") {
		t.Fatalf("TUI supervisor does not keep login shells available: %q", script)
	}
	if !slices.Equal(spec.Command[4:], argv) {
		t.Fatalf("TUI supervisor argv = %v, want %v", spec.Command[4:], argv)
	}
	headless := *run
	headless.Mode = domain.LaunchHeadless
	headlessSpec := e.sched.containerSpec(&headless, e.member, argv, plan)
	if !slices.Equal(headlessSpec.Command, argv) {
		t.Fatalf("headless argv changed: %v", headlessSpec.Command)
	}
}

type testPTYProcess struct {
	cmd    *exec.Cmd
	master *os.File
	reads  <-chan string
	done   <-chan error
	output strings.Builder
}

func startTestPTYProcess(t *testing.T, argv []string) *testPTYProcess {
	t.Helper()
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open PTY master: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "/dev/ptmx")
	ptyNumber, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		t.Fatalf("get PTY number: %v", err)
	}
	if unlockErr := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); unlockErr != nil {
		_ = master.Close()
		t.Fatalf("unlock PTY: %v", unlockErr)
	}
	slaveFD, err := unix.Open("/dev/pts/"+strconv.Itoa(ptyNumber), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		t.Fatalf("open PTY slave: %v", err)
	}
	slave := os.NewFile(uintptr(slaveFD), "/dev/pts/"+strconv.Itoa(ptyNumber))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		_ = master.Close()
		t.Fatalf("start PTY command: %v", err)
	}
	if err := slave.Close(); err != nil {
		_ = master.Close()
		t.Fatalf("close PTY slave: %v", err)
	}
	reads := make(chan string, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := master.Read(buf)
			if n > 0 {
				reads <- string(buf[:n])
			}
			if readErr != nil {
				close(reads)
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return &testPTYProcess{cmd: cmd, master: master, reads: reads, done: done}
}

func (p *testPTYProcess) waitForOutput(t *testing.T, marker string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for !strings.Contains(p.output.String(), marker) {
		select {
		case chunk, ok := <-p.reads:
			if !ok {
				t.Fatalf("PTY closed before %q; output = %q", marker, p.output.String())
			}
			p.output.WriteString(chunk)
		case <-timer.C:
			t.Fatalf("timed out waiting for %q; output = %q", marker, p.output.String())
		}
	}
}

func (p *testPTYProcess) drainOutput() {
	for {
		select {
		case chunk, ok := <-p.reads:
			if !ok {
				return
			}
			p.output.WriteString(chunk)
		default:
			return
		}
	}
}

func TestTUIWrapperKeepsNormalShellsAndForwardsStop(t *testing.T) {
	wrapped := wrapTUICommand([]string{"/bin/sh", "-c", "printf 'harness-ready\\n'; IFS= read -r line; printf 'harness-input:%s\\n' \"$line\""})
	p := startTestPTYProcess(t, wrapped)
	defer func() {
		_ = p.cmd.Process.Kill()
		_ = p.master.Close()
	}()

	p.waitForOutput(t, "harness-ready")
	if _, err := p.master.Write([]byte("from-harness\n")); err != nil {
		t.Fatalf("write harness input: %v", err)
	}
	p.waitForOutput(t, "harness-input:from-harness")
	p.waitForOutput(t, "[aether] harness exited with code 0")
	if _, err := p.master.Write([]byte("case $- in *i*) printf 'shell-%s\\n' interactive;; *) printf 'shell-%s\\n' noninteractive;; esac\nexit\n")); err != nil {
		t.Fatalf("write first shell input: %v", err)
	}
	p.waitForOutput(t, "shell-interactive")
	if _, err := p.master.Write([]byte("printf 'shell-%s\\n' replacement\n")); err != nil {
		t.Fatalf("write replacement shell input: %v", err)
	}
	p.waitForOutput(t, "shell-replacement")

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal ready login shell: %v", err)
	}
	select {
	case err := <-p.done:
		if err == nil {
			t.Fatal("supervisor exited successfully after TERM")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not exit after TERM reached login shell")
	}
	time.Sleep(100 * time.Millisecond)
	p.drainOutput()
	if got := strings.Count(p.output.String(), "[aether] harness exited with code"); got != 1 {
		t.Fatalf("harness status count = %d, output = %q", got, p.output.String())
	}
	time.Sleep(100 * time.Millisecond)
	_, _ = p.master.Write([]byte("printf 'after-term\\n'\n"))
	time.Sleep(100 * time.Millisecond)
	p.drainOutput()
	if strings.Contains(p.output.String(), "after-term") {
		t.Fatalf("login shell respawned after TERM: output = %q", p.output.String())
	}
}

func TestTUIWrapperForwardsTERMToHarness(t *testing.T) {
	wrapped := wrapTUICommand([]string{"/bin/sh", "-c", "trap 'printf \"harness-%s\\n\" term; exit 0' TERM; printf 'harness-%s\\n' ready; while :; do read -r line; done"})
	p := startTestPTYProcess(t, wrapped)
	defer func() {
		_ = p.cmd.Process.Kill()
		_ = p.master.Close()
	}()

	p.waitForOutput(t, "harness-ready")
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal running harness: %v", err)
	}
	select {
	case err := <-p.done:
		if err == nil {
			t.Fatal("supervisor exited successfully after TERM")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not exit after TERM reached harness")
	}
	time.Sleep(100 * time.Millisecond)
	p.drainOutput()
	if got := strings.Count(p.output.String(), "harness-term"); got != 1 {
		t.Fatalf("harness TERM observations = %d, output = %q", got, p.output.String())
	}
	if strings.Contains(p.output.String(), "[aether] harness exited with code") {
		t.Fatalf("signal shutdown printed normal harness status: output = %q", p.output.String())
	}
	_, _ = p.master.Write([]byte("printf 'after-signal\\n'\n"))
	time.Sleep(100 * time.Millisecond)
	p.drainOutput()
	if strings.Contains(p.output.String(), "after-signal") {
		t.Fatalf("login shell started after harness TERM: output = %q", p.output.String())
	}
}

func TestAgentCrash(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	run, c := e.launchFake(t, "risky refactor\nwith details")
	c.exitNow(3)

	ev := waitStatusEvent(t, sub, run.ID, domain.RunFailed)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited 3" {
		t.Fatalf("failed reason = %q", p.Reason)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "wip: risky refactor" {
		t.Fatalf("commits = %v", got)
	}
	if e.git.publishedCount(run.ID) == 0 {
		t.Fatal("run branch never published after crash")
	}
	fresh := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	if fresh.FinishedAt == nil {
		t.Fatal("failed run must have FinishedAt")
	}
	if _, err := os.Stat(fresh.Worktree); err != nil {
		t.Fatalf("checkout must be preserved after crash: %v", err)
	}
}

func TestProvisioningFailure(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	e.rt.createErr = errors.New("no such image")

	t.Setenv(fakeAgentEnv, "fake-agent")
	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "task", "fake", domain.LaunchTUI)
	if err == nil {
		t.Fatal("Launch succeeded despite runtime failure")
	}

	prov := waitStatusEvent(t, sub, "", domain.RunProvisioning)
	failed := waitStatusEvent(t, sub, prov.RunID, domain.RunFailed)
	p := failed.Payload.(events.RunStatusPayload)
	if !strings.HasPrefix(p.Reason, "provisioning: ") || !strings.Contains(p.Reason, "no such image") {
		t.Fatalf("failed reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, prov.RunID, domain.RunFailed)
}

func TestLaunchValidation(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	if _, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "t", "unknown-harness", domain.LaunchTUI); err == nil {
		t.Fatal("unknown harness accepted")
	}
	if _, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "t", "claude", domain.LaunchMode("bogus")); err == nil {
		t.Fatal("invalid mode accepted")
	}
	t.Setenv(fakeAgentEnv, "fake-agent")
	if _, err := e.sched.Launch(ctx, "ws_missing", e.member.ID, e.member.ID, "t", "fake", domain.LaunchTUI); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing workspace error = %v, want ErrNotFound", err)
	}
	t.Setenv(fakeAgentEnv, "")
	if _, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "t", "fake", domain.LaunchTUI); err == nil {
		t.Fatal("fake harness with empty AETHER_FAKE_AGENT accepted")
	}
}

func TestLaunchCapturesAndPinsBaseProvenance(t *testing.T) {
	e := newTestEnv(t, nil)
	t.Setenv(fakeAgentEnv, "fake-agent")
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "pinned base", "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	e.base.mu.Lock()
	calls := slices.Clone(e.base.calls)
	e.base.mu.Unlock()
	if !slices.Equal(calls, []string{""}) {
		t.Fatalf("base capture calls = %v, want strict empty cached commit", calls)
	}
	if run.BaseCommit != testBaseCommit || run.BaseBranch != e.ws.BaseBranch || run.BaseSource != "local" {
		t.Fatalf("run base provenance = commit=%q branch=%q source=%q",
			run.BaseCommit, run.BaseBranch, run.BaseSource)
	}
	if run.BaseCheckedAt.IsZero() {
		t.Fatal("run base provenance has no checked-at timestamp")
	}
	if got := e.git.baseCommitFor(run.ID); got != testBaseCommit {
		t.Fatalf("checkout base commit = %q, want %q", got, testBaseCommit)
	}
	if got := e.git.baseBranchFor(run.ID); got != e.ws.BaseBranch {
		t.Fatalf("checkout base branch = %q, want %q", got, e.ws.BaseBranch)
	}
}

func TestLaunchCachedBaseOptionPinsCachedSource(t *testing.T) {
	e := newTestEnv(t, nil)
	e.base.mu.Lock()
	e.base.result.Configured = true
	e.base.result.Cached = true
	e.base.result.Source = "github.com/acme/project"
	e.base.mu.Unlock()
	t.Setenv(fakeAgentEnv, "fake-agent")
	const cached = testBaseCommit
	run, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID,
		"cached base", "fake", domain.LaunchTUI, domain.LaunchOptions{CachedBase: cached})
	if err != nil {
		t.Fatalf("LaunchWithOptions: %v", err)
	}
	e.base.mu.Lock()
	calls := slices.Clone(e.base.calls)
	e.base.mu.Unlock()
	if !slices.Equal(calls, []string{cached}) {
		t.Fatalf("base capture calls = %v, want %q", calls, cached)
	}
	if run.BaseSource != "cached:github.com/acme/project" {
		t.Fatalf("cached base source = %q", run.BaseSource)
	}
}

func TestLaunchCachedBaseOptionRejectsFaultyCapture(t *testing.T) {
	e := newTestEnv(t, nil)
	const cached = testBaseCommit
	different := strings.Repeat("f", 40)
	e.base.mu.Lock()
	e.base.result = mirror.CaptureResult{
		WorkspaceID: e.ws.ID,
		Commit:      different,
		Branch:      e.ws.BaseBranch,
		Source:      "github.com/acme/project",
		Configured:  true,
		Cached:      false,
	}
	e.base.mu.Unlock()
	t.Setenv(fakeAgentEnv, "fake-agent")
	_, err := e.sched.LaunchWithOptions(t.Context(), e.ws.ID, e.member.ID, e.member.ID,
		"faulty cached capture", "fake", domain.LaunchTUI, domain.LaunchOptions{CachedBase: cached})
	if err == nil {
		t.Fatal("LaunchWithOptions succeeded with an uncached, different capture")
	}
	var captureErr *BaseCaptureError
	if !errors.As(err, &captureErr) {
		t.Fatalf("LaunchWithOptions error = %T %v, want BaseCaptureError", err, err)
	}
	var mirrorErr *gitengine.MirrorError
	if !errors.As(err, &mirrorErr) || mirrorErr.Kind != gitengine.MirrorErrorInvalidRequest {
		t.Fatalf("LaunchWithOptions error = %v, want invalid-request MirrorError", err)
	}
	if captureErr.Capture.Commit != different || captureErr.Capture.Cached {
		t.Fatalf("capture result = %+v, want faulty uncached commit %q", captureErr.Capture, different)
	}

	runs, listErr := e.db.ListRunsByWorkspace(t.Context(), e.ws.ID)
	if listErr != nil {
		t.Fatalf("ListRunsByWorkspace: %v", listErr)
	}
	if len(runs) != 0 {
		t.Fatalf("run rows after cached capture mismatch = %d, want 0", len(runs))
	}
	e.sched.mu.Lock()
	pendingCount, runCount := len(e.sched.pending), len(e.sched.runs)
	e.sched.mu.Unlock()
	if pendingCount != 0 || runCount != 0 {
		t.Fatalf("scheduler state after cached capture mismatch: pending=%d runs=%d", pendingCount, runCount)
	}
	e.git.mu.Lock()
	checkoutCount, checkoutRoot := len(e.git.baseCommits), e.git.root
	e.git.mu.Unlock()
	if checkoutCount != 0 {
		t.Fatalf("checkout records after cached capture mismatch = %d, want 0", checkoutCount)
	}
	if _, statErr := os.Stat(checkoutRoot); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("checkout root after cached capture mismatch: stat error = %v", statErr)
	}
	e.rt.mu.Lock()
	containerCount, createCount := len(e.rt.containers), e.rt.seq
	e.rt.mu.Unlock()
	if containerCount != 0 || createCount != 0 {
		t.Fatalf("runtime provisioning after cached capture mismatch: containers=%d creates=%d", containerCount, createCount)
	}
}

func TestBaseCaptureFailureLeavesNoRunState(t *testing.T) {
	e := newTestEnv(t, nil)
	cause := errors.New("mirror refresh failed")
	e.base.mu.Lock()
	e.base.result = mirror.CaptureResult{
		WorkspaceID: e.ws.ID,
		Commit:      testBaseCommit,
		Branch:      e.ws.BaseBranch,
		Source:      "github.com/acme/project",
		Configured:  true,
	}
	e.base.err = cause
	e.base.mu.Unlock()
	t.Setenv(fakeAgentEnv, "fake-agent")
	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "failed capture", "fake", domain.LaunchTUI)
	if err == nil {
		t.Fatal("Launch succeeded despite base capture failure")
	}
	var captureErr *BaseCaptureError
	if !errors.As(err, &captureErr) || !errors.Is(err, cause) {
		t.Fatalf("Launch error = %v, want BaseCaptureError wrapping cause", err)
	}
	if captureErr.Capture.Commit != testBaseCommit || captureErr.Capture.Source != "github.com/acme/project" {
		t.Fatalf("capture result = %+v", captureErr.Capture)
	}
	runs, listErr := e.db.ListRunsByWorkspace(t.Context(), e.ws.ID)
	if listErr != nil {
		t.Fatalf("ListRunsByWorkspace: %v", listErr)
	}
	if len(runs) != 0 {
		t.Fatalf("run rows after capture failure = %d, want 0", len(runs))
	}
	e.sched.mu.Lock()
	defer e.sched.mu.Unlock()
	if len(e.sched.pending) != 0 || len(e.sched.runs) != 0 {
		t.Fatalf("scheduler state after capture failure: pending=%d runs=%d", len(e.sched.pending), len(e.sched.runs))
	}
}

func TestCommandTemplates(t *testing.T) {
	e := newTestEnv(t, nil)
	argv, profile, err := e.sched.command(t.Context(), e.member.ID, "claude", domain.LaunchHeadless, "do it")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "do it"}
	if fmt.Sprint(argv) != fmt.Sprint(want) {
		t.Fatalf("claude headless argv = %v, want %v", argv, want)
	}
	if profile.Name != "claude" || len(profile.CredentialPaths) == 0 {
		t.Fatalf("claude profile = %+v, want registry profile with credential paths", profile)
	}
	if _, _, codexErr := e.sched.command(t.Context(), e.member.ID, "codex", domain.LaunchTUI, "x"); codexErr != nil {
		t.Fatalf("codex tui: %v", codexErr)
	}
	// A Config.Harnesses argv override replaces the registry template but
	// keeps the registry profile.
	e2 := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"my-claude", "{task}"}}}
	})
	argv, profile, err = e2.sched.command(t.Context(), e2.member.ID, "claude", domain.LaunchTUI, "go")
	if err != nil {
		t.Fatalf("command with override: %v", err)
	}
	if fmt.Sprint(argv) != fmt.Sprint([]string{"my-claude", "go"}) {
		t.Fatalf("override argv = %v", argv)
	}
	if profile.Name != "claude" {
		t.Fatalf("override lost registry profile: %+v", profile)
	}
	// "custom" ships with no command of its own: it requires an override.
	if _, _, err := e.sched.command(t.Context(), e.member.ID, "custom", domain.LaunchTUI, "x"); err == nil {
		t.Fatal("custom without an override accepted")
	}
}

// TestLaunchSpecIdentityAndCreationKey pins the Wave 2 spec construction:
// the agent's git identity env comes from the owning member and the run
// ID rides as the creation key for crash recovery.
func TestLaunchSpecIdentityAndCreationKey(t *testing.T) {
	e := newTestEnv(t, nil)
	run, c := e.launchFake(t, "identity check")

	env := c.spec.Env
	if env["GIT_AUTHOR_NAME"] != "Ada" || env["GIT_COMMITTER_NAME"] != "Ada" {
		t.Errorf("git name env = %q/%q, want Ada", env["GIT_AUTHOR_NAME"], env["GIT_COMMITTER_NAME"])
	}
	wantEmail := string(e.member.ID) + "@aether.local"
	if env["GIT_AUTHOR_EMAIL"] != wantEmail || env["GIT_COMMITTER_EMAIL"] != wantEmail {
		t.Errorf("git email env = %q/%q, want %q", env["GIT_AUTHOR_EMAIL"], env["GIT_COMMITTER_EMAIL"], wantEmail)
	}
	if c.spec.CreationKey != string(run.ID) {
		t.Errorf("creation key = %q, want %q", c.spec.CreationKey, run.ID)
	}
	if len(c.spec.Mounts) != 1 {
		t.Fatalf("mounts = %v, want one persistent home mount", c.spec.Mounts)
	}
	wantHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatalf("member home: %v", err)
	}
	if c.spec.Mounts[0].HostPath != wantHome || c.spec.Mounts[0].ContainerPath != "/root" || c.spec.Mounts[0].ReadOnly {
		t.Errorf("home mount = %+v, want %q at /root", c.spec.Mounts[0], wantHome)
	}
	if env["HOME"] != "/root" {
		t.Errorf("HOME = %q, want /root for a root run", env["HOME"])
	}
}

func TestSharedAccountLaunchUsesAccountHomeAndKeepsActorIdentity(t *testing.T) {
	e := newTestEnv(t, nil)
	account := &domain.Member{
		DisplayName: "Grace", PublicKey: testPublicKey(t),
		Color: "#3cb44b", Role: domain.RoleCollaborator,
	}
	if err := e.db.CreateMember(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeAgentEnv, "fake-agent {task}")
	sub := e.subscribe(t)
	run, err := e.sched.Launch(
		t.Context(), e.ws.ID, e.member.ID, account.ID,
		"shared account", "fake", domain.LaunchTUI,
	)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if run.MemberID != e.member.ID || run.AccountMember() != account.ID {
		t.Fatalf("run actor/account = %s/%s, want %s/%s", run.MemberID, run.AccountMember(), e.member.ID, account.ID)
	}
	c := e.rt.byName(string(run.ID))
	if c == nil || len(c.spec.Mounts) != 1 {
		t.Fatalf("container mounts = %+v, want account home", c)
	}
	wantHome, err := e.cfg.Homes.Path(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.spec.Mounts[0].HostPath != wantHome {
		t.Fatalf("home mount = %q, want account home %q", c.spec.Mounts[0].HostPath, wantHome)
	}
	if c.spec.Env["GIT_AUTHOR_NAME"] != e.member.DisplayName ||
		c.spec.Env["AETHER_ACCOUNT_MEMBER_ID"] != string(account.ID) {
		t.Fatalf("container identity env = %+v", c.spec.Env)
	}
	started := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if started.ActorID != e.member.ID {
		t.Fatalf("run.status actor = %s, want launcher %s", started.ActorID, e.member.ID)
	}
}

// TestLaunchMountsPersistentHome pins that every launch for one member uses
// the same writable server-owned home at the container's HOME.
func TestLaunchMountsPersistentHome(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"fake-claude", "{task}"}}}
	})
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "with home", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	c := e.rt.byName(string(run.ID))
	if c == nil {
		t.Fatal("no container created")
	}
	wantHome, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatalf("member home: %v", err)
	}
	if len(c.spec.Mounts) != 1 {
		t.Fatalf("mounts = %v, want exactly one home mount", c.spec.Mounts)
	}
	if got := c.spec.Mounts[0]; got.HostPath != wantHome || got.ContainerPath != "/root" || got.ReadOnly {
		t.Fatalf("home mount = %+v, want %q at /root", got, wantHome)
	}
	run2, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "later run", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("second Launch: %v", err)
	}
	c2 := e.rt.byName(string(run2.ID))
	if c2 == nil || len(c2.spec.Mounts) != 1 || c2.spec.Mounts[0].HostPath != wantHome {
		t.Fatalf("second home mount = %+v, want %q", c2.spec.Mounts, wantHome)
	}
}

// TestContainerSpecNonRootHome pins that a non-root run user gets
// HOME=/home/aether in the container env (Docker leaves HOME wrong for
// numeric users, and the credential mounts land under that home).
func TestContainerSpecNonRootHome(t *testing.T) {
	e := newTestEnv(t, nil)
	run := &domain.Run{ID: "run-x", WorkspaceID: e.ws.ID, MemberID: e.member.ID}
	plan := &EnvironmentPlan{Image: "busybox:1.36", Env: map[string]string{"HOME": "/home/aether"}, User: "1000:1000"}
	spec := e.sched.containerSpec(run, e.member, []string{"agent"}, plan)
	if spec.Env["HOME"] != "/home/aether" {
		t.Errorf("HOME = %q, want /home/aether", spec.Env["HOME"])
	}
	if spec.User != "1000:1000" {
		t.Errorf("user = %q, want 1000:1000", spec.User)
	}
}

// TestReserveRunUserConflict pins the credential-home ownership guard:
// a run whose resolved uid:gid differs from a live run of the same member
// fails provisioning loudly (the ownership pass would otherwise flip the
// shared home's ownership back and forth), while same mapping, different
// member, and root runs all pass. The guard is cross-platform; only the
// chown itself is linux-only.
func TestReserveRunUserConflict(t *testing.T) {
	e := newTestEnv(t, nil)
	live := &supervised{
		runID:    "run-live",
		memberID: e.member.ID,
		runUser:  "1000:1000",
	}
	e.sched.mu.Lock()
	e.sched.runs[live.runID] = live
	e.sched.mu.Unlock()

	entry := &supervised{runID: "run-new", memberID: e.member.ID}
	err := e.sched.reserveRunUser(entry, "2000:2000", true)
	if err == nil {
		t.Fatal("conflicting uid accepted for the shared member home")
	}
	if !strings.Contains(err.Error(), "member's environment home") {
		t.Errorf("conflict error %q does not name the member's environment home", err)
	}
	for _, want := range []string{"1000:1000", "2000:2000", "run-live"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict error %q does not name %q", err, want)
		}
	}

	same := &supervised{runID: "run-same", memberID: e.member.ID}
	if err := e.sched.reserveRunUser(same, "1000:1000", true); err != nil {
		t.Fatalf("same mapping rejected: %v", err)
	}
	if same.runUser != "1000:1000" {
		t.Errorf("runUser = %q, want recorded 1000:1000", same.runUser)
	}

	otherMember := &supervised{runID: "run-other-member", memberID: "other-member"}
	if err := e.sched.reserveRunUser(otherMember, "2000:2000", true); err != nil {
		t.Fatalf("different member rejected: %v", err)
	}

	root := &supervised{runID: "run-root", memberID: e.member.ID}
	if err := e.sched.reserveRunUser(root, "", true); err != nil {
		t.Fatalf("root run rejected: %v", err)
	}

	noHome := &supervised{runID: "run-nohome", memberID: e.member.ID}
	if err := e.sched.reserveRunUser(noHome, "2000:2000", false); err != nil {
		t.Fatalf("run without credential mounts rejected: %v", err)
	}
}

func TestPendingTerminalReservationBlocksConflictingRun(t *testing.T) {
	e := newTestEnv(t, nil)
	pending := &terminalSupervision{member: e.member.ID}
	if err := e.sched.reserveTerminalUser(pending, "1000:1000"); err != nil {
		t.Fatalf("reserve terminal user: %v", err)
	}

	conflicting := &supervised{runID: "run-conflict", memberID: e.member.ID}
	if err := e.sched.reserveRunUser(conflicting, "2000:2000", true); err == nil {
		t.Fatal("conflicting run user accepted while terminal reservation was pending")
	}
	e.sched.releaseTerminalReservation(pending)
	if err := e.sched.reserveRunUser(conflicting, "2000:2000", true); err != nil {
		t.Fatalf("run user after terminal release: %v", err)
	}
}

func TestLegalTransitions(t *testing.T) {
	allowed := map[[2]domain.RunStatus]bool{}
	for _, from := range domain.AllRunStatuses {
		allowed[[2]domain.RunStatus{from, domain.RunMerged}] = true
		allowed[[2]domain.RunStatus{from, domain.RunAbandoned}] = true
	}
	for _, from := range []domain.RunStatus{
		domain.RunQueued, domain.RunProvisioning, domain.RunRunning, domain.RunNeedsAttention,
	} {
		allowed[[2]domain.RunStatus{from, domain.RunInterrupted}] = true
	}
	allowed[[2]domain.RunStatus{domain.RunQueued, domain.RunProvisioning}] = true
	allowed[[2]domain.RunStatus{domain.RunProvisioning, domain.RunRunning}] = true
	allowed[[2]domain.RunStatus{domain.RunProvisioning, domain.RunFailed}] = true
	allowed[[2]domain.RunStatus{domain.RunRunning, domain.RunNeedsAttention}] = true
	allowed[[2]domain.RunStatus{domain.RunRunning, domain.RunCompleted}] = true
	allowed[[2]domain.RunStatus{domain.RunRunning, domain.RunFailed}] = true
	allowed[[2]domain.RunStatus{domain.RunNeedsAttention, domain.RunRunning}] = true
	allowed[[2]domain.RunStatus{domain.RunNeedsAttention, domain.RunNeedsAttention}] = true
	allowed[[2]domain.RunStatus{domain.RunNeedsAttention, domain.RunCompleted}] = true
	allowed[[2]domain.RunStatus{domain.RunNeedsAttention, domain.RunFailed}] = true

	for _, from := range domain.AllRunStatuses {
		for _, to := range domain.AllRunStatuses {
			want := allowed[[2]domain.RunStatus{from, to}]
			if got := legalTransition(from, to); got != want {
				t.Errorf("legalTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestInvalidAPITransitions(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Relaunch on running run: %v, want ErrInvalidTransition", err)
	}
	if err := e.sched.Resume(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Resume on unpaused run: %v, want ErrInvalidTransition", err)
	}

	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunFailed); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("CloseRun with outcome failed: %v, want ErrInvalidTransition", err)
	}
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Kill(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Kill on terminal run: %v", err)
	}
	if err := e.sched.Pause(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Pause on finished run: %v, want ErrInvalidTransition", err)
	}
	if err := e.sched.Inject(ctx, run.ID, e.member.ID, "hi"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Inject on finished run: %v, want ErrInvalidTransition", err)
	}
}

func TestDeleteRunRemovesTerminalRun(t *testing.T) {
	e := newTestEnv(t, nil)
	run := &domain.Run{
		WorkspaceID: e.ws.ID,
		MemberID:    e.member.ID,
		Task:        "remove stale run",
		Harness:     "claude",
		Mode:        domain.LaunchTUI,
		Status:      domain.RunFailed,
		CreatedAt:   time.Now().UTC(),
	}
	if err := e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if _, err := e.db.GetRun(t.Context(), run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after delete: %v, want store.ErrNotFound", err)
	}
}

func TestDeleteRunStopsActiveRunBeforeRemovingIt(t *testing.T) {
	e := newTestEnv(t, nil)
	run, container := e.launchFake(t, "remove active run")

	if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if _, err := e.db.GetRun(t.Context(), run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after active delete: %v, want store.ErrNotFound", err)
	}
	if container.currentState() != "stopped" {
		t.Fatalf("container state = %q, want stopped", container.currentState())
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("active run container was not destroyed")
	}
}

func TestDeleteRunPublishesDeletedEvent(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	run := &domain.Run{
		WorkspaceID: e.ws.ID,
		MemberID:    e.member.ID,
		Task:        "publish deletion",
		Harness:     "claude",
		Mode:        domain.LaunchTUI,
		Status:      domain.RunFailed,
		CreatedAt:   time.Now().UTC(),
	}
	if err := e.db.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}

	select {
	case ev := <-sub.Events():
		if ev.Type != events.TypeRunDeleted || ev.RunID != run.ID {
			t.Fatalf("deletion event = %#v, want run.deleted for %s", ev, run.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("DeleteRun published no deletion event")
	}
}

func TestTaskLine(t *testing.T) {
	if got := taskLine("short"); got != "short" {
		t.Fatalf("taskLine short = %q", got)
	}
	if got := taskLine("first line\nsecond"); got != "first line" {
		t.Fatalf("taskLine multiline = %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := taskLine(long); len(got) != 72 {
		t.Fatalf("taskLine long = %d chars", len(got))
	}
}

func TestCheckoutTTLDefault(t *testing.T) {
	e := newTestEnv(t, nil)
	if got := e.sched.cfg.CheckoutTTL; got != 72*time.Hour {
		t.Fatalf("default CheckoutTTL = %v, want 72h", got)
	}
	disabled := newTestEnv(t, func(cfg *Config) { cfg.CheckoutTTL = -1 })
	if got := disabled.sched.cfg.CheckoutTTL; got >= 0 {
		t.Fatalf("negative CheckoutTTL = %v, want kept negative (GC disabled)", got)
	}
}

// TestLaunchPinsProfileWithoutMount pins the snapshot for run provenance,
// while the member home remains the only environment mount.
func TestLaunchPinsProfileWithoutMount(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"fake-claude", "{task}"}}}
	})
	ctx := t.Context()
	svc, err := profile.New(e.db)
	if err != nil {
		t.Fatalf("profile.New: %v", err)
	}
	snap, err := svc.Put(ctx, string(e.member.ID), "claude", []profile.File{
		{Path: "settings.json", Mode: 0o644, Content: []byte(`{"ok":true}`)},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "with profile", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	got, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ProfileSnapshotID != snap.ID {
		t.Fatalf("pinned %q, want %q", got.ProfileSnapshotID, snap.ID)
	}
	c := e.rt.byName(string(run.ID))
	if c == nil {
		t.Fatal("no container")
	}
	if len(c.spec.Mounts) != 1 || c.spec.Mounts[0].ContainerPath != "/root" || c.spec.Mounts[0].ReadOnly {
		t.Fatalf("mounts = %v, want only writable home mount", c.spec.Mounts)
	}
}

func TestLaunchWithoutSnapshotHasOnlyHomeMount(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"fake-claude", "{task}"}}}
	})
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "no snap", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	got, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ProfileSnapshotID != "" {
		t.Fatalf("unexpected pin %q", got.ProfileSnapshotID)
	}
	c := e.rt.byName(string(run.ID))
	if c == nil || len(c.spec.Mounts) != 1 || c.spec.Mounts[0].ContainerPath != "/root" {
		t.Fatalf("mounts = %v, want only home mount", c.spec.Mounts)
	}
}

func TestCustomHarnessDefinition(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{
			"aider": {
				TUIArgs:         []string{"aider", "{task}"},
				HeadlessArgs:    []string{"aider", "-p", "{task}"},
				Executable:      "aider",
				ProfileRoot:     "/home/aether/.aider",
				CredentialPaths: []string{"/home/aether/.aider"},
				DenyNames:       []string{"auth.json"},
			},
		}
	})
	argv, prof, err := e.sched.command(t.Context(), e.member.ID, "aider", domain.LaunchHeadless, "quoted; task")
	if err != nil {
		t.Fatalf("custom command: %v", err)
	}
	if got, want := fmt.Sprint(argv), fmt.Sprint([]string{"aider", "-p", "quoted; task"}); got != want {
		t.Fatalf("argv = %s, want %s", got, want)
	}
	if prof.LocalRoot != "/home/aether/.aider" || len(prof.CredentialPaths) != 1 {
		t.Fatalf("profile = %+v", prof)
	}
}
func TestCustomHarnessRequiresDefinition(t *testing.T) {
	e := newTestEnv(t, nil)
	e.cfg.Harnesses = map[string]HarnessSpec{"aider": {TUIArgs: []string{"aider", "{task}"}}}
	if _, err := New(e.cfg); err == nil {
		t.Fatal("custom harness without executable accepted")
	} else if !strings.Contains(err.Error(), `custom harness "aider" requires an explicit definition`) {
		t.Fatalf("custom harness error = %v", err)
	}
}
func TestFakeHarnessDefinitionUsesEnvironment(t *testing.T) {
	e := newTestEnv(t, nil)
	e.cfg.Harnesses = map[string]HarnessSpec{"fake": {}}
	s, err := New(e.cfg)
	if err != nil {
		t.Fatalf("New with built-in fake harness: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	t.Setenv(fakeAgentEnv, "fake-agent {task}")
	argv, _, err := s.command(t.Context(), "", "fake", domain.LaunchTUI, "integration task")
	if err != nil {
		t.Fatalf("fake command: %v", err)
	}
	if got, want := fmt.Sprint(argv), fmt.Sprint([]string{"fake-agent", "integration task"}); got != want {
		t.Fatalf("argv = %s, want %s", got, want)
	}
}
func TestContainerAddrReturnsLiveContainerIP(t *testing.T) {
	e := newTestEnv(t, nil)
	e.rt.containerIP = "192.0.2.44"
	run, _ := e.launchFake(t, "forward")

	got, err := e.sched.ContainerAddr(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("ContainerAddr: %v", err)
	}
	if got != "192.0.2.44" {
		t.Fatalf("ContainerAddr = %q, want 192.0.2.44", got)
	}
}

func TestContainerAddrRequiresSupervisedRun(t *testing.T) {
	e := newTestEnv(t, nil)
	_, err := e.sched.ContainerAddr(t.Context(), "run_missing")
	if err == nil || err.Error() != "run has no live container" {
		t.Fatalf("ContainerAddr missing = %v, want run has no live container", err)
	}
}

func TestSuperviseWaitRetriesTransportErrorUntilExit(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	rt := newScriptedWaitRuntime(e.rt)
	e.sched.cfg.Runtime = rt
	run, c := e.launchFake(t, "wait transport retry")

	if got := waitScriptedWaitCall(t, rt); got != c.id {
		t.Fatalf("first Wait container = %q, want %q", got, c.id)
	}
	sendScriptedWaitOutcome(t, rt, scriptedWaitOutcome{err: errors.New("test: daemon socket reset")})
	if got := waitScriptedWaitCall(t, rt); got != c.id {
		t.Fatalf("retry Wait container = %q, want %q", got, c.id)
	}
	if _, err := e.sched.ContainerAddr(t.Context(), run.ID); err != nil {
		t.Fatalf("live run after transient Wait: %v", err)
	}

	sendScriptedWaitOutcome(t, rt, scriptedWaitOutcome{useUnderlying: true})
	c.output("still writable\r\n")
	waitFor(t, "run PTY output after transient Wait", func() bool {
		session := e.pty.session(run.ID)
		return session != nil && strings.Contains(session.output(), "still writable")
	})
	c.exitNow(0)
	waitStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: wait transport retry" {
		t.Fatalf("commits after eventual exit = %v", got)
	}
}

func TestSuperviseWaitCancellationDuringRetryLeavesRunLive(t *testing.T) {
	e := newTestEnv(t, nil)
	rt := newScriptedWaitRuntime(e.rt)
	e.sched.cfg.Runtime = rt
	run, c := e.launchFake(t, "wait cancellation")
	waitScriptedWaitCall(t, rt)
	sendScriptedWaitOutcome(t, rt, scriptedWaitOutcome{err: errors.New("test: daemon unavailable")})

	closed := make(chan struct{})
	go func() {
		_ = e.sched.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel Wait retry promptly")
	}
	fresh, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun after cancelled retry: %v", err)
	}
	if fresh.Status != domain.RunRunning {
		t.Fatalf("run status after cancelled retry = %s, want running", fresh.Status)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := e.rt.Wait(waitCtx, c.id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("container after cancelled retry = %v, want running", err)
	}
}
