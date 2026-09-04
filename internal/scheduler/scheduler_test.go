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
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/profile"
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
	e.ws = &domain.Workspace{
		Name:        "ws",
		BaseBranch:  "main",
		Environment: domain.WorkspaceEnvironment{CustomImage: "busybox:1.36", Variables: map[string]string{"WS": "1"}},
	}
	if cerr := db.CreateWorkspace(ctx, e.ws); cerr != nil {
		t.Fatalf("create workspace: %v", cerr)
	}
	e.member = &domain.Member{DisplayName: "Ada", PublicKey: testPublicKey(t), Color: "#e6194b", Role: domain.RoleCollaborator}
	if cerr := db.CreateMember(ctx, e.member); cerr != nil {
		t.Fatalf("create member: %v", cerr)
	}

	homes, err := memberhome.New(filepath.Join(dir, "homes"))
	if err != nil {
		t.Fatalf("memberhome.New: %v", err)
	}
	e.cfg = Config{
		Store:    db,
		Runtime:  e.rt,
		Bus:      bus,
		Git:      e.git,
		PTY:      e.pty,
		StateDir: filepath.Join(dir, "scheduler"),
		Homes:    homes,
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
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, task, "fake", domain.LaunchTUI)
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
	_, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, "task", "fake", domain.LaunchTUI)
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

	if _, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, "t", "unknown-harness", domain.LaunchTUI); err == nil {
		t.Fatal("unknown harness accepted")
	}
	if _, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, "t", "claude", domain.LaunchMode("bogus")); err == nil {
		t.Fatal("invalid mode accepted")
	}
	t.Setenv(fakeAgentEnv, "fake-agent")
	if _, err := e.sched.Launch(ctx, "ws_missing", e.member.ID, "t", "fake", domain.LaunchTUI); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing workspace error = %v, want ErrNotFound", err)
	}
	t.Setenv(fakeAgentEnv, "")
	if _, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, "t", "fake", domain.LaunchTUI); err == nil {
		t.Fatal("fake harness with empty AETHER_FAKE_AGENT accepted")
	}
}

func TestCommandTemplates(t *testing.T) {
	e := newTestEnv(t, nil)
	argv, profile, err := e.sched.command(t.Context(), e.member.ID, "claude", domain.LaunchHeadless, "do it")
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

// TestLaunchMountsPersistentHome pins that every launch for one member uses
// the same writable server-owned home at the container's HOME.
func TestLaunchMountsPersistentHome(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{"claude": {TUIArgs: []string{"fake-claude", "{task}"}}}
	})
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, "with home", "claude", domain.LaunchTUI)
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
	run2, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, "later run", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("second Launch: %v", err)
	}
	c2 := e.rt.byName(string(run2.ID))
	if c2 == nil || len(c2.spec.Mounts) != 1 || c2.spec.Mounts[0].HostPath != wantHome {
		t.Fatalf("second home mount = %+v, want %q", c2.spec.Mounts, wantHome)
	}
}

func TestCheckAgentPresentRefusesMissingExecutable(t *testing.T) {
	e := newTestEnv(t, nil)
	ws := &domain.Workspace{Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	p, ok := harness.Lookup("claude")
	if !ok {
		t.Fatal("claude profile missing")
	}
	err := e.sched.checkAgentPresent(t.Context(), e.member.ID, ws, "claude", "claude")
	if err == nil {
		t.Fatal("missing agent accepted")
	}
	want := fmt.Sprintf("scheduler: agent %q is not installed in your environment: %q is not in ~/.local/bin; open your terminal (aether terminal) and run: %s",
		"claude", "claude", p.InstallScript)
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestCheckAgentPresentSkipsDeploymentAndCustomImages(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses = map[string]HarnessSpec{"deployment": {
			TUIArgs:      []string{"deployment", "{task}"},
			HeadlessArgs: []string{"deployment", "{task}"},
			Executable:   "deployment",
		}}
	})
	neutral := &domain.Workspace{Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	if err := e.sched.checkAgentPresent(t.Context(), e.member.ID, neutral, "deployment", "missing"); err != nil {
		t.Fatalf("deployment harness was checked: %v", err)
	}
	if err := e.sched.checkAgentPresent(t.Context(), e.member.ID, e.ws, "claude", "missing"); err != nil {
		t.Fatalf("custom image was checked: %v", err)
	}
}

func TestCheckAgentPresentAcceptsAbsoluteSymlink(t *testing.T) {
	e := newTestEnv(t, nil)
	ws := &domain.Workspace{Environment: domain.WorkspaceEnvironment{NeutralImage: true}}
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatalf("member home: %v", err)
	}
	if mkErr := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o700); mkErr != nil {
		t.Fatalf("create agent bin: %v", mkErr)
	}
	// The claude native installer leaves ~/.local/bin/claude as a symlink
	// to an absolute versioned path that only resolves in the container.
	if lnErr := os.Symlink("/root/.local/share/claude/versions/1.0.0", filepath.Join(home, ".local", "bin", "claude")); lnErr != nil {
		t.Fatalf("symlink agent: %v", lnErr)
	}
	if err := e.sched.checkAgentPresent(t.Context(), e.member.ID, ws, "claude", "claude"); err != nil {
		t.Fatalf("symlinked agent refused: %v", err)
	}
}

func TestLaunchNeutralImageWithHomeExecutable(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.NeutralImage = "neutral:latest" })
	e.ws.Environment = domain.WorkspaceEnvironment{NeutralImage: true}
	if err := e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatalf("update workspace: %v", err)
	}
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatalf("member home: %v", err)
	}
	if mkErr := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o700); mkErr != nil {
		t.Fatalf("create agent bin: %v", mkErr)
	}
	if writeErr := os.WriteFile(filepath.Join(home, ".local", "bin", "claude"), []byte("#!/bin/sh\n"), 0o755); writeErr != nil {
		t.Fatalf("write agent: %v", writeErr)
	}
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, "neutral", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if run.Status != domain.RunRunning {
		t.Fatalf("run status = %s, want running", run.Status)
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

	same := &supervised{runID: "run-same", memberID: e.member.ID, harness: "claude"}
	if err := e.sched.reserveRunUser(same, "1000:1000", true); err != nil {
		t.Fatalf("same mapping rejected: %v", err)
	}
	if same.runUser != "1000:1000" {
		t.Errorf("runUser = %q, want recorded 1000:1000", same.runUser)
	}

	otherMember := &supervised{runID: "run-other-member", memberID: "other-member", harness: "codex"}
	if err := e.sched.reserveRunUser(otherMember, "2000:2000", true); err != nil {
		t.Fatalf("different member rejected: %v", err)
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
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, "with profile", "claude", domain.LaunchTUI)
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
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, "no snap", "claude", domain.LaunchTUI)
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
	argv, prof, err := e.sched.command(t.Context(), e.member.ID, "omp", domain.LaunchHeadless, "quoted; task")
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
	argv, _, err := s.command(t.Context(), "", "fake", domain.LaunchTUI, "integration task")
	if err != nil {
		t.Fatalf("fake command: %v", err)
	}
	if got, want := fmt.Sprint(argv), fmt.Sprint([]string{"fake-agent", "integration task"}); got != want {
		t.Fatalf("argv = %s, want %s", got, want)
	}
}
