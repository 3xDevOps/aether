package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

type failingRunStatusStore struct {
	store.Store
	fail bool
}

func (s *failingRunStatusStore) UpdateRunStatus(ctx context.Context, id domain.RunID, status domain.RunStatus, reason string, startedAt, finishedAt *time.Time) error {
	if s.fail {
		return errors.New("test: status transition failed")
	}
	return s.Store.UpdateRunStatus(ctx, id, status, reason, startedAt, finishedAt)
}

func (s *failingRunStatusStore) FinishRunByMember(ctx context.Context, id domain.RunID, status domain.RunStatus, reason string, startedAt, finishedAt *time.Time) error {
	if s.fail {
		return errors.New("test: status transition failed")
	}
	return s.Store.FinishRunByMember(ctx, id, status, reason, startedAt, finishedAt)
}

type destroyFailureRuntime struct {
	runtime.Runtime
	destroyErr error
}

func (r *destroyFailureRuntime) Destroy(ctx context.Context, id runtime.ID) error {
	if r.destroyErr != nil {
		return r.destroyErr
	}
	return r.Runtime.Destroy(ctx, id)
}

func TestKill(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	// Kill publishes the timeline event and the abandoned transition on
	// different goroutines in either order, so each wait reads its own stream.
	status := e.subscribe(t)
	ctx := t.Context()

	run, _ := e.launchFake(t, "long haul task")
	if err := e.sched.Kill(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineKill)

	ev := waitStatusEvent(t, status, run.ID, domain.RunAbandoned)
	p := ev.Payload.(events.RunStatusPayload)
	if p.Reason != "killed" {
		t.Fatalf("abandoned reason = %q", p.Reason)
	}
	if ev.ActorID != e.member.ID {
		t.Fatalf("abandoned actor = %s, want %s", ev.ActorID, e.member.ID)
	}
	if p.FinishUnopened {
		t.Fatalf("abandoned event = %+v, want a member's kill to leave the finish opened", p)
	}
	fresh := e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
	if fresh.FinishUnopened {
		t.Fatal("a member's kill left the finish unopened")
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "wip: long haul task" {
		t.Fatalf("commits = %v", got)
	}
	if fresh.Worktree == "" {
		t.Fatal("kill must preserve the worktree")
	}
	waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
}

func TestPauseResumeAndStallExemption(t *testing.T) {
	t.Parallel()
	// 500ms/20ms: a tighter threshold can elapse under contention before
	// Pause takes effect.
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 500 * time.Millisecond
		cfg.PollInterval = 20 * time.Millisecond
	})
	sub := e.subscribe(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, c := e.launchFake(t, "task")
	if err := e.sched.Pause(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelinePause)
	if got := c.currentState(); got != "paused" {
		t.Fatalf("container state = %q, want paused", got)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || !sc.Paused {
		t.Fatalf("sidecar paused flag: %+v, %v", sc, err)
	}
	if !e.sched.Paused(run.ID) {
		t.Fatal("Paused(run) = false after Pause")
	}
	if e.sched.Paused("run_unknown") {
		t.Fatal("Paused(unknown run) = true")
	}
	if perr := e.sched.Pause(ctx, run.ID, e.member.ID); perr == nil {
		t.Fatal("double pause accepted")
	}

	time.Sleep(700 * time.Millisecond)
	r, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunRunning {
		t.Fatalf("paused run status = %s, want running", r.Status)
	}

	if rerr := e.sched.Resume(ctx, run.ID, e.member.ID); rerr != nil {
		t.Fatalf("Resume: %v", rerr)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineResume)
	if got := c.currentState(); got != "running" {
		t.Fatalf("container state = %q, want running", got)
	}
	sc, err = e.sched.readSidecar(run.ID)
	if err != nil || sc.Paused {
		t.Fatalf("sidecar paused flag after resume: %+v, %v", sc, err)
	}
	if e.sched.Paused(run.ID) {
		t.Fatal("Paused(run) = true after Resume")
	}

	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); !strings.HasPrefix(p.Reason, "stalled: no output or file changes for ") {
		t.Fatalf("stall reason = %q", p.Reason)
	}
}

func TestStallAndActivityResume(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 500 * time.Millisecond
		cfg.PollInterval = 20 * time.Millisecond
	})
	sub := e.subscribe(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, c := e.launchFake(t, "task")
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); !strings.HasPrefix(p.Reason, "stalled: ") {
		t.Fatalf("stall reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				c.output("still alive\r\n")
			}
		}
	}()
	resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if p := resumed.Payload.(events.RunStatusPayload); p.Reason != "activity resumed" {
		t.Fatalf("resume reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
}

func TestFileChangeCountsAsActivity(t *testing.T) {
	t.Parallel()
	// 500ms/20ms: with ~200 parallel tests on one P, a delayed tick can
	// outrun a tighter threshold and park an active run.
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 500 * time.Millisecond
		cfg.PollInterval = 20 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, _ := e.launchFake(t, "task")
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		e.git.touch(run.ID)
		time.Sleep(20 * time.Millisecond)
	}
	r, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Status != domain.RunRunning {
		t.Fatalf("run with file activity = %s, want running", r.Status)
	}
}

func TestInject(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	if _, err := e.sched.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: "focus on the tests"}, false, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	inj := e.pty.injected()
	if len(inj) != 1 || inj[0].name != "Ada" || inj[0].color != "#e6194b" || inj[0].message != "focus on the tests" {
		t.Fatalf("injects = %+v", inj)
	}
	waitFor(t, "stdin delivery", func() bool {
		return strings.Contains(c.stdinString(), "focus on the tests\r")
	})
	ev := waitTimelineEvent(t, sub, run.ID, events.TimelineSteer)
	p := ev.Payload.(events.TimelinePayload)
	if p.Message != "focus on the tests" {
		t.Fatalf("steer message = %q", p.Message)
	}
	if ev.ActorID != e.member.ID {
		t.Fatalf("steer actor = %s", ev.ActorID)
	}
}

// The submit sequence is the harness's: a harness that steers with a
// second Enter gets it, so steered text reaches the conversation instead
// of sitting in the agent's input box.
func TestInjectUsesHarnessSubmitSequence(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	if _, err := e.sched.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: "one enter"}, false, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	waitFor(t, "stdin delivery", func() bool {
		return strings.HasSuffix(c.stdinString(), "one enter\r")
	})

	opencode, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "task", "opencode", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch opencode: %v", err)
	}
	if _, err := e.sched.Inject(ctx, opencode.ID, e.member.ID, domain.AgentPrompt{Text: "two enters"}, false, nil); err != nil {
		t.Fatalf("Inject opencode: %v", err)
	}
	inj := e.pty.injected()
	var last *fakeInject
	for i := range inj {
		if inj[i].run == opencode.ID {
			last = &inj[i]
		}
	}
	if last == nil || last.submit != "\r\r" {
		t.Fatalf("opencode injects = %+v, want submit %q", inj, "\r\r")
	}
}

// CloseRun resolves a stalled TUI run's disposition directly: the status
// moves to the outcome before any later relaunch, and its exact container is
// retained until DeleteRun or expiry.
func TestCloseRunResolvesStalledRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = 500 * time.Millisecond
		cfg.PollInterval = 20 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, _ := e.launchFake(t, "task")
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun on stalled run: %v", err)
	}
	r := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if r.FinishedAt == nil {
		t.Fatal("closed run must have FinishedAt")
	}
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("closed TUI run lost its retained container")
	}
	e.sched.mu.Lock()
	retained := e.sched.runs[run.ID] != nil && e.sched.runs[run.ID].retained
	e.sched.mu.Unlock()
	if !retained {
		t.Fatal("closed TUI run was not marked retained")
	}
	if err := e.sched.DeleteRun(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
}

// Kill must reconcile a durable retained owner before recoverRuns adopts it.
// The terminal row remains, but the exact retained container and sidecar are
// destroyed through the normal retry-owning expiry path.
func TestKillUnsupervisedRetainedSidecar(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "kill retained sidecar")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read retained sidecar: %v", err)
	}
	if !sc.Retained || sc.ContainerID == "" {
		t.Fatalf("retained sidecar = %+v", sc)
	}
	if closeErr := e.sched.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	recovered := e.newScheduler(t, e.rt, newFakePTY())
	if killErr := recovered.Kill(ctx, run.ID, e.member.ID); killErr != nil {
		t.Fatalf("startup Kill: %v", killErr)
	}
	waitFor(t, "unsupervised retained container destroyed", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after startup Kill: %v", err)
	}
	if row.Reason != retainedUnavailableReason {
		t.Fatalf("startup Kill reason = %q, want %q", row.Reason, retainedUnavailableReason)
	}
	if recovered.RetainsContainer(ctx, run.ID) {
		t.Fatal("startup Kill left a durable retained ownership promise")
	}
	if _, err := os.Stat(recovered.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("sidecar after startup Kill: %v", err)
	}
	recovered.mu.Lock()
	entry := recovered.runs[run.ID]
	recovered.mu.Unlock()
	if entry != nil {
		t.Fatalf("startup Kill left an in-memory owner: %+v", entry)
	}
}
func TestReleaseRetainedRunPreservesRecordAndArchive(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run, _ := e.launchFake(t, "release archived run")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	archived, err := e.sched.SetArchived(ctx, run.ID, e.member.ID, true)
	if err != nil {
		t.Fatalf("SetArchived: %v", err)
	}
	sub := e.subscribe(t)
	if err := e.sched.Release(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("released container still exists")
	}
	fresh := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if fresh.Reason != retainedUnavailableReason || fresh.Worktree != archived.Worktree ||
		fresh.Branch != archived.Branch || fresh.ArchivedAt == nil ||
		!fresh.ArchivedAt.Equal(*archived.ArchivedAt) || fresh.FinishedAt == nil {
		t.Fatalf("release changed retained record or archive: before %+v, after %+v", archived, fresh)
	}
	if _, err := os.Stat(fresh.Worktree); err != nil {
		t.Fatalf("released run lost checkout: %v", err)
	}
	if _, err := os.Stat(e.sched.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("release kept sidecar: %v", err)
	}
	status := expectOnlyStatusEvent(t, sub, run.ID, domain.RunMerged)
	if status.From != domain.RunMerged || status.Reason != retainedUnavailableReason {
		t.Fatalf("release event = %+v", status)
	}
	if err := e.sched.Release(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("repeat Release: %v", err)
	}
	expectNoStatusEvent(t, sub, run.ID, "repeat release")
}

func TestReleaseRefusesActiveAndRelaunchedRuns(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run, container := e.launchFake(t, "active then relaunched")
	if err := e.sched.Release(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("active Release = %v, want invalid transition", err)
	}
	if e.rt.byName(string(run.ID)) != container || container.currentState() != "running" {
		t.Fatal("active release stopped the running container")
	}
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if _, err := e.sched.Relaunch(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if err := e.sched.Release(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("release after relaunch = %v, want invalid transition", err)
	}
	if e.rt.byName(string(run.ID)) != container || container.currentState() != "running" {
		t.Fatal("release destroyed relaunched container")
	}
	if fresh := e.waitStoreStatus(t, run.ID, domain.RunRunning); fresh.Worktree != run.Worktree {
		t.Fatalf("reopened checkout changed: %+v", fresh)
	}
}

func TestReleaseRefusesUnsupervisedActiveRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, container := e.launchFake(t, "active before recovery")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	if err := recovered.Release(ctx, run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unsupervised active Release = %v, want invalid transition", err)
	}
	if e.rt.byName(string(run.ID)) != container || container.currentState() != "running" {
		t.Fatal("unsupervised active release stopped container")
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
}

func TestReleaseAdoptsUnsupervisedRetainedOwner(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run, _ := e.launchFake(t, "release before recovery")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	if err := recovered.Release(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Release before recoverRuns: %v", err)
	}
	if e.rt.byName(string(run.ID)) != nil || recovered.RetainsContainer(ctx, run.ID) {
		t.Fatal("unsupervised release kept runtime or durable ownership")
	}
	if fresh := e.waitStoreStatus(t, run.ID, domain.RunMerged); fresh.Reason != retainedUnavailableReason {
		t.Fatalf("unsupervised release changed terminal status/reason: %+v", fresh)
	}
	if err := recovered.Release(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("repeat unsupervised release: %v", err)
	}
}

func TestReleaseKeepsRetainedContainerOnEvidenceFailure(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run, container := e.launchFake(t, "release requires evidence")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.failures = 1
	e.sched.UseEvidence(capture)
	if err := e.sched.Release(ctx, run.ID, e.member.ID); err == nil || !strings.Contains(err.Error(), "evidence capture unavailable") {
		t.Fatalf("Release evidence error = %v", err)
	}
	if e.rt.byName(string(run.ID)) != container || !e.sched.RetainsContainer(ctx, run.ID) {
		t.Fatal("failed evidence capture released retained container")
	}
	if err := e.sched.Release(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("retry Release: %v", err)
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("successful evidence retry kept container")
	}
}

func syntheticDestroyPendingRun(t *testing.T, e *testEnv, task string) *domain.Run {
	t.Helper()
	run, _ := e.launchFake(t, task)
	ctx := t.Context()
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	if err := e.sched.writeSidecar(sidecar{
		RunID: string(run.ID), WorkspaceID: string(run.WorkspaceID),
		DestroyPending: true, RunUser: unknownRecoveryRunUser,
	}); err != nil {
		t.Fatalf("write synthetic sidecar: %v", err)
	}
	return run
}

func TestReleaseRetriesTerminalCreationKeyCleanup(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run := syntheticDestroyPendingRun(t, e, "release pending cleanup")
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	if err := recovered.Release(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Release destroy-pending run: %v", err)
	}
	if e.rt.byName(string(run.ID)) != nil || recovered.RetainsContainer(ctx, run.ID) {
		t.Fatal("release left creation-key container or owner")
	}
	if fresh := e.waitStoreStatus(t, run.ID, domain.RunMerged); fresh.Worktree != run.Worktree {
		t.Fatalf("cleanup changed terminal outcome or checkout: %+v", fresh)
	}
}

func activeDestroyPendingRun(t *testing.T, e *testEnv, task string) *domain.Run {
	t.Helper()
	run, _ := e.launchFake(t, task)
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close scheduler: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read active sidecar: %v", err)
	}
	sc.ContainerID = ""
	sc.Retained = false
	sc.RetainedUntil = nil
	sc.DestroyPending = true
	if err := e.sched.writeSidecar(sc); err != nil {
		t.Fatalf("write active pending sidecar: %v", err)
	}
	return run
}

func TestKillUnsupervisedActiveEmptyCIDDestroyPendingSidecar(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run := activeDestroyPendingRun(t, e, "kill active synthetic pending")
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	t.Cleanup(func() { _ = recovered.Close() })

	if err := recovered.Kill(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("startup Kill: %v", err)
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after startup Kill: %v", err)
	}
	if row.Status != domain.RunInterrupted {
		t.Fatalf("row after startup Kill = %s, want interrupted cleanup result", row.Status)
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("startup Kill left the active pending container")
	}
	recovered.mu.Lock()
	owner := recovered.runs[run.ID]
	recovered.mu.Unlock()
	if owner != nil {
		t.Fatalf("startup Kill left an active pending owner: %+v", owner)
	}
	if _, err := os.Stat(recovered.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("active pending sidecar after startup Kill: %v", err)
	}
}

func TestDeleteUnsupervisedActiveEmptyCIDDestroyPendingSidecar(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run := activeDestroyPendingRun(t, e, "delete active synthetic pending")
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	t.Cleanup(func() { _ = recovered.Close() })

	if err := recovered.DeleteRun(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("startup DeleteRun: %v", err)
	}
	if _, err := e.db.GetRun(ctx, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after startup DeleteRun: %v, want store.ErrNotFound", err)
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("startup DeleteRun left the active pending container")
	}
	if _, err := os.Stat(run.Worktree); !os.IsNotExist(err) {
		t.Fatalf("startup DeleteRun left checkout: %v", err)
	}
	if _, err := os.Stat(recovered.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("active pending sidecar after startup DeleteRun: %v", err)
	}
}

func TestKillUnsupervisedEmptyCIDDestroyPendingSidecar(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run := syntheticDestroyPendingRun(t, e, "kill synthetic pending")
	recovered := e.newScheduler(t, e.rt, newFakePTY())

	if err := recovered.Kill(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("startup Kill: %v", err)
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("startup Kill left the synthetic pending container")
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after startup Kill: %v", err)
	}
	if row.Status != domain.RunMerged || row.Reason != retainedExpiredReason {
		t.Fatalf("row after startup Kill = %+v, want retained terminal row", row)
	}
	if _, err := os.Stat(run.Worktree); err != nil {
		t.Fatalf("startup Kill removed checkout: %v", err)
	}
	if _, err := os.Stat(recovered.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("synthetic sidecar after startup Kill: %v", err)
	}
}

func TestDeleteUnsupervisedEmptyCIDDestroyPendingSidecar(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run := syntheticDestroyPendingRun(t, e, "delete synthetic pending")
	recovered := e.newScheduler(t, e.rt, newFakePTY())

	if err := recovered.DeleteRun(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("startup DeleteRun: %v", err)
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("startup DeleteRun left the synthetic pending container")
	}
	if _, err := e.db.GetRun(ctx, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after startup DeleteRun: %v, want store.ErrNotFound", err)
	}
	if _, err := os.Stat(run.Worktree); !os.IsNotExist(err) {
		t.Fatalf("startup DeleteRun left checkout: %v", err)
	}
	if _, err := os.Stat(recovered.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("synthetic sidecar after startup DeleteRun: %v", err)
	}
}

// A retained sidecar is installed before the row transition. If that write
// fails, CloseRun must return an error and restore the exact live state rather
// than publishing a terminal retained promise.
func TestCloseRunRetainedSidecarFailureRollsBack(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run, c := e.launchFake(t, "retained sidecar failure")
	stateDir := e.cfg.StateDir
	// Keep the original ownership marker intact while rejecting every sidecar
	// rename, independently of development admission/terminal state writes.
	e.sched.cfg.StateDir = t.TempDir()
	blockedSidecar := e.sched.sidecarPath(run.ID)
	if err := os.Mkdir(blockedSidecar, 0o700); err != nil {
		t.Fatal(err)
	}

	err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged)
	e.sched.cfg.StateDir = stateDir
	var renameErr *os.LinkError
	if !errors.As(err, &renameErr) || renameErr.New != blockedSidecar {
		t.Fatalf("CloseRun sidecar failure = %v, want failed rename to %s", err, blockedSidecar)
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if row.Reason != "" {
		t.Fatalf("row reason after failed retained close = %q, want empty", row.Reason)
	}
	if got := c.currentState(); got != "running" {
		t.Fatalf("container state after failed retained close = %q, want running", got)
	}
	if e.sched.Paused(run.ID) {
		t.Fatal("failed retained close left the run paused")
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar after failed retained close: %v", err)
	}
	if sc.Paused || sc.Retained || sc.RetainedUntil != nil {
		t.Fatalf("sidecar after failed retained close = %+v", sc)
	}
	if got := e.pty.ActiveSessions(string(ptyhost.RunSession(run.ID))); len(got) != 1 {
		t.Fatalf("restored PTY sessions = %v, want one", got)
	}
	if _, watching := e.git.watchingFor(run.ID); !watching {
		t.Fatal("diff watch was not restored after failed retained close")
	}
	if retryErr := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); retryErr != nil {
		t.Fatalf("CloseRun retry after sidecar failure: %v", retryErr)
	}
	closed := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if closed.Reason != retainedCloseReason {
		t.Fatalf("retry close reason = %q, want %q", closed.Reason, retainedCloseReason)
	}
	sc, err = e.sched.readSidecar(run.ID)
	if err != nil || !sc.Retained || sc.RetainedUntil == nil {
		t.Fatalf("sidecar after close retry = %+v, %v", sc, err)
	}
	if err := e.sched.DeleteRun(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun after close retry: %v", err)
	}
}

func TestCloseRunWithoutRetentionCommitsAndPublishes(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = -time.Second
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "no-retention close")

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunMerged)
	waitFor(t, "container destroyed", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	waitFor(t, "supervision released", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID] == nil
	})
	if got := e.git.commitsFor(run.ID); len(got) != 1 || got[0] != "aether: no-retention close" {
		t.Fatalf("commits = %v", got)
	}
	if e.git.publishedCount(run.ID) == 0 {
		t.Fatal("run branch was not published")
	}
	if _, err := os.Stat(e.sched.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("sidecar after no-retention close: %v", err)
	}
}

func TestCloseRunWithoutRetentionRestoresAfterTransitionFailure(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = -time.Second
	})
	ctx := t.Context()
	run, c := e.launchFake(t, "failed no-retention close")
	failingStore := &failingRunStatusStore{Store: e.db, fail: true}
	e.sched.cfg.Store = failingStore

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err == nil {
		t.Fatal("CloseRun succeeded despite terminal transition failure")
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if got := c.currentState(); got != "running" {
		t.Fatalf("container state after failed CloseRun = %q, want running", got)
	}
	if e.sched.Paused(run.ID) {
		t.Fatal("failed CloseRun left the run paused")
	}
	if got := e.pty.ActiveSessions(string(ptyhost.RunSession(run.ID))); len(got) != 1 {
		t.Fatalf("restored PTY sessions = %v, want one", got)
	}
	if _, watching := e.git.watchingFor(run.ID); !watching {
		t.Fatal("diff watch was not restored")
	}
}

// A failed terminal transition must restore a run that was already paused
// without thawing its container; the interaction surface is restored while
// the pause flag remains truthful.
func TestCloseRunRollbackPreservesPrePausedState(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "paused close rollback")
	if err := e.sched.Pause(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	failingStore := &failingRunStatusStore{Store: e.db, fail: true}
	e.sched.cfg.Store = failingStore
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err == nil {
		t.Fatal("CloseRun succeeded despite injected terminal transition failure")
	}

	if got := c.currentState(); got != "paused" {
		t.Fatalf("container state after failed close = %q, want paused", got)
	}
	if !e.sched.Paused(run.ID) {
		t.Fatal("Paused(run) = false after failed close of pre-paused run")
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar after failed close: %v", err)
	}
	if !sc.Paused {
		t.Fatal("sidecar paused = false after failed close of pre-paused run")
	}
	if got := e.pty.ActiveSessions(string(ptyhost.RunSession(run.ID))); len(got) != 1 {
		t.Fatalf("restored PTY sessions = %v, want one", got)
	}
	if _, watching := e.git.watchingFor(run.ID); !watching {
		t.Fatal("diff watch was not restored after failed close")
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)

	failingStore.fail = false
	if resumeErr := e.sched.Resume(ctx, run.ID, e.member.ID); resumeErr != nil {
		t.Fatalf("Resume after failed close: %v", resumeErr)
	}
	if got := c.currentState(); got != "running" {
		t.Fatalf("container state after Resume = %q, want running", got)
	}
	if e.sched.Paused(run.ID) {
		t.Fatal("Paused(run) = true after Resume")
	}
	sc, err = e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar after Resume: %v", err)
	}
	if sc.Paused {
		t.Fatal("sidecar paused = true after Resume")
	}
}

// A finished run is re-labeled in place by the close disposition, and a
// second close at the same outcome is a no-op. The member closing it has
// dealt with it, so an unopened exit ends opened and stays so.
func TestCloseRunRelabelsFinishedRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	c.exitNow(0)
	if row := e.waitStoreStatus(t, run.ID, domain.RunCompleted); !row.FinishUnopened {
		t.Fatal("an exit nobody asked for left the finish opened")
	}
	sub := e.subscribe(t)

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatalf("CloseRun completed to abandoned: %v", err)
	}
	if p := expectOnlyStatusEvent(t, sub, run.ID, domain.RunAbandoned); p.FinishUnopened {
		t.Fatalf("close event = %+v, want a member's close to leave the finish opened", p)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunAbandoned); row.FinishUnopened {
		t.Fatal("a member's close of an unopened run left it unopened")
	}
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun abandoned to merged: %v", err)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunMerged); row.FinishUnopened {
		t.Fatal("a member's close of an opened run left it unopened")
	}
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun at the same outcome: %v", err)
	}
}

// A member's close or kill of a run that already finished changes no status
// and still opens its finish, telling clients once: on the relabel's
// run.status when the act relabels the row, else with run.finish_opened.
func TestMemberDismissalOpensAnAlreadyFinishedRun(t *testing.T) {
	t.Parallel()
	dismissed := func(t *testing.T, e *testEnv, run domain.RunID, act func() error) []string {
		t.Helper()
		if row, err := e.db.GetRun(t.Context(), run); err != nil || !row.FinishUnopened {
			t.Fatalf("row before the member's act = %+v, %v; want an unopened finish", row, err)
		}
		sub := e.subscribe(t)
		if err := act(); err != nil {
			t.Fatalf("member's act: %v", err)
		}
		var got []string
		deadline := time.After(200 * time.Millisecond)
		for done := false; !done; {
			select {
			case ev := <-sub.Events():
				switch p := ev.Payload.(type) {
				case events.RunStatusPayload:
					got = append(got, fmt.Sprintf("%s %s unopened=%v by %s", ev.Type, p.To, p.FinishUnopened, ev.ActorID))
				case events.RunFinishOpenedPayload, events.RunOutcomeSeenPayload:
					got = append(got, fmt.Sprintf("%s by %s", ev.Type, ev.ActorID))
				}
			case <-deadline:
				done = true
			}
		}
		if row, err := e.db.GetRun(t.Context(), run); err != nil || row.FinishUnopened {
			t.Fatalf("row after the member's act = %+v, %v; want the finish opened", row, err)
		}
		return got
	}

	t.Run("close at the outcome a swarm left", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		run, _ := e.launchFake(t, "stopped by its swarm")
		if err := e.sched.Kill(ctx, run.ID, ""); err != nil {
			t.Fatalf("Kill without an actor: %v", err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
		waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
		got := dismissed(t, e, run.ID, func() error {
			return e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunAbandoned)
		})
		if want := []string{fmt.Sprintf("run.finish_opened by %s", e.member.ID)}; !slices.Equal(got, want) {
			t.Fatalf("events = %v, want %v", got, want)
		}
	})
	t.Run("kill after an exit", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		run, c := e.launchFake(t, "exited on its own")
		c.exitNow(0)
		e.waitStoreStatus(t, run.ID, domain.RunCompleted)
		waitFor(t, "container destroyed", func() bool { return e.rt.byName(string(run.ID)) == nil })
		got := dismissed(t, e, run.ID, func() error { return e.sched.Kill(ctx, run.ID, e.member.ID) })
		if want := []string{fmt.Sprintf("run.finish_opened by %s", e.member.ID)}; !slices.Equal(got, want) {
			t.Fatalf("events = %v, want %v", got, want)
		}
	})
	t.Run("kill of a retained run", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		run, _ := launchRetentionWorker(t, e, domain.LaunchTUI)
		if err := e.sched.CompleteMission(ctx, run.ID, domain.RunCompleted); err != nil {
			t.Fatalf("CompleteMission: %v", err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunCompleted)
		got := dismissed(t, e, run.ID, func() error { return e.sched.Kill(ctx, run.ID, e.member.ID) })
		if want := []string{fmt.Sprintf("run.status completed unopened=false by %s", e.member.ID)}; !slices.Equal(got, want) {
			t.Fatalf("events = %v, want %v", got, want)
		}
	})
	t.Run("release is not a dismissal", func(t *testing.T) {
		t.Parallel()
		e := newTestEnv(t, nil)
		ctx := t.Context()
		run, _ := launchRetentionWorker(t, e, domain.LaunchTUI)
		if err := e.sched.CompleteMission(ctx, run.ID, domain.RunCompleted); err != nil {
			t.Fatalf("CompleteMission: %v", err)
		}
		e.waitStoreStatus(t, run.ID, domain.RunCompleted)
		if err := e.sched.Release(ctx, run.ID, e.member.ID); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if row, err := e.db.GetRun(ctx, run.ID); err != nil || !row.FinishUnopened {
			t.Fatalf("released row = %+v, %v; want the finish still unopened", row, err)
		}
	})
}

func TestCloseRunRelabelsWhileExitCleanupFinalizes(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	barrier := &destroyBarrierRuntime{
		Runtime: e.rt,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(barrier.releaseNow)
	e.sched.cfg.Runtime = barrier

	run, c := e.launchFake(t, "close during exit cleanup")
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	select {
	case <-barrier.started:
	case <-time.After(time.Second):
		t.Fatal("container destroy did not start")
	}

	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun while finalizing: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunMerged)

	barrier.releaseNow()
	waitFor(t, "completed run removed from supervision", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID] == nil
	})
}

func TestInjectLiveStalledNeedsAttention(t *testing.T) {
	t.Parallel()
	const stallThreshold = 500 * time.Millisecond
	e := newTestEnv(t, func(cfg *Config) {
		cfg.StallThreshold = stallThreshold
		cfg.PollInterval = 20 * time.Millisecond
	})
	sub := e.subscribe(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-startDone })

	run, c := e.launchFake(t, "task")
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	if _, err := e.sched.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: "keep going"}, false, nil); err != nil {
		t.Fatalf("Inject stalled: %v", err)
	}
	inj := e.pty.injected()
	if len(inj) != 1 || inj[0].message != "keep going" {
		t.Fatalf("injects = %+v", inj)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineSteer)

	// The steer by itself does not clear the stall. Its banner is the
	// server's own output, and this agent never answers, so unparking here
	// would hide the hang for another whole threshold.
	quiet := time.After(5 * stallThreshold)
	for watching := true; watching; {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatal("event stream closed while watching the stalled run")
			}
			p, isStatus := ev.Payload.(events.RunStatusPayload)
			if isStatus && ev.RunID == run.ID && p.To == domain.RunRunning {
				t.Fatalf("steer alone returned the run to running (%q); only the agent's own output should",
					p.Reason)
			}
		case <-quiet:
			watching = false
		}
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				c.output("got:keep going\r\n")
			}
		}
	}()
	resumed := waitStatusEvent(t, sub, run.ID, domain.RunRunning)
	if p := resumed.Payload.(events.RunStatusPayload); p.Reason != "activity resumed" {
		t.Fatalf("resume reason = %q", p.Reason)
	}
}

func TestInjectCleanExitedCompleted(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, c := e.launchFake(t, "task")
	c.exitNow(0)
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	waitFor(t, "completed run removed from supervision", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		_, ok := e.sched.runs[run.ID]
		return !ok
	})

	_, err := e.sched.Inject(ctx, run.ID, e.member.ID, domain.AgentPrompt{Text: "too late"}, false, nil)
	if !errors.Is(err, ptyhost.ErrNoSession) {
		t.Fatalf("Inject completed = %v, want ErrNoSession", err)
	}
}

// DeleteRun's redundant Kill must survive the kill that is already in
// flight: the first kill's finalize can transition the status and destroy
// the container while the delete's own Stop call is in the air, and the
// delete must still remove the checkout, transcripts, and run record.
func TestDeleteRunSurvivesKillFinalizingDuringStop(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "delete during kill")

	entered := make(chan struct{})
	release := make(chan struct{})
	e.rt.stopHook = func() {
		close(entered)
		<-release
	}

	errCh := make(chan error, 1)
	go func() { errCh <- e.sched.DeleteRun(t.Context(), run.ID, e.member.ID) }()
	<-entered // DeleteRun's Kill sits inside Stop; its status check already passed.

	e.sched.mu.Lock()
	cid := e.sched.runs[run.ID].containerID
	e.sched.mu.Unlock()
	// The first kill's effect: the process ends and finalization destroys
	// the container and releases the entry while the redundant stop is parked.
	if err := e.rt.Destroy(t.Context(), cid); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	close(release)
	waitFor(t, "finalization released the entry", func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		return e.sched.runs[run.ID] == nil
	})

	if err := <-errCh; err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if _, err := e.sched.cfg.Store.GetRun(t.Context(), run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after delete = %v, want not found", err)
	}
}

func TestDeleteRunReconcilesRetainedSidecarAfterProbeError(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "delete retained sidecar")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// An inconclusive recovery probe keeps the durable sidecar represented by
	// an in-memory retry owner.
	e.rt.setWaitError(errors.New("test: retained probe inconclusive"))
	destroyErr := errors.New("test: retained destroy failed")
	rt := &destroyFailureRuntime{Runtime: e.rt, destroyErr: destroyErr}
	cfg := e.cfg
	cfg.Runtime = rt
	cfg.PTY = newFakePTY()
	recovered, err := New(cfg)
	if err != nil {
		t.Fatalf("New recovered scheduler: %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	if recoverErr := recovered.recoverRuns(ctx); recoverErr != nil {
		t.Fatalf("recoverRuns: %v", recoverErr)
	}
	recovered.mu.Lock()
	entry := recovered.runs[run.ID]
	recovered.mu.Unlock()
	if entry == nil || !entry.retained || !entry.waitStarted {
		t.Fatalf("retained ownership after inconclusive recovery = %+v", entry)
	}

	err = recovered.DeleteRun(ctx, run.ID, e.member.ID)
	if !errors.Is(err, destroyErr) || !strings.Contains(err.Error(), "destroy retained container") {
		t.Fatalf("DeleteRun with failed destroy = %v, want contextual destroy error", err)
	}
	if _, statErr := os.Stat(recovered.sidecarPath(run.ID)); statErr != nil {
		t.Fatalf("sidecar after failed delete: %v", statErr)
	}
	if _, statErr := os.Stat(run.Worktree); statErr != nil {
		t.Fatalf("checkout after failed delete: %v", statErr)
	}
	if _, getErr := e.db.GetRun(ctx, run.ID); getErr != nil {
		t.Fatalf("run row after failed delete: %v", getErr)
	}
	if e.rt.byName(string(run.ID)) == nil {
		t.Fatal("container removed after failed delete")
	}
	recovered.mu.Lock()
	entry = recovered.runs[run.ID]
	recovered.mu.Unlock()
	if entry == nil || !entry.retained || !entry.waitStarted {
		t.Fatalf("retained ownership after failed delete = %+v", entry)
	}

	rt.destroyErr = nil
	if err := recovered.DeleteRun(ctx, run.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun retry: %v", err)
	}
	if _, getErr := e.db.GetRun(ctx, run.ID); !errors.Is(getErr, store.ErrNotFound) {
		t.Fatalf("run row after successful retry: %v", getErr)
	}
	if _, statErr := os.Stat(recovered.sidecarPath(run.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("sidecar after successful retry: %v", statErr)
	}
	if _, statErr := os.Stat(run.Worktree); !os.IsNotExist(statErr) {
		t.Fatalf("checkout after successful retry: %v", statErr)
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("container remains after successful retry")
	}
}
