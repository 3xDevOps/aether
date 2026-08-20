package scheduler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

const waitTimeout = 10 * time.Second

// testEnv wires a scheduler to the real store and real event bus with
// fake git/pty seams and the in-memory runtime.
type testEnv struct {
	t      *testing.T
	db     *store.DB
	bus    *events.InProc
	rt     *fakeRuntime
	git    *fakeGit
	pty    *fakePTY
	sched  *Scheduler
	cfg    Config
	ws     *domain.Workspace
	sess   *domain.Session
	member *domain.Member
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
	e.ws = &domain.Workspace{Name: "ws", Environment: domain.WorkspaceEnvironment{CustomImage: "busybox:1.36", Variables: map[string]string{"WS": "1"}}}
	if cerr := db.CreateWorkspace(ctx, e.ws); cerr != nil {
		t.Fatalf("create workspace: %v", cerr)
	}
	e.sess = &domain.Session{WorkspaceID: e.ws.ID, Name: "main effort", BaseBranch: "main"}
	if cerr := db.CreateSession(ctx, e.sess); cerr != nil {
		t.Fatalf("create session: %v", cerr)
	}
	e.member = &domain.Member{DisplayName: "Ada", PublicKey: testPublicKey(t), Color: "#e6194b", Role: domain.RoleCollaborator}
	if cerr := db.CreateMember(ctx, e.member); cerr != nil {
		t.Fatalf("create member: %v", cerr)
	}

	e.cfg = Config{
		Store:    db,
		Runtime:  e.rt,
		Bus:      bus,
		Git:      e.git,
		PTY:      e.pty,
		StateDir: filepath.Join(dir, "scheduler"),
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
// and bus — a "rebooted server" — with fresh PTY state and the given
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
		Filter: events.Filter{Session: e.sess.ID},
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
	run, err := e.sched.Launch(t.Context(), e.sess.ID, e.member.ID, task, "fake", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	c := e.rt.byName(string(run.ID))
	if c == nil {
		t.Fatalf("no container created for run %s", run.ID)
	}
	return run, c
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
	if got := c.spec.Command; len(got) != 2 || got[0] != "fake-agent" || got[1] != "fix the auth bug" {
		t.Fatalf("container command = %v", got)
	}
	if !c.spec.TTY {
		t.Fatal("container spec must set TTY")
	}
	if c.spec.Env["AETHER_RUN_ID"] != string(run.ID) || c.spec.Env["TERM"] != "xterm-256color" {
		t.Fatalf("container env = %v", c.spec.Env)
	}
	if c.spec.WorktreeHostPath != run.Worktree || c.spec.WorktreeMountPath != "/workspace" {
		t.Fatalf("worktree mount = %q -> %q", c.spec.WorktreeHostPath, c.spec.WorktreeMountPath)
	}
	if _, err := os.Stat(e.sched.sidecarPath(run.ID)); err != nil {
		t.Fatalf("sidecar missing while running: %v", err)
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
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("needs-attention reason = %q", p.Reason)
	}
	fresh := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if fresh.FinishedAt != nil {
		t.Fatal("needs-attention must not set FinishedAt")
	}
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
	if p := closed.Payload.(events.RunStatusPayload); p.Reason != "closed" {
		t.Fatalf("close reason = %q", p.Reason)
	}
	if closed.ActorID != e.member.ID {
		t.Fatalf("close actor = %s", closed.ActorID)
	}
	final := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if final.FinishedAt == nil {
		t.Fatal("terminal run must have FinishedAt")
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
	_, err := e.sched.Launch(t.Context(), e.sess.ID, e.member.ID, "task", "fake", domain.LaunchTUI)
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

	if _, err := e.sched.Launch(ctx, e.sess.ID, e.member.ID, "t", "unknown-harness", domain.LaunchTUI); err == nil {
		t.Fatal("unknown harness accepted")
	}
	if _, err := e.sched.Launch(ctx, e.sess.ID, e.member.ID, "t", "claude", domain.LaunchMode("bogus")); err == nil {
		t.Fatal("invalid mode accepted")
	}
	t.Setenv(fakeAgentEnv, "fake-agent")
	if _, err := e.sched.Launch(ctx, "sess_missing", e.member.ID, "t", "fake", domain.LaunchTUI); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing session error = %v, want ErrNotFound", err)
	}
	t.Setenv(fakeAgentEnv, "")
	if _, err := e.sched.Launch(ctx, e.sess.ID, e.member.ID, "t", "fake", domain.LaunchTUI); err == nil {
		t.Fatal("fake harness with empty AETHER_FAKE_AGENT accepted")
	}
}

func TestCommandTemplates(t *testing.T) {
	e := newTestEnv(t, nil)
	argv, profile, err := e.sched.command("claude", domain.LaunchHeadless, "do it")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--dangerously-skip-permissions", "do it"}
	if fmt.Sprint(argv) != fmt.Sprint(want) {
		t.Fatalf("claude headless argv = %v, want %v", argv, want)
	}
	if profile.Name != "claude" || len(profile.CredentialPaths) == 0 {
		t.Fatalf("claude profile = %+v, want registry profile with credential paths", profile)
	}
	if _, _, codexErr := e.sched.command("codex", domain.LaunchTUI, "x"); codexErr != nil {
		t.Fatalf("codex tui: %v", codexErr)
	}
	// A Config.Harnesses argv override replaces the registry template but
	// keeps the registry profile.
	e2 := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"my-claude", "{task}"}}}
	})
	argv, profile, err = e2.sched.command("claude", domain.LaunchTUI, "go")
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
	if _, _, err := e.sched.command("custom", domain.LaunchTUI, "x"); err == nil {
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
	if c.spec.User != "" {
		t.Errorf("user = %q, want empty (root default)", c.spec.User)
	}
	if env["HOME"] != "/root" {
		t.Errorf("HOME = %q, want /root for a root run", env["HOME"])
	}
	if len(c.spec.Mounts) != 0 {
		t.Errorf("mounts = %v, want none without HomesDir", c.spec.Mounts)
	}
}

// TestLaunchCredentialMounts pins the credential-home wiring: with a
// HomesDir configured and a registry harness with credential paths, the
// member's harness home is created on the host and mounted read-write at
// the profile's container-side credential path.
func TestLaunchCredentialMounts(t *testing.T) {
	homes := filepath.Join(t.TempDir(), "homes")
	e := newTestEnv(t, func(cfg *Config) {
		cfg.HomesDir = homes
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"fake-claude", "{task}"}}}
	})
	run, err := e.sched.Launch(t.Context(), e.sess.ID, e.member.ID, "with creds", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	c := e.rt.byName(string(run.ID))
	if c == nil {
		t.Fatal("no container created")
	}
	if len(c.spec.Mounts) != 1 {
		t.Fatalf("mounts = %v, want the claude credential home", c.spec.Mounts)
	}
	m := c.spec.Mounts[0]
	if m.ContainerPath != "/root/.claude" || m.ReadOnly {
		t.Errorf("credential mount = %+v", m)
	}
	// Mount validation canonicalizes HostPath, so the expectation must be
	// resolved too (temp dirs can sit behind symlinks).
	wantHost, err := filepath.EvalSymlinks(filepath.Join(homes, string(e.member.ID), "claude", ".claude"))
	if err != nil {
		t.Fatalf("resolve expected host path: %v", err)
	}
	if m.HostPath != wantHost {
		t.Errorf("credential host path = %q, want %q", m.HostPath, wantHost)
	}
	info, err := os.Stat(m.HostPath)
	if err != nil || !info.IsDir() {
		t.Errorf("credential home not created on host: %v", err)
	}
	// A second run of the same member+harness shares the same home.
	run2, err := e.sched.Launch(t.Context(), e.sess.ID, e.member.ID, "later run", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("second Launch: %v", err)
	}
	c2 := e.rt.byName(string(run2.ID))
	if c2.spec.Mounts[0].HostPath != wantHost {
		t.Errorf("second run home = %q, want %q", c2.spec.Mounts[0].HostPath, wantHost)
	}
}

// TestContainerSpecNonRootHome pins that a non-root run user gets
// HOME=/home/aether in the container env (Docker leaves HOME wrong for
// numeric users, and the credential mounts land under that home).
func TestContainerSpecNonRootHome(t *testing.T) {
	e := newTestEnv(t, nil)
	run := &domain.Run{ID: "run-x", SessionID: e.sess.ID, MemberID: e.member.ID}
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
// a run whose resolved uid:gid differs from a live run of the same
// member+harness fails provisioning loudly (the ownership pass would
// otherwise flip the shared home's ownership back and forth), while same
// mapping, different member or harness, and root runs all pass. The guard
// is cross-platform; only the chown itself is linux-only.
func TestReserveRunUserConflict(t *testing.T) {
	e := newTestEnv(t, nil)
	live := &supervised{
		runID:    "run-live",
		memberID: e.member.ID,
		harness:  "claude",
		runUser:  "1000:1000",
	}
	e.sched.mu.Lock()
	e.sched.runs[live.runID] = live
	e.sched.mu.Unlock()

	entry := &supervised{runID: "run-new", memberID: e.member.ID, harness: "claude"}
	err := e.sched.reserveRunUser(entry, "2000:2000", true)
	if err == nil {
		t.Fatal("conflicting uid accepted for a shared credential home")
	}
	for _, want := range []string{"1000:1000", "2000:2000", "run-live"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict error %q does not name %q", err, want)
		}
	}

	same := &supervised{runID: "run-same", memberID: e.member.ID, harness: "claude"}
	if err := e.sched.reserveRunUser(same, "1000:1000", true); err != nil {
		t.Fatalf("same mapping rejected: %v", err)
	}
	if same.runUser != "1000:1000" {
		t.Errorf("runUser = %q, want recorded 1000:1000", same.runUser)
	}

	otherHarness := &supervised{runID: "run-codex", memberID: e.member.ID, harness: "codex"}
	if err := e.sched.reserveRunUser(otherHarness, "2000:2000", true); err != nil {
		t.Fatalf("different harness rejected: %v", err)
	}

	root := &supervised{runID: "run-root", memberID: e.member.ID, harness: "claude"}
	if err := e.sched.reserveRunUser(root, "", true); err != nil {
		t.Fatalf("root run rejected: %v", err)
	}

	noHome := &supervised{runID: "run-nohome", memberID: e.member.ID, harness: "claude"}
	if err := e.sched.reserveRunUser(noHome, "2000:2000", false); err != nil {
		t.Fatalf("run without credential mounts rejected: %v", err)
	}
}

func TestLegalTransitions(t *testing.T) {
	allowed := map[[2]domain.RunStatus]bool{}
	for _, from := range domain.AllRunStatuses {
		if from.Terminal() {
			continue // terminal states never transition; verified below
		}
		allowed[[2]domain.RunStatus{from, domain.RunAbandoned}] = true
		allowed[[2]domain.RunStatus{from, domain.RunInterrupted}] = true
	}
	allowed[[2]domain.RunStatus{domain.RunQueued, domain.RunProvisioning}] = true
	allowed[[2]domain.RunStatus{domain.RunProvisioning, domain.RunRunning}] = true
	allowed[[2]domain.RunStatus{domain.RunProvisioning, domain.RunFailed}] = true
	allowed[[2]domain.RunStatus{domain.RunRunning, domain.RunNeedsAttention}] = true
	allowed[[2]domain.RunStatus{domain.RunRunning, domain.RunFailed}] = true
	allowed[[2]domain.RunStatus{domain.RunNeedsAttention, domain.RunRunning}] = true
	allowed[[2]domain.RunStatus{domain.RunNeedsAttention, domain.RunNeedsAttention}] = true
	allowed[[2]domain.RunStatus{domain.RunNeedsAttention, domain.RunFailed}] = true
	allowed[[2]domain.RunStatus{domain.RunNeedsAttention, domain.RunMerged}] = true

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
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("CloseRun on running run: %v, want ErrInvalidTransition", err)
	}
	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Relaunch on running run: %v, want ErrInvalidTransition", err)
	}
	if err := e.sched.Resume(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Resume on unpaused run: %v, want ErrInvalidTransition", err)
	}

	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunFailed); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("CloseRun with outcome failed: %v, want ErrInvalidTransition", err)
	}
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("CloseRun on terminal run: %v, want ErrInvalidTransition", err)
	}
	if err := e.sched.Kill(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Kill on terminal run: %v, want ErrInvalidTransition", err)
	}
	if err := e.sched.Pause(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Pause on finished run: %v, want ErrInvalidTransition", err)
	}
	if err := e.sched.Inject(ctx, run.ID, e.member.ID, "hi"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Inject on finished run: %v, want ErrInvalidTransition", err)
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

// TestLaunchProfileAndCredentialMounts pins a snapshot, mounts a writable
// materialization at LocalRoot, and keeps credential files on a separate
// host path under the member home (nested via AllowedNestings).
func TestLaunchProfileAndCredentialMounts(t *testing.T) {
	dir := t.TempDir()
	homes := filepath.Join(dir, "homes")
	e := newTestEnv(t, func(cfg *Config) {
		cfg.HomesDir = homes
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"fake-claude", "{task}"}}}
	})
	ctx := t.Context()
	svc, err := profile.New(e.db, filepath.Join(dir, "profiles"))
	if err != nil {
		t.Fatalf("profile.New: %v", err)
	}
	// The env already auto-wired a service against the same store; Put
	// through either instance is visible to Latest/PinRun.
	snap, err := svc.Put(ctx, string(e.member.ID), "claude", []profile.File{
		{Path: "settings.json", Mode: 0o644, Content: []byte(`{"ok":true}`)},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	run, err := e.sched.Launch(ctx, e.sess.ID, e.member.ID, "with profile", "claude", domain.LaunchTUI)
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
	if len(c.spec.Mounts) < 2 {
		t.Fatalf("mounts = %v, want profile parent plus credential children", c.spec.Mounts)
	}
	prof := c.spec.Mounts[0]
	if prof.ContainerPath != "/root/.claude" || prof.ReadOnly {
		t.Errorf("profile mount = %+v", prof)
	}
	wantProf, err := filepath.EvalSymlinks(filepath.Join(e.sched.cfg.ProfilesDir, "runs", string(run.ID)))
	if err != nil {
		t.Fatalf("resolve profile dest: %v", err)
	}
	if prof.HostPath != wantProf {
		t.Errorf("profile host = %q, want %q", prof.HostPath, wantProf)
	}
	seed, err := os.ReadFile(filepath.Join(wantProf, "settings.json"))
	if err != nil || string(seed) != `{"ok":true}` {
		t.Errorf("materialized settings = %q err %v", seed, err)
	}
	if err = os.WriteFile(filepath.Join(wantProf, "session.json"), []byte("run-local"), 0o644); err != nil {
		t.Fatalf("write dest: %v", err)
	}

	var credHost string
	for _, m := range c.spec.Mounts[1:] {
		if !strings.HasPrefix(m.ContainerPath, "/root/.claude/") {
			t.Errorf("credential child %q is not nested under the profile", m.ContainerPath)
		}
		if m.HostPath == prof.HostPath {
			t.Errorf("credential host collides with profile dest")
		}
		if !strings.Contains(m.HostPath, string(e.member.ID)) {
			t.Errorf("credential host %q is not under the member home", m.HostPath)
		}
		credHost = m.HostPath
	}
	if credHost == "" {
		t.Fatal("no credential child mounts")
	}
	if filepath.Dir(credHost) == wantProf {
		t.Fatal("credential files live in the ephemeral dest")
	}

	// A second run gets its own dest; writing the first does not change it.
	run2, err := e.sched.Launch(ctx, e.sess.ID, e.member.ID, "later", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("second Launch: %v", err)
	}
	c2 := e.rt.byName(string(run2.ID))
	if c2.spec.Mounts[0].HostPath == prof.HostPath {
		t.Errorf("runs share a profile dest")
	}
	if _, err := os.Stat(filepath.Join(c2.spec.Mounts[0].HostPath, "session.json")); !os.IsNotExist(err) {
		t.Errorf("second dest inherited first run's write: %v", err)
	}
}

func TestLaunchWithoutSnapshotSkipsProfileMount(t *testing.T) {
	homes := filepath.Join(t.TempDir(), "homes")
	e := newTestEnv(t, func(cfg *Config) {
		cfg.HomesDir = homes
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"fake-claude", "{task}"}}}
	})
	run, err := e.sched.Launch(t.Context(), e.sess.ID, e.member.ID, "no snap", "claude", domain.LaunchTUI)
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
}

func TestCombineProfileCredentialsNested(t *testing.T) {
	parent := &runtime.Mount{HostPath: "/data/profiles/runs/r1", ContainerPath: "/root/.claude"}
	creds := []runtime.Mount{{HostPath: "/data/homes/m/claude/.claude/creds", ContainerPath: "/root/.claude/creds"}}
	mounts, nestings, err := combineProfileCredentials(parent, creds, nil)
	if err != nil {
		t.Fatalf("combine: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("mounts = %v", mounts)
	}
	if nestings["/root/.claude/creds"] != "/root/.claude" {
		t.Fatalf("nestings = %v", nestings)
	}
	if mounts[0].ContainerPath != "/root/.claude" {
		t.Fatalf("parent not first: %v", mounts)
	}
}
func TestCustomHarnessDefinition(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{
			"omp": {
				TUIArgs:         []string{"omp", "{task}"},
				HeadlessArgs:    []string{"omp", "-p", "{task}"},
				Executable:      "omp",
				ProfileRoot:     "/home/aether/.omp",
				CredentialPaths: []string{"/home/aether/.omp"},
				DenyNames:       []string{"auth.json"},
			},
		}
	})
	argv, prof, err := e.sched.command("omp", domain.LaunchHeadless, "quoted; task")
	if err != nil {
		t.Fatalf("custom command: %v", err)
	}
	if got, want := fmt.Sprint(argv), fmt.Sprint([]string{"omp", "-p", "quoted; task"}); got != want {
		t.Fatalf("argv = %s, want %s", got, want)
	}
	if prof.LocalRoot != "/home/aether/.omp" || len(prof.CredentialPaths) != 1 {
		t.Fatalf("profile = %+v", prof)
	}
}
func TestCustomHarnessRequiresDefinition(t *testing.T) {
	e := newTestEnv(t, nil)
	e.cfg.Harnesses = map[string]HarnessSpec{"omp": {TUIArgs: []string{"omp", "{task}"}}}
	if _, err := New(e.cfg); err == nil {
		t.Fatal("custom harness without executable accepted")
	} else if !strings.Contains(err.Error(), `custom harness "omp" requires an explicit definition`) {
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
	argv, _, err := s.command("fake", domain.LaunchTUI, "integration task")
	if err != nil {
		t.Fatalf("fake command: %v", err)
	}
	if got, want := fmt.Sprint(argv), fmt.Sprint([]string{"fake-agent", "integration task"}); got != want {
		t.Fatalf("argv = %s, want %s", got, want)
	}
}
