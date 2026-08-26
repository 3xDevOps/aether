package scheduler

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKillDuringProvisioning pins the invariant that a Kill landing while
// the run is still provisioning (e.g. mid image pull) terminates the run:
// abandoned is terminal, the launch fails, and no container survives.
func TestKillDuringProvisioning(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	ctx := t.Context()
	t.Setenv(fakeAgentEnv, "fake-agent {task}")

	provisioning := make(chan struct{})
	release := make(chan struct{})
	e.rt.createHook = func() {
		close(provisioning)
		<-release
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, "slow image pull", "fake", domain.LaunchTUI)
		done <- err
	}()
	<-provisioning
	ev := waitStatusEvent(t, sub, "", domain.RunProvisioning)
	runID := ev.RunID

	if err := e.sched.Kill(ctx, runID, e.member.ID); err != nil {
		t.Fatalf("Kill during provisioning: %v", err)
	}
	waitTimelineEvent(t, sub, runID, events.TimelineKill)
	close(release)

	if err := <-done; err == nil {
		t.Fatal("Launch must not succeed after a kill during provisioning")
	}
	aband := waitStatusEvent(t, sub, runID, domain.RunAbandoned)
	if p := aband.Payload.(events.RunStatusPayload); p.Reason != "killed" {
		t.Fatalf("abandoned reason = %q", p.Reason)
	}
	r := e.waitStoreStatus(t, runID, domain.RunAbandoned)
	if r.FinishedAt == nil {
		t.Fatal("abandoned run must have FinishedAt")
	}
	if r.Worktree == "" {
		t.Fatal("kill must preserve the worktree")
	}
	if got := e.git.commitsFor(runID); len(got) != 1 || got[0] != "wip: slow image pull" {
		t.Fatalf("commits = %v", got)
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(runID)) == nil })
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(e.sched.sidecarPath(runID))
		return os.IsNotExist(err)
	})
	// The terminal state must stick: no late provisioning step may
	// resurrect the run.
	time.Sleep(50 * time.Millisecond)
	r, err := e.db.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunAbandoned {
		t.Fatalf("run resurrected to %s after kill", r.Status)
	}
}

// TestConcurrentCloseAndKill pins that racing terminal transitions on a
// parked needs-attention run resolve to exactly one winner.
func TestConcurrentCloseAndKill(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "parked work")
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged) }()
	go func() { defer wg.Done(); errs[1] = e.sched.Kill(ctx, run.ID, e.member.ID) }()
	wg.Wait()

	var ok int
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("loser error = %v, want ErrInvalidTransition", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d of the racing terminal transitions succeeded, want exactly 1 (errs=%v)", ok, errs)
	}
	r, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	switch {
	case errs[0] == nil && r.Status != domain.RunMerged:
		t.Fatalf("close won but status = %s", r.Status)
	case errs[1] == nil && r.Status != domain.RunAbandoned:
		t.Fatalf("kill won but status = %s", r.Status)
	}
	if r.FinishedAt == nil {
		t.Fatal("terminal run must have FinishedAt")
	}
}

// TestRecoveryReissuesPersistedKill pins that a kill persisted to the
// sidecar but interrupted by a reboot is re-issued when supervision
// resumes: the durable kill_requested flag is the whole point of the
// sidecar (§6.6).
func TestRecoveryReissuesPersistedKill(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)

	run, _ := e.launchFake(t, "killed mid reboot")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The server died after persisting kill_requested but before stopping
	// the container.
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("readSidecar: %v", err)
	}
	sc.KillRequested = true
	data, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	if err := os.WriteFile(e.sched.sidecarPath(run.ID), data, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	s2 := e.newScheduler(t, e.rt, newFakePTY())
	startScheduler(t, s2)

	ev := waitStatusEvent(t, sub, run.ID, domain.RunAbandoned)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "killed" {
		t.Fatalf("abandoned reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
}

// TestRecoveryDestroysProvisioningContainer pins that recovery of a run
// that died mid-provisioning consults the sidecar and destroys the
// container it names instead of leaking it (the wide crash window spans
// Attach/StartSession/StartDiffWatch).
func TestRecoveryDestroysProvisioningContainer(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	r := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "halfway there",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunQueued,
	}
	if err := e.db.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	path, branch, err := e.git.CreateRunCheckout(ctx, e.ws.ID, r.ID, "main", r.Task)
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	r.Worktree, r.Branch = path, branch
	if uerr := e.db.UpdateRun(ctx, r); uerr != nil {
		t.Fatalf("UpdateRun: %v", uerr)
	}
	if uerr := e.db.UpdateRunStatus(ctx, r.ID, domain.RunProvisioning, "", nil, nil); uerr != nil {
		t.Fatalf("UpdateRunStatus: %v", uerr)
	}
	cid, err := e.rt.Create(ctx, runtime.Spec{
		Name: string(r.ID), Image: "busybox:1.36", Command: []string{"fake-agent"},
		WorktreeHostPath: path, WorktreeMountPath: "/workspace", TTY: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if serr := e.rt.Start(ctx, cid); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	data, err := json.Marshal(sidecar{
		RunID: string(r.ID), ContainerID: string(cid),
		WorkspaceID: string(e.ws.ID),
	})
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	if err := os.WriteFile(e.sched.sidecarPath(r.ID), data, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	startScheduler(t, e.sched)
	e.waitStoreStatus(t, r.ID, domain.RunInterrupted)
	waitFor(t, "orphaned container destroyed", func() bool { return e.rt.byName(string(r.ID)) == nil })
	if got := e.git.commitsFor(r.ID); len(got) != 1 || got[0] != "wip: halfway there" {
		t.Fatalf("commits = %v", got)
	}
	if e.git.publishedCount(r.ID) == 0 {
		t.Fatal("branch not published during recovery")
	}
	waitFor(t, "sidecar removed", func() bool {
		_, err := os.Stat(e.sched.sidecarPath(r.ID))
		return os.IsNotExist(err)
	})
}

// TestSweepSkipsCheckoutSharedWithActiveRun pins that checkout GC never
// removes a checkout an active (relaunched) run is still working in, even
// when the old terminal row that names it has expired.
func TestSweepSkipsCheckoutSharedWithActiveRun(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) {
		cfg.CheckoutTTL = time.Hour
	})
	ctx := t.Context()

	old := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "interrupted",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunQueued,
	}
	if err := e.db.CreateRun(ctx, old); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	path, branch, err := e.git.CreateRunCheckout(ctx, e.ws.ID, old.ID, "main", old.Task)
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	old.Worktree, old.Branch = path, branch
	if uerr := e.db.UpdateRun(ctx, old); uerr != nil {
		t.Fatalf("UpdateRun: %v", uerr)
	}
	finished := time.Now().UTC().Add(-2 * time.Hour)
	if uerr := e.db.UpdateRunStatus(ctx, old.ID, domain.RunInterrupted, "", nil, &finished); uerr != nil {
		t.Fatalf("UpdateRunStatus: %v", uerr)
	}
	// A leftover active row still names the old checkout (the pre-fix
	// shared-tree case). GC must not reclaim it.
	relaunched := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: old.Task,
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunRunning,
		Branch: branch, Worktree: path,
	}
	if cerr := e.db.CreateRun(ctx, relaunched); cerr != nil {
		t.Fatalf("CreateRun relaunched: %v", cerr)
	}

	e.sched.sweepCheckouts(ctx)

	if _, serr := os.Stat(path); serr != nil {
		t.Fatalf("shared checkout removed under the live run: %v", serr)
	}
	oldAgain, err := e.db.GetRun(ctx, old.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if oldAgain.Worktree == "" {
		t.Fatal("old run's worktree cleared while the checkout is still in use")
	}
}

// TestRelaunchRejectsCheckoutInUse pins that relaunch refuses to clone from
// a source whose old checkout is somehow still named by an active run.
func TestRelaunchRejectsCheckoutInUse(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, _ := e.launchFake(t, "interrupted work")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rt2 := newFakeRuntime()
	s2 := e.newScheduler(t, rt2, newFakePTY())
	startScheduler(t, s2)
	old := e.waitStoreStatus(t, run.ID, domain.RunInterrupted)

	next, err := s2.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if next.Worktree == old.Worktree {
		t.Fatal("relaunch must not share the old worktree")
	}
	if e.git.baseBranchFor(next.ID) != old.Branch {
		t.Fatalf("CreateRunCheckout base = %q, want %q", e.git.baseBranchFor(next.ID), old.Branch)
	}

	// A leftover active row still naming the old checkout must block
	// another relaunch of that source.
	blocker := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "blocker",
		Harness: "fake", Mode: domain.LaunchTUI, Status: domain.RunRunning,
		Branch: "aether/run-blocker", Worktree: old.Worktree,
	}
	if cerr := e.db.CreateRun(ctx, blocker); cerr != nil {
		t.Fatalf("CreateRun blocker: %v", cerr)
	}
	if _, rerr := s2.Relaunch(ctx, run.ID, e.member.ID); !errors.Is(rerr, ErrInvalidTransition) {
		t.Fatalf("Relaunch with old checkout in use = %v, want ErrInvalidTransition", rerr)
	}
	finished := time.Now().UTC()
	if uerr := e.db.UpdateRunStatus(ctx, blocker.ID, domain.RunAbandoned, "", nil, &finished); uerr != nil {
		t.Fatalf("abandon blocker: %v", uerr)
	}
	third, err := s2.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch after blocker released: %v", err)
	}
	if third.Worktree == old.Worktree || third.Worktree == next.Worktree {
		t.Fatalf("third relaunch worktree = %q, collided with old %q or next %q", third.Worktree, old.Worktree, next.Worktree)
	}
	if e.git.baseBranchFor(third.ID) != old.Branch {
		t.Fatalf("third CreateRunCheckout base = %q, want %q", e.git.baseBranchFor(third.ID), old.Branch)
	}
}

// TestKillRacingAgentExit pins that a Kill accepted while finalize is in
// flight is reflected in the recorded outcome: the run lands abandoned,
// not parked at needs-attention.
func TestKillRacingAgentExit(t *testing.T) {
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	ctx := t.Context()

	var entered sync.Once
	inCommit := make(chan struct{})
	release := make(chan struct{})
	e.git.commitHook = func(domain.RunID, string) {
		entered.Do(func() { close(inCommit) })
		<-release
	}

	run, c := e.launchFake(t, "exit vs kill")
	c.exitNow(0)
	<-inCommit // finalize is past its early snapshot, blocked in CommitAll

	if err := e.sched.Kill(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineKill)
	close(release)

	ev := waitStatusEvent(t, sub, run.ID, domain.RunAbandoned)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "killed" {
		t.Fatalf("abandoned reason = %q", p.Reason)
	}
	if ev.ActorID != e.member.ID {
		t.Fatalf("abandoned actor = %s", ev.ActorID)
	}
	e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
}
