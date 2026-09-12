package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
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

func TestRecoveryUnstartedDestroyFailureRetainsPendingOwner(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()
	r := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "pending unstarted cleanup",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunQueued,
	}
	if err := e.db.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := e.db.UpdateRunStatus(ctx, r.ID, domain.RunProvisioning, "", nil, nil); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	cid, err := e.rt.Create(ctx, runtime.Spec{
		Name: string(r.ID), Image: "busybox:1.36", Command: []string{"fake-agent"},
		TTY: true, CreationKey: string(r.ID),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if startErr := e.rt.Start(ctx, cid); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close scheduler: %v", closeErr)
	}

	retry := &destroyRetryRuntime{Runtime: e.rt}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = retry
	if recoverErr := s2.recoverRuns(ctx); recoverErr != nil {
		t.Fatalf("recoverRuns: %v", recoverErr)
	}
	failures, destroys, _ := retry.counts()
	if failures != 1 || destroys != 1 {
		t.Fatalf("initial unstarted cleanup calls = failures %d, destroys %d; want one failed destroy", failures, destroys)
	}
	if e.rt.byName(string(r.ID)) == nil {
		t.Fatal("failed unstarted destroy must retain the container")
	}
	s2.mu.Lock()
	owner := s2.runs[r.ID]
	pending := owner != nil && owner.destroyPending && !owner.retained
	s2.mu.Unlock()
	if !pending {
		t.Fatalf("unstarted owner after failed destroy = %+v, want active destroy-pending owner", owner)
	}
	sc, err := s2.readSidecar(r.ID)
	if err != nil {
		t.Fatalf("read unstarted pending sidecar: %v", err)
	}
	if !sc.DestroyPending || sc.Retained {
		t.Fatalf("unstarted pending sidecar = %+v, want destroy-pending active state", sc)
	}
	s2.sweepRetained(ctx)
	waitFor(t, "unstarted retry destroy", func() bool { return e.rt.byName(string(r.ID)) == nil })
	row := e.waitStoreStatus(t, r.ID, domain.RunInterrupted)
	if row.Status != domain.RunInterrupted {
		t.Fatalf("unstarted row after retry = %s, want interrupted", row.Status)
	}
	s2.mu.Lock()
	defer s2.mu.Unlock()
	if s2.runs[r.ID] != nil {
		t.Fatal("unstarted owner survived successful retry")
	}
}

func TestRecoveryUnstartedLookupFailureKeepsSyntheticOwner(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()
	r := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "unstarted lookup outage",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunQueued,
	}
	if err := e.db.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := e.db.UpdateRunStatus(ctx, r.ID, domain.RunProvisioning, "", nil, nil); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	cid, err := e.rt.Create(ctx, runtime.Spec{
		Name: string(r.ID), Image: "busybox:1.36", Command: []string{"fake-agent"},
		TTY: true, CreationKey: string(r.ID),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := e.rt.Start(ctx, cid); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	retry := &creationKeyFailureRuntime{
		Runtime: e.rt, findErr: errors.New("runtime API unavailable"),
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = retry
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	s2.mu.Lock()
	owner := s2.runs[r.ID]
	pending := owner != nil && owner.containerID == "" && owner.destroyPending &&
		!owner.retained && owner.userReservation != nil
	s2.mu.Unlock()
	if !pending {
		t.Fatalf("owner after unstarted lookup failure = %+v, want synthetic pending owner", owner)
	}
	if e.rt.byName(string(r.ID)) == nil {
		t.Fatal("lookup outage must retain the live container")
	}

	retry.setFindErr(nil)
	s2.sweepRetained(ctx)
	waitFor(t, "unstarted lookup retry destroy", func() bool { return e.rt.byName(string(r.ID)) == nil })
	row := e.waitStoreStatus(t, r.ID, domain.RunInterrupted)
	if row.Status != domain.RunInterrupted {
		t.Fatalf("row after unstarted lookup retry = %s, want interrupted", row.Status)
	}
}

func TestTerminalCreationKeyFailureInstallsSyntheticRetryOwner(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "synthetic creation-key cleanup")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	if err := os.Remove(e.sched.sidecarPath(run.ID)); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}

	findErr := errors.New("runtime API unavailable")
	retry := &creationKeyFailureRuntime{Runtime: e.rt, findErr: findErr}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = retry
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	s2.mu.Lock()
	owner := s2.runs[run.ID]
	pending := owner != nil && owner.containerID == "" && owner.destroyPending &&
		owner.retained && owner.userReservation != nil
	s2.mu.Unlock()
	if !pending {
		t.Fatalf("owner after creation-key failure = %+v, want synthetic pending owner", owner)
	}
	sc, err := s2.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read synthetic sidecar: %v", err)
	}
	if sc.ContainerID != "" || !sc.DestroyPending || sc.RunUser != unknownRecoveryRunUser {
		t.Fatalf("synthetic sidecar = %+v, want unresolved pending owner", sc)
	}

	if err := s2.Close(); err != nil {
		t.Fatalf("Close synthetic owner scheduler: %v", err)
	}
	retry.setFindErr(nil)
	s3 := e.newScheduler(t, e.rt, newFakePTY())
	s3.cfg.Runtime = retry
	if err := s3.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns after synthetic-owner reboot: %v", err)
	}
	waitFor(t, "synthetic creation-key retry", func() bool { return e.rt.byName(string(run.ID)) == nil })
	s3.mu.Lock()
	remaining := s3.runs[run.ID]
	s3.mu.Unlock()
	if remaining != nil {
		t.Fatalf("synthetic owner after reboot retry = %+v, want none", remaining)
	}
	if got := retry.emptyDestroyCount(); got != 0 {
		t.Fatalf("synthetic retry attempted Destroy with empty ID %d times", got)
	}
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
	e.rt.setWaitError(errors.New("runtime API unavailable"))

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

func TestCloseRunReconcilesInconclusiveRecoveryOwner(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = -time.Second
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "close after inconclusive recovery")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	e.rt.setWaitError(errors.New("runtime API unavailable"))
	stored, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	t.Cleanup(func() { _ = s2.Close() })
	s2.recoverSupervised(ctx, stored)
	e.rt.setWaitError(nil)

	if err := s2.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
	waitFor(t, "inconclusive recovery container destroyed", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	waitFor(t, "inconclusive recovery sidecar removed", func() bool {
		_, statErr := os.Stat(s2.sidecarPath(run.ID))
		return os.IsNotExist(statErr)
	})
}

func TestRetainedProbeErrorAdoptsOwnerForSweep(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "retained transient probe")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	e.rt.setWaitError(errors.New("runtime API unavailable"))
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	t.Cleanup(func() { _ = s2.Close() })
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}

	var owner *supervised
	waitFor(t, "retained transient owner", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		owner = s2.runs[run.ID]
		return owner != nil && owner.retained && owner.retainedUntil != nil && owner.waitStarted
	})
	s2.mu.Lock()
	due := time.Now().UTC().Add(-time.Second)
	owner.retainedUntil = &due
	if err := s2.writeSidecar(owner.sidecar()); err != nil {
		s2.mu.Unlock()
		t.Fatalf("write due sidecar: %v", err)
	}
	s2.mu.Unlock()
	e.rt.setWaitError(nil)
	s2.sweepRetained(ctx)

	waitFor(t, "retained transient container destroyed", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	waitFor(t, "retained transient owner dropped", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[run.ID] == nil
	})
	if _, statErr := os.Stat(s2.sidecarPath(run.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("retained transient sidecar still exists: %v", statErr)
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

type relaunchRollbackRuntime struct {
	runtime.Runtime
	pauseErr  error
	attachErr error
	onAttach  func()
}

func (r *relaunchRollbackRuntime) Pause(ctx context.Context, id runtime.ID) error {
	if r.pauseErr != nil {
		return r.pauseErr
	}
	return r.Runtime.Pause(ctx, id)
}

func (r *relaunchRollbackRuntime) Attach(ctx context.Context, id runtime.ID) (runtime.Attachment, error) {
	if r.onAttach != nil {
		r.onAttach()
	}
	if r.attachErr != nil {
		return nil, r.attachErr
	}
	return r.Runtime.Attach(ctx, id)
}

type failingRunUpdateStore struct {
	store.Store
	failAt int
	calls  int
	err    error
}

func (s *failingRunUpdateStore) UpdateRun(ctx context.Context, run *domain.Run) error {
	s.calls++
	if s.calls == s.failAt {
		return s.err
	}
	return s.Store.UpdateRun(ctx, run)
}

type failingRecoveryPTY struct {
	*fakePTY
	err         error
	started     chan struct{}
	startedOnce sync.Once
}

func (p *failingRecoveryPTY) StartSession(_ context.Context, _ ptyhost.SessionKey, _ runtime.Attachment) error {
	p.startedOnce.Do(func() { close(p.started) })
	return p.err
}

type destroyBarrierRuntime struct {
	runtime.Runtime
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func (r *destroyBarrierRuntime) Destroy(ctx context.Context, id runtime.ID) error {
	r.startedOnce.Do(func() { close(r.started) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.Runtime.Destroy(ctx, id)
}

func (r *destroyBarrierRuntime) releaseNow() {
	r.releaseOnce.Do(func() { close(r.release) })
}

type recoveryProbeBarrierRuntime struct {
	runtime.Runtime
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	waits   int
}

func (r *recoveryProbeBarrierRuntime) Wait(ctx context.Context, id runtime.ID) (runtime.ExitStatus, error) {
	r.mu.Lock()
	r.waits++
	call := r.waits
	r.mu.Unlock()
	if call == 1 {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return runtime.ExitStatus{}, ctx.Err()
		}
		return runtime.ExitStatus{}, context.DeadlineExceeded
	}
	return r.Runtime.Wait(ctx, id)
}

func (r *recoveryProbeBarrierRuntime) waitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waits
}

type creationKeyFailureRuntime struct {
	runtime.Runtime
	mu                sync.Mutex
	findErr           error
	emptyDestroyCalls int
}

func (r *creationKeyFailureRuntime) FindByCreationKey(ctx context.Context, key string) (runtime.ID, error) {
	r.mu.Lock()
	err := r.findErr
	r.mu.Unlock()
	if err != nil {
		return "", err
	}
	return r.Runtime.FindByCreationKey(ctx, key)
}

func (r *creationKeyFailureRuntime) Destroy(ctx context.Context, id runtime.ID) error {
	if id == "" {
		r.mu.Lock()
		r.emptyDestroyCalls++
		r.mu.Unlock()
		return errors.New("empty container ID must not be destroyed")
	}
	return r.Runtime.Destroy(ctx, id)
}

func (r *creationKeyFailureRuntime) emptyDestroyCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.emptyDestroyCalls
}

func (r *creationKeyFailureRuntime) setFindErr(err error) {
	r.mu.Lock()
	r.findErr = err
	r.mu.Unlock()
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

func TestBootExitedRetainedDestroyFailureAdoptsDueOwner(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, container := e.launchFake(t, "retry exited retained destroy")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	container.exitNow(0)

	retry := &destroyRetryRuntime{Runtime: e.rt}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = retry
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	failures, _, waits := retry.counts()
	if failures != 1 || waits != 1 {
		t.Fatalf("initial cleanup calls = failures %d, waits %d; want one failed destroy after one probe", failures, waits)
	}
	if e.rt.byName(string(run.ID)) != container {
		t.Fatal("failed exited destroy must retain the container")
	}
	s2.mu.Lock()
	owner := s2.runs[run.ID]
	due := owner != nil && owner.retained && owner.destroyPending &&
		owner.retainedUntil != nil && !time.Now().UTC().Before(*owner.retainedUntil)
	s2.mu.Unlock()
	if !due {
		t.Fatalf("owner after exited destroy failure = %+v, want retained due destroy-pending owner", owner)
	}
	sc, err := s2.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read due sidecar: %v", err)
	}
	if !sc.Retained || !sc.DestroyPending || sc.RetainedUntil == nil ||
		time.Now().UTC().Before(*sc.RetainedUntil) {
		t.Fatalf("sidecar after exited destroy failure = %+v, want due retained owner", sc)
	}

	s2.sweepRetained(ctx)
	waitFor(t, "exited retained container destroyed", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	waitFor(t, "exited retained owner dropped", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[run.ID] == nil
	})
	if _, err := os.Stat(s2.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("exited retained sidecar still exists: %v", err)
	}
}

func TestFailedTerminalDestroyRebootsWithRetryOwnership(t *testing.T) {
	e := newTestEnv(t, withServerBinary(fakeServerBinary(t, "recovery build")))
	coord, binDir := withCoordination(t, e)
	ctx := t.Context()
	run, container := e.launchFake(t, "failed terminal destroy")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	due := time.Now().UTC().Add(-time.Second)
	sc.Retained = true
	sc.RetainedUntil = &due
	sc.RunUser = "1000:1000"
	if err := e.sched.writeSidecar(sc); err != nil {
		t.Fatalf("write failed-destroy sidecar: %v", err)
	}
	finished := time.Now().UTC()
	if err := e.db.UpdateRunStatus(ctx, run.ID, domain.RunCompleted,
		"agent exited; results committed", nil, &finished); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}

	retry := &destroyRetryRuntime{Runtime: e.rt}
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = retry
	s2.UseCoordination(coord, binDir)
	t.Cleanup(func() { _ = s2.Close() })
	if !s2.RetainsContainer(ctx, run.ID) {
		t.Fatal("failed terminal destroy sidecar lost durable ownership")
	}
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	if e.rt.byName(string(run.ID)) != container {
		t.Fatal("failed reboot destroy removed the container")
	}
	var owner *supervised
	waitFor(t, "failed terminal destroy owner", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		owner = s2.runs[run.ID]
		return owner != nil && owner.retained && owner.userReservation != nil && owner.waitStarted
	})
	if _, statErr := os.Stat(sc.CoordDir); statErr != nil {
		t.Fatalf("coordination directory after failed destroy: %v", statErr)
	}

	s2.sweepRetained(ctx)
	waitFor(t, "failed terminal destroy retry", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	waitFor(t, "failed terminal destroy owner dropped", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		return s2.runs[run.ID] == nil
	})
	if _, statErr := os.Stat(s2.sidecarPath(run.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("failed terminal destroy sidecar after retry: %v", statErr)
	}
	if _, statErr := os.Stat(sc.CoordDir); !os.IsNotExist(statErr) {
		t.Fatalf("coordination directory after retry: %v", statErr)
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

type resumeFailureRuntime struct {
	runtime.Runtime
	resumeErr error
}

func (r *resumeFailureRuntime) Resume(ctx context.Context, id runtime.ID) error {
	if r.resumeErr != nil {
		return r.resumeErr
	}
	return r.Runtime.Resume(ctx, id)
}

func TestRelaunchRestoresTerminalRowWhenResumeFails(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, container := e.launchFake(t, "resume ordering")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	resumeErr := errors.New("runtime resume unavailable")
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = &resumeFailureRuntime{Runtime: e.rt, resumeErr: resumeErr}
	t.Cleanup(func() { _ = s2.Close() })
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	if _, err := s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, resumeErr) {
		t.Fatalf("Relaunch error = %v, want resume error", err)
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after failed Relaunch: %v", err)
	}
	if row.Status != domain.RunMerged || row.Reason != retainedCloseReason {
		t.Fatalf("row after failed Relaunch = %+v, want retained terminal row", row)
	}
	if got := container.currentState(); got != "paused" {
		t.Fatalf("container after failed Relaunch = %q, want paused", got)
	}
	sc, err := s2.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar after failed Relaunch: %v", err)
	}
	if !sc.Retained || !sc.Paused {
		t.Fatalf("sidecar after failed Relaunch = %+v, want retained paused", sc)
	}
}

func TestRelaunchRollbackStoreUpdateFailureKeepsActiveOwner(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, container := e.launchFake(t, "rollback row failure")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	attachErr := errors.New("relaunch attach unavailable")
	rowErr := errors.New("terminal row restore unavailable")
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = &relaunchRollbackRuntime{Runtime: e.rt, attachErr: attachErr}
	s2.cfg.Store = &failingRunUpdateStore{Store: e.db, failAt: 2, err: rowErr}
	t.Cleanup(func() { _ = s2.Close() })

	if _, err := s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, attachErr) ||
		!errors.Is(err, rowErr) {
		t.Fatalf("Relaunch error = %v, want attach and terminal-row errors", err)
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after failed Relaunch: %v", err)
	}
	if row.Status != domain.RunRunning || row.Reason != "" || row.FinishedAt != nil {
		t.Fatalf("row after failed Relaunch = %+v, want promoted running row", row)
	}
	s2.mu.Lock()
	owner := s2.runs[run.ID]
	coherent := owner != nil && owner.status == domain.RunRunning &&
		owner.paused && !owner.retained && !owner.destroyPending
	s2.mu.Unlock()
	if !coherent {
		t.Fatalf("owner after failed Relaunch = %+v, want paused active owner", owner)
	}
	sc, err := s2.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar after failed Relaunch: %v", err)
	}
	if !sc.Paused || sc.Retained || sc.DestroyPending || sc.RetainedUntil != nil {
		t.Fatalf("sidecar after failed Relaunch = %+v, want paused active state", sc)
	}
	if got := container.currentState(); got != "paused" {
		t.Fatalf("container after failed Relaunch = %q, want paused", got)
	}
}

func TestRelaunchRollbackSidecarFailureKeepsActiveOwnerAcrossReboot(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, container := e.launchFake(t, "rollback sidecar failure")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	attachErr := errors.New("relaunch attach unavailable")
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	stateDir := s2.cfg.StateDir
	s2.cfg.Runtime = &relaunchRollbackRuntime{
		Runtime:   e.rt,
		attachErr: attachErr,
		onAttach: func() {
			s2.cfg.StateDir = filepath.Join(stateDir, "missing")
		},
	}
	t.Cleanup(func() {
		s2.cfg.StateDir = stateDir
		_ = s2.Close()
	})

	if _, err := s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, attachErr) ||
		!strings.Contains(err.Error(), "persist retained close") {
		t.Fatalf("Relaunch error = %v, want attach and sidecar errors", err)
	}
	s2.cfg.StateDir = stateDir
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after failed Relaunch: %v", err)
	}
	if row.Status != domain.RunRunning || row.Reason != "" || row.FinishedAt != nil {
		t.Fatalf("row after failed Relaunch = %+v, want promoted running row", row)
	}
	s2.mu.Lock()
	owner := s2.runs[run.ID]
	active := owner != nil && owner.status == domain.RunRunning &&
		owner.paused && !owner.retained && !owner.destroyPending
	s2.mu.Unlock()
	if !active {
		t.Fatalf("owner after sidecar failure = %+v, want paused active owner", owner)
	}
	if got := container.currentState(); got != "paused" {
		t.Fatalf("container after sidecar failure = %q, want paused", got)
	}

	if closeErr := s2.Close(); closeErr != nil {
		t.Fatalf("Close after failed Relaunch: %v", closeErr)
	}
	e.rt.setWaitError(errors.New("reboot probe unavailable"))
	s3 := e.newScheduler(t, e.rt, newFakePTY())
	t.Cleanup(func() { _ = s3.Close() })
	if recoverErr := s3.recoverRuns(ctx); recoverErr != nil {
		t.Fatalf("recoverRuns after sidecar failure: %v", recoverErr)
	}
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("reboot after sidecar failure orphaned the container")
	}
	s3.mu.Lock()
	recovered := s3.runs[run.ID]
	recoveredActive := recovered != nil && recovered.status == domain.RunRunning &&
		recovered.paused && !recovered.retained && !recovered.destroyPending
	s3.mu.Unlock()
	if !recoveredActive {
		t.Fatalf("recovered owner after sidecar failure = %+v, want paused active owner", recovered)
	}
	sc, err := s3.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar after reboot: %v", err)
	}
	if sc.Retained || sc.DestroyPending || sc.RetainedUntil != nil || !sc.Paused {
		t.Fatalf("sidecar after reboot = %+v, want active paused state", sc)
	}
}

func TestRelaunchPauseFailureKeepsPromotedRunningOwner(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, container := e.launchFake(t, "rollback pause failure")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}

	attachErr := errors.New("relaunch attach unavailable")
	pauseErr := errors.New("rollback pause unavailable")
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = &relaunchRollbackRuntime{
		Runtime: e.rt, attachErr: attachErr, pauseErr: pauseErr,
	}
	if err := s2.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	if _, err := s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(err, attachErr) ||
		!errors.Is(err, pauseErr) {
		t.Fatalf("Relaunch error = %v, want attach and rollback pause errors", err)
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after failed Relaunch: %v", err)
	}
	if row.Status != domain.RunRunning || row.Reason != "" || row.FinishedAt != nil {
		t.Fatalf("row after failed Relaunch = %+v, want running row", row)
	}
	s2.mu.Lock()
	owner := s2.runs[run.ID]
	active := owner != nil && owner.status == domain.RunRunning &&
		!owner.retained && !owner.paused && !owner.destroyPending
	s2.mu.Unlock()
	if !active {
		t.Fatalf("owner after failed Relaunch = %+v, want active non-retained owner", owner)
	}
	sc, err := s2.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar after failed Relaunch: %v", err)
	}
	if sc.Retained || sc.Paused || sc.DestroyPending || sc.RetainedUntil != nil {
		t.Fatalf("sidecar after failed Relaunch = %+v, want active state", sc)
	}
	if got := container.currentState(); got != "running" {
		t.Fatalf("container after failed Relaunch = %q, want running", got)
	}
}

func TestRecoveryAttachDoesNotReplaceCloseOwner(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "close wins recovery attach")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	stored, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	barrier := &recoveryProbeBarrierRuntime{
		Runtime: e.rt, started: make(chan struct{}), release: make(chan struct{}),
	}
	beforeAttach := e.rt.attachCount()
	s2 := e.newScheduler(t, e.rt, newFakePTY())
	s2.cfg.Runtime = barrier
	recoveryDone := make(chan struct{})
	go func() {
		s2.recoverSupervised(ctx, stored)
		close(recoveryDone)
	}()
	select {
	case <-barrier.started:
	case <-time.After(waitTimeout):
		t.Fatal("recovery probe never reached barrier")
	}
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- s2.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged)
	}()
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("CloseRun: %v", closeErr)
		}
	case <-time.After(waitTimeout):
		t.Fatal("CloseRun did not win recovery probe race")
	}
	close(barrier.release)
	select {
	case <-recoveryDone:
	case <-time.After(waitTimeout):
		t.Fatal("recovery attach did not finish after probe release")
	}
	if got := e.rt.attachCount(); got != beforeAttach {
		t.Fatalf("recovery attached after Close won: attaches %d -> %d", beforeAttach, got)
	}
	if got := barrier.waitCount(); got != 2 {
		t.Fatalf("Wait owners/calls = %d, want probe plus Close owner", got)
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after race: %v", err)
	}
	if row.Status != domain.RunMerged || row.Reason != retainedCloseReason {
		t.Fatalf("row after recovery/Close race = %+v, want retained terminal row", row)
	}
	s2.mu.Lock()
	owner := s2.runs[run.ID]
	coherent := owner != nil && owner.retained && owner.waitStarted
	s2.mu.Unlock()
	if !coherent {
		t.Fatalf("owner after recovery/Close race = %+v, want one retained Wait owner", owner)
	}
}
func TestRecoveryPTYFailureKeepsOwnerForDelete(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, _ := e.launchFake(t, "recovery PTY failure")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	sc.RunUser = "1000:1000"
	if writeErr := e.sched.writeSidecar(sc); writeErr != nil {
		t.Fatalf("write sidecar: %v", writeErr)
	}
	stored, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	destroy := &destroyBarrierRuntime{
		Runtime: e.rt, started: make(chan struct{}), release: make(chan struct{}),
	}
	pty := &failingRecoveryPTY{
		fakePTY: newFakePTY(), err: errors.New("recovery PTY unavailable"),
		started: make(chan struct{}),
	}
	s2 := e.newScheduler(t, e.rt, pty.fakePTY)
	s2.cfg.Runtime = destroy
	s2.cfg.PTY = pty
	t.Cleanup(func() {
		destroy.releaseNow()
		_ = s2.Close()
	})

	recoveryDone := make(chan struct{})
	go func() {
		s2.recoverSupervised(ctx, stored)
		close(recoveryDone)
	}()
	select {
	case <-pty.started:
	case <-time.After(waitTimeout):
		t.Fatal("recovery never attempted PTY setup")
	}
	select {
	case <-destroy.started:
	case <-time.After(waitTimeout):
		t.Fatal("recovery never started cleanup")
	}

	s2.mu.Lock()
	owner := s2.runs[run.ID]
	pending := owner != nil && owner.destroyPending && !owner.retained &&
		owner.userReservation != nil
	s2.mu.Unlock()
	if !pending {
		t.Fatalf("owner during failed PTY cleanup = %+v, want A destroy-pending owner", owner)
	}

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- s2.DeleteRun(ctx, run.ID, e.member.ID) }()
	select {
	case err := <-deleteDone:
		t.Fatalf("DeleteRun completed before physical cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := e.db.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("GetRun while cleanup is blocked: %v", err)
	}
	if _, err := os.Stat(run.Worktree); err != nil {
		t.Fatalf("checkout removed before physical cleanup: %v", err)
	}
	s2.mu.Lock()
	if s2.runs[run.ID] != owner {
		s2.mu.Unlock()
		t.Fatalf("cleanup replaced owner A with %+v", s2.runs[run.ID])
	}
	s2.mu.Unlock()

	destroy.releaseNow()
	select {
	case <-recoveryDone:
	case <-time.After(waitTimeout):
		t.Fatal("recovery cleanup did not finish")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("DeleteRun after cleanup: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("DeleteRun did not finish after cleanup")
	}
	select {
	case <-owner.done:
	default:
		t.Fatal("owner A done channel is still open")
	}
	if _, err := e.db.GetRun(ctx, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after DeleteRun: %v, want store.ErrNotFound", err)
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
