package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// startScheduler runs sched.Start in the background for the duration of
// the test; assertions poll for recovery's effects.
func startScheduler(t *testing.T, sched *Scheduler) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func TestRebootRecoveryResumesSupervision(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)

	run, c := e.launchFake(t, "survive the reboot")
	// "Reboot": the first scheduler dies without finalizing; the container
	// keeps running (Docker semantics for a daemonless host process loss).
	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	startScheduler(t, s2)

	// The new instance re-attached: a fresh PTY session exists and output
	// flows into it.
	waitFor(t, "resumed pty session", func() bool { return pty2.session(run.ID) != nil })
	c.output("back online\r\n")
	waitFor(t, "output after recovery", func() bool {
		sess := pty2.session(run.ID)
		return sess != nil && sess.output() != ""
	})

	r, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunRunning {
		t.Fatalf("recovered run status = %s, want running", r.Status)
	}

	// Exit under the new instance completes the lifecycle normally.
	c.exitNow(0)
	ev := waitStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("reason = %q", p.Reason)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: survive the reboot" {
		t.Fatalf("commits = %v", got)
	}
}

// TestRecoveryOfLegacySidecarKeepsWorkspaceScope pins the upgrade path:
// a sidecar written before runs hung off workspaces carries session_id and
// no workspace_id. It must still decode, and the resumed supervision must
// publish under the run row's workspace - the subscription here is
// workspace-filtered, so receiving the exit event is the proof.
func TestRecoveryOfLegacySidecarKeepsWorkspaceScope(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)

	run, c := e.launchFake(t, "written by an older build")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(e.sched.sidecarPath(run.ID))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var fields map[string]any
	if decErr := json.Unmarshal(raw, &fields); decErr != nil {
		t.Fatalf("decode sidecar: %v", decErr)
	}
	delete(fields, "workspace_id")
	fields["session_id"] = "sess_legacy"
	legacy, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal legacy sidecar: %v", err)
	}
	if err := os.WriteFile(e.sched.sidecarPath(run.ID), legacy, 0o644); err != nil {
		t.Fatalf("write legacy sidecar: %v", err)
	}

	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	waitFor(t, "supervision resumed", func() bool {
		_, watching := e.git.watchingFor(run.ID)
		return watching
	})

	c.exitNow(0)
	ev := waitStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if ev.WorkspaceID != e.ws.ID {
		t.Fatalf("recovered event workspace = %q, want %q", ev.WorkspaceID, e.ws.ID)
	}
}

func TestRecoveryOfLegacySidecarCapturesHomeForImages(t *testing.T) {
	e := newTestEnv(t, nil)
	run, container := e.launchFake(t, "legacy image")
	container.mu.Lock()
	container.spec.Env["HOME"] = "/home/recovered"
	container.mu.Unlock()
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("readSidecar: %v", err)
	}
	sc.Home = ""
	if writeErr := e.sched.writeSidecar(sc); writeErr != nil {
		t.Fatalf("write legacy sidecar: %v", writeErr)
	}

	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	waitFor(t, "legacy run supervision", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		entry := s2.runs[run.ID]
		return entry != nil && entry.home == "/home/recovered"
	})

	data := []byte("legacy recovered image")
	path, err := s2.SaveTerminalImage(t.Context(), e.member.ID, run.ID, ".png", data)
	if err != nil {
		t.Fatalf("SaveTerminalImage: %v", err)
	}
	if got := readSavedTerminalImage(t, e, e.member.ID, path); string(got) != string(data) {
		t.Fatalf("saved image = %q, want %q", got, data)
	}
	if !strings.HasPrefix(path, "/home/recovered/.aether/terminal-images/") {
		t.Fatalf("image path = %q, want recovered HOME", path)
	}
	persisted, err := s2.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read recovered sidecar: %v", err)
	}
	if persisted.Home != "/home/recovered" {
		t.Fatalf("persisted HOME = %q, want /home/recovered", persisted.Home)
	}
}

type gatedRecoveryPTY struct {
	*fakePTY
	started chan struct{}
	release chan struct{}
}

func (p *gatedRecoveryPTY) StartSession(ctx context.Context, key ptyhost.SessionKey, att runtime.Attachment) error {
	if err := p.fakePTY.StartSession(ctx, key, att); err != nil {
		return err
	}
	close(p.started)
	<-p.release
	return nil
}

func TestRecoveryPublishesRunBeforePTYStartReturns(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "publish before pty return")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	gated := &gatedRecoveryPTY{
		fakePTY: newFakePTY(),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	s2 := e.newScheduler(t, e.rt, gated.fakePTY)
	s2.cfg.PTY = gated
	startScheduler(t, s2)
	t.Cleanup(func() { close(gated.release) })
	select {
	case <-gated.started:
	case <-time.After(waitTimeout):
		t.Fatal("recovery never published PTY session")
	}
	if err := s2.Inject(t.Context(), run.ID, e.member.ID, "inject while PTY starts"); err != nil {
		t.Fatalf("Inject during PTY start: %v", err)
	}
}

func TestRebootRecoveryContainerGone(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)

	run, _ := e.launchFake(t, "lost to the reboot")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The container did not survive: a fresh, empty runtime.
	s2 := e.newScheduler(t, newFakeRuntime(), newFakePTY())
	startScheduler(t, s2)

	ev := waitStatusEvent(t, sub, run.ID, domain.RunInterrupted)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "server restarted" {
		t.Fatalf("interrupted reason = %q", p.Reason)
	}
	r := e.waitStoreStatus(t, run.ID, domain.RunInterrupted)
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "wip: lost to the reboot" {
		t.Fatalf("commits = %v", got)
	}
	if e.git.publishedCount(run.ID) == 0 {
		t.Fatal("branch not published during recovery")
	}
	if r.Worktree == "" {
		t.Fatal("interrupted run must keep its worktree")
	}
	if _, err := os.Stat(r.Worktree); err != nil {
		t.Fatalf("checkout must survive recovery: %v", err)
	}
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(s2.sidecarPath(run.ID))
		return os.IsNotExist(err)
	})
}

func TestRebootRecoveryQueuedAndMissingSidecar(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	queued := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "never started",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunQueued,
	}
	if err := e.db.CreateRun(ctx, queued); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	orphan := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "no sidecar",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunRunning,
	}
	if err := e.db.CreateRun(ctx, orphan); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	startScheduler(t, e.sched)
	e.waitStoreStatus(t, queued.ID, domain.RunInterrupted)
	e.waitStoreStatus(t, orphan.ID, domain.RunInterrupted)
}

// TestRecoveryFindsContainerByCreationKey pins the narrow crash window
// between Runtime.Create and the sidecar write: no sidecar exists, but the
// container carries the run ID as its creation key, so recovery finds and
// destroys it instead of leaking a running agent into the checkout.
func TestRecoveryFindsContainerByCreationKey(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	r := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "narrow window",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunQueued,
	}
	if err := e.db.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := e.db.UpdateRunStatus(ctx, r.ID, domain.RunProvisioning, "", nil, nil); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	// The container was created (creation key = run ID) but the crash hit
	// before the sidecar write: no sidecar file exists.
	cid, err := e.rt.Create(ctx, runtime.Spec{
		Name: string(r.ID), Image: "busybox:1.36", Command: []string{"fake-agent"},
		TTY: true, CreationKey: string(r.ID),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if serr := e.rt.Start(ctx, cid); serr != nil {
		t.Fatalf("Start: %v", serr)
	}

	startScheduler(t, e.sched)
	e.waitStoreStatus(t, r.ID, domain.RunInterrupted)
	waitFor(t, "orphaned container destroyed", func() bool { return e.rt.byName(string(r.ID)) == nil })
}


func TestCheckoutGC(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.CheckoutTTL = time.Hour
	})
	ctx := t.Context()

	// An expired terminal run and a fresh one.
	mk := func(task string, finished time.Time) *domain.Run {
		r := &domain.Run{
			WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: task,
			Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunQueued,
		}
		if err := e.db.CreateRun(ctx, r); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		path, _, err := e.git.CreateRunCheckout(ctx, e.ws.ID, r.ID, "main", task, "")
		if err != nil {
			t.Fatalf("CreateRunCheckout: %v", err)
		}
		r.Worktree, r.Branch = path, "aether/run-"+string(r.ID)
		if err := e.db.UpdateRun(ctx, r); err != nil {
			t.Fatalf("UpdateRun: %v", err)
		}
		if err := e.db.UpdateRunStatus(ctx, r.ID, domain.RunAbandoned, "", nil, &finished); err != nil {
			t.Fatalf("UpdateRunStatus: %v", err)
		}
		return r
	}
	expired := mk("old", time.Now().UTC().Add(-2*time.Hour))
	fresh := mk("new", time.Now().UTC())

	e.sched.sweepCheckouts(ctx)

	if _, err := os.Stat(e.git.checkoutPath(expired.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired checkout not removed: %v", err)
	}
	r, err := e.db.GetRun(ctx, expired.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Worktree != "" {
		t.Fatalf("expired run worktree = %q, want cleared", r.Worktree)
	}
	if _, statErr := os.Stat(e.git.checkoutPath(fresh.ID)); statErr != nil {
		t.Fatalf("fresh checkout must survive: %v", statErr)
	}
	r2, err := e.db.GetRun(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("GetRun fresh: %v", err)
	}
	if r2.Worktree == "" {
		t.Fatal("fresh run worktree cleared prematurely")
	}
}

func TestRecoveryProbeErrorRetainsRunAndContainer(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "inconclusive recovery")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	e.rt.waitErr = errors.New("runtime API unavailable")

	stored, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	before := e.rt.attachCount()
	s2.recoverSupervised(t.Context(), stored)

	after, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun after recovery: %v", err)
	}
	if after.Status != domain.RunRunning {
		t.Fatalf("status = %s, want running", after.Status)
	}
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("inconclusive probe destroyed the live container")
	}
	if e.rt.attachCount() != before {
		t.Fatal("inconclusive probe must not attach")
	}
	if _, err := os.Stat(s2.sidecarPath(run.ID)); err != nil {
		t.Fatalf("sidecar removed after inconclusive probe: %v", err)
	}
}

func TestCrashExitBeforeMarker(t *testing.T) {
	// (a) Runtime has recorded exit, no exit_observed marker yet: the
	// startup probe finalizes the original outcome once.
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)

	run, c := e.launchFake(t, "crash before marker")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	c.exitNow(0)

	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	before := e.rt.attachCount()
	startScheduler(t, s2)

	ev := waitStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: crash before marker" {
		t.Fatalf("commits = %v", got)
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(s2.sidecarPath(run.ID))
		return os.IsNotExist(err)
	})
	if e.rt.attachCount() != before {
		t.Fatalf("reattached stopped container: attaches %d -> %d", before, e.rt.attachCount())
	}
	if pty2.session(run.ID) != nil {
		t.Fatal("must not start a PTY session for a stopped container")
	}
}

func TestCrashExitAfterMarkerBeforeStatus(t *testing.T) {
	// (b) Marker fsynced, commit/status not done: startup finalizes from
	// the marker once with the original outcome.
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)

	run, _ := e.launchFake(t, "crash after marker")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("readSidecar: %v", err)
	}
	sc.ExitObserved = true
	sc.ExitCode = 0
	data, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	if err := os.WriteFile(e.sched.sidecarPath(run.ID), data, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	before := e.rt.attachCount()
	startScheduler(t, s2)

	ev := waitStatusEvent(t, sub, run.ID, domain.RunCompleted)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: crash after marker" {
		t.Fatalf("commits = %v", got)
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	if e.rt.attachCount() != before {
		t.Fatalf("reattached after marker: attaches %d -> %d", before, e.rt.attachCount())
	}
	if pty2.session(run.ID) != nil {
		t.Fatal("must not Attach when exit_observed is set")
	}
}

func TestCrashExitAfterStatusBeforeDestroy(t *testing.T) {
	// (c) Status already completed, destroy not done: cleanup the leftover
	// container without reattaching or marking the run interrupted.
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "crash after status")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("readSidecar: %v", err)
	}
	sc.ExitObserved = true
	sc.ExitCode = 0
	data, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	if err = os.WriteFile(e.sched.sidecarPath(run.ID), data, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	now := time.Now().UTC()
	if err = e.db.UpdateRunStatus(ctx, run.ID, domain.RunCompleted, "", nil, &now); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	c.exitNow(0)

	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	before := e.rt.attachCount()
	startScheduler(t, s2)

	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	waitFor(t, "sidecar removed", func() bool {
		_, statErr := os.Stat(s2.sidecarPath(run.ID))
		return os.IsNotExist(statErr)
	})
	fresh, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if fresh.Status != domain.RunCompleted {
		t.Fatalf("status = %s, want completed (not interrupted)", fresh.Status)
	}
	if e.rt.attachCount() != before {
		t.Fatalf("reattached leftover container: attaches %d -> %d", before, e.rt.attachCount())
	}
	if pty2.session(run.ID) != nil {
		t.Fatal("must not reattach a leftover stopped container")
	}
}

func TestTUICloseRelaunchKeepsExactRunAndContainer(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, container := e.launchFake(t, "retain this terminal")
	containerID := container.id
	worktree := run.Worktree

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	closed := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if closed.Reason != retainedCloseReason {
		t.Fatalf("close reason = %q, want %q", closed.Reason, retainedCloseReason)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read retained sidecar: %v", err)
	}
	if !sc.Retained || sc.Mode != domain.LaunchTUI || sc.RetainedUntil == nil {
		t.Fatalf("retained sidecar = %+v", sc)
	}

	reopened, err := e.sched.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if reopened.ID != run.ID || reopened.Worktree != worktree || reopened.Status != domain.RunRunning {
		t.Fatalf("reopened run = %+v, want same running row/worktree", reopened)
	}
	if got := e.rt.byName(string(run.ID)); got == nil || got.id != containerID {
		t.Fatalf("relaunch replaced container: got %v, want %s", got, containerID)
	}
	if _, watching := e.git.watchingFor(run.ID); !watching {
		t.Fatal("relaunch did not restart diff watch")
	}
	if reopened.FinishedAt != nil {
		t.Fatalf("reopened FinishedAt = %v, want nil", reopened.FinishedAt)
	}
}

func TestRetainedExpiryDestroysContainerAndHidesRelaunch(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "expire this terminal")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	past := time.Now().UTC().Add(-time.Second)
	entry.retainedUntil = &past
	if err := e.sched.writeSidecar(entry.sidecar()); err != nil {
		t.Fatalf("write expired sidecar: %v", err)
	}
	e.sched.mu.Unlock()

	e.sched.sweepRetained(ctx)
	waitFor(t, "expired container destroyed", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	expired := e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
	if expired.Reason != retainedExpiredReason {
		t.Fatalf("expiry reason = %q, want %q", expired.Reason, retainedExpiredReason)
	}
	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) ||
		!strings.Contains(err.Error(), retainedUnavailableReason) {
		t.Fatalf("expired Relaunch error = %v, want retained-unavailable invalid transition", err)
	}
}

func TestNegativeRetentionDestroysAndRejectsRelaunch(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()

	fallback, _ := e.launchFake(t, "negative fallback")
	if err := e.sched.CloseRun(ctx, fallback.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun fallback: %v", err)
	}
	e.sched.cfg.RunContainerTTL = -time.Second
	if !e.sched.RetainsContainer(ctx, fallback.ID) {
		t.Fatal("negative-TTL retained sidecar lost ownership before destruction")
	}
	if _, err := e.sched.Relaunch(ctx, fallback.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) ||
		!strings.Contains(err.Error(), retainedUnavailableReason) {
		t.Fatalf("negative fallback Relaunch error = %v, want retained-unavailable invalid transition", err)
	}
	waitFor(t, "negative fallback container destroyed", func() bool {
		return e.rt.byName(string(fallback.ID)) == nil
	})
	fallbackRow := e.waitStoreStatus(t, fallback.ID, domain.RunMerged)
	if fallbackRow.Reason != retainedExpiredReason {
		t.Fatalf("negative fallback reason = %q, want %q", fallbackRow.Reason, retainedExpiredReason)
	}
	if _, err := os.Stat(e.sched.sidecarPath(fallback.ID)); !os.IsNotExist(err) {
		t.Fatalf("negative fallback sidecar still exists: %v", err)
	}

	e.sched.cfg.RunContainerTTL = time.Hour
	boot, _ := e.launchFake(t, "negative boot")
	if err := e.sched.CloseRun(ctx, boot.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatalf("CloseRun boot: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	e.cfg.RunContainerTTL = -time.Second
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	waitFor(t, "negative boot container destroyed", func() bool {
		return e.rt.byName(string(boot.ID)) == nil
	})
	waitFor(t, "negative boot reason", func() bool {
		row, err := e.db.GetRun(ctx, boot.ID)
		return err == nil && row.Reason == retainedExpiredReason
	})
	if _, err := s2.Relaunch(ctx, boot.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("negative boot Relaunch error = %v, want ErrInvalidTransition", err)
	}
	if _, err := os.Stat(s2.sidecarPath(boot.ID)); !os.IsNotExist(err) {
		t.Fatalf("negative boot sidecar still exists: %v", err)
	}
}

type destroyRetryRuntime struct {
	runtime.Runtime
	mu       sync.Mutex
	failures int
	destroys int
	waits    int
}

func (r *destroyRetryRuntime) Destroy(ctx context.Context, id runtime.ID) error {
	r.mu.Lock()
	r.destroys++
	if r.failures == 0 {
		r.failures++
		r.mu.Unlock()
		return errors.New("destroy temporarily unavailable")
	}
	r.mu.Unlock()
	return r.Runtime.Destroy(ctx, id)
}

func (r *destroyRetryRuntime) Wait(ctx context.Context, id runtime.ID) (runtime.ExitStatus, error) {
	r.mu.Lock()
	r.waits++
	r.mu.Unlock()
	return r.Runtime.Wait(ctx, id)
}

func (r *destroyRetryRuntime) counts() (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures, r.destroys, r.waits
}

func TestBootRetainedDestroyFailureRetriesOnSweep(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
		cfg.PollInterval = 200 * time.Millisecond
	})
	ctx := t.Context()
	run, container := e.launchFake(t, "retry boot destroy")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	e.cfg.RunContainerTTL = -time.Second
	retry := &destroyRetryRuntime{Runtime: e.rt}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = retry
	startScheduler(t, s2)

	waitFor(t, "boot destroy failure", func() bool {
		failures, _, _ := retry.counts()
		return failures == 1
	})
	if e.rt.byName(string(run.ID)) != container {
		t.Fatal("failed boot destroy must retain the container")
	}

	var owner *supervised
	waitFor(t, "retained retry owner", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		owner = s2.runs[run.ID]
		return owner != nil && owner.retained && owner.retainedUntil != nil && owner.waitStarted
	})
	sc, err := s2.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read retained retry sidecar: %v", err)
	}
	if sc.ContainerID != string(container.id) || !sc.Retained || sc.RetainedUntil == nil ||
		time.Now().UTC().Before(*sc.RetainedUntil) {
		t.Fatalf("retry sidecar = %+v, want due retained owner", sc)
	}

	waitFor(t, "sweep destroy retry", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	waitFor(t, "retained owner dropped", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[run.ID] == nil
	})
	select {
	case <-owner.done:
	default:
		t.Fatal("retained owner done channel is still open")
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
	if row.Reason != retainedExpiredReason {
		t.Fatalf("boot retry reason = %q, want %q", row.Reason, retainedExpiredReason)
	}
	if _, err := os.Stat(s2.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("boot retry sidecar still exists: %v", err)
	}
	failures, destroys, waits := retry.counts()
	if failures != 1 || destroys < 2 || waits > 2 {
		t.Fatalf("destroy/wait calls = failures %d, destroys %d, waits %d; want one failed destroy, retry, and at most one Wait owner", failures, destroys, waits)
	}
	if _, err := s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Relaunch after retry = %v, want ErrInvalidTransition", err)
	}
}

func TestRetainedTUIRebootsAndReopensSameContainer(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, container := e.launchFake(t, "reboot retained terminal")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)
	reopened, err := s2.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch after reboot: %v", err)
	}
	if reopened.ID != run.ID {
		t.Fatalf("reopened ID = %s, want %s", reopened.ID, run.ID)
	}
	if got := e.rt.byName(string(run.ID)); got == nil || got.id != container.id {
		t.Fatalf("reboot relaunch replaced container: got %v, want %s", got, container.id)
	}
}


// TestRecoveryKeepsARunParkedForItsMember covers what a restart must not do
// to a run that is waiting for the member. Nothing has been observed on the
// terminal since the restart, so a run parked seconds before it comes back
// as Working on the first poll unless un-parking takes activity that was
// actually seen. And the reporter comes back with the run - it is recorded
// at launch, not recomputed - so once the recovered agent says it is
// waiting again, a repaint while the member types still does not release
// it.
func TestRecoveryKeepsARunParkedForItsMember(t *testing.T) {
	e := newReportingEnv(t, func(cfg *Config) {
		// Far longer than the test: nothing here is a stall.
		cfg.StallThreshold = time.Hour
		cfg.PollInterval = 10 * time.Millisecond
	})
	run, c := e.launchReporting(t)
	waiting := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonInput}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report waiting: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	startScheduler(t, s2)
	waitFor(t, "supervision resumed", func() bool { return pty2.session(run.ID) != nil })
	// Many polls, no observed activity: the run is still the member's.
	time.Sleep(100 * time.Millisecond)
	r, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunNeedsAttention || r.Reason != agentstatus.ReasonInput {
		t.Fatalf("recovered run = %s because %q, want it still parked because %q", r.Status, r.Reason, agentstatus.ReasonInput)
	}

	if rerr := s2.ReportAgentState(t.Context(), run.ID, waiting); rerr != nil {
		t.Fatalf("report waiting after recovery: %v", rerr)
	}
	for range 10 {
		c.output("redraw\r\n")
		time.Sleep(10 * time.Millisecond)
	}
	if r, err = e.db.GetRun(t.Context(), run.ID); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunNeedsAttention {
		t.Fatalf("run = %s after a repaint, want it still parked: the recovered run kept its reporter", r.Status)
	}
}

// TestRecoveryKeepsAWaitingReportAcrossARestart pins the half of a restart
// the run row cannot carry. The row says needs-attention, but not that the
// agent itself asked for the member: without the report, the first thing
// the recovered agent paints - and reattaching resizes the terminal, so a
// full-screen TUI paints at once - reads as work resuming and hands the
// run back to the agent it is still waiting for.
func TestRecoveryKeepsAWaitingReportAcrossARestart(t *testing.T) {
	e := newReportingEnv(t, func(cfg *Config) {
		// Far longer than the test: nothing here is a stall.
		cfg.StallThreshold = time.Hour
		cfg.PollInterval = 10 * time.Millisecond
	})
	run, c := e.launchReporting(t)
	waiting := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonInput}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report waiting: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	startScheduler(t, s2)
	waitFor(t, "supervision resumed", func() bool { return pty2.session(run.ID) != nil })

	// The recovered agent repaints. It has said nothing since the restart,
	// so this is the same repaint the live scheduler refuses to treat as
	// work - and the restart must not have forgotten that.
	for range 10 {
		c.output("redraw\r\n")
		time.Sleep(10 * time.Millisecond)
	}
	r, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunNeedsAttention || r.Reason != agentstatus.ReasonInput {
		t.Fatalf("run = %s because %q after a repaint, want it still parked because %q: the waiting report survives a restart",
			r.Status, r.Reason, agentstatus.ReasonInput)
	}

	// The agent's own next turn still releases it.
	if rerr := s2.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Working}); rerr != nil {
		t.Fatalf("report working after recovery: %v", rerr)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
}

// TestRecoveryKeepsATurnEndRunParkedThroughItsTail is the codex half of
// the same restart. Nothing on the terminal since the restart means the
// park effectively begins again when the run comes back, and a recovered
// full-screen TUI paints at once - reattaching resizes it. Without a park
// time to measure those frames against, the first two of them would read
// as the next turn and hand the run back to an agent that is waiting.
func TestRecoveryKeepsATurnEndRunParkedThroughItsTail(t *testing.T) {
	e := newReportingEnv(t, func(cfg *Config) {
		// Far longer than the test: nothing here is a stall.
		cfg.StallThreshold = time.Hour
		cfg.PollInterval = 10 * time.Millisecond
	})
	run, c := e.launchOn(t, "codex")
	waiting := agentstatus.Report{State: agentstatus.Waiting, Reason: agentstatus.ReasonInput}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, waiting); err != nil {
		t.Fatalf("report waiting: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	startScheduler(t, s2)
	waitFor(t, "supervision resumed", func() bool { return pty2.session(run.ID) != nil })

	// The recovered TUI redraws itself over many polls. codex says nothing
	// when a turn starts, so this is all the scheduler has to judge - and
	// this soon after the run came back it is still the old turn.
	stop := pump(t, c)
	defer stop()
	time.Sleep(time.Second)
	r, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunNeedsAttention || r.Reason != agentstatus.ReasonInput {
		t.Fatalf("run = %s because %q after a recovered repaint, want it still parked because %q",
			r.Status, r.Reason, agentstatus.ReasonInput)
	}

	// Output still arriving well past the redraw is the agent working.
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
}
