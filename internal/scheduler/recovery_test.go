package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
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
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
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
	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if ev.WorkspaceID != e.ws.ID {
		t.Fatalf("recovered event workspace = %q, want %q", ev.WorkspaceID, e.ws.ID)
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

func TestRelaunchFromInterrupted(t *testing.T) {
	e := newTestEnv(t, nil)

	run, _ := e.launchFake(t, "interrupted work")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rt2 := newFakeRuntime()
	s2 := e.newScheduler(t, rt2, newFakePTY())
	startScheduler(t, s2)
	old := e.waitStoreStatus(t, run.ID, domain.RunInterrupted)

	member2 := &domain.Member{DisplayName: "Grace", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err := e.db.CreateMember(t.Context(), member2); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	next, err := s2.Relaunch(t.Context(), run.ID, member2.ID)
	if err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if next.ID == old.ID {
		t.Fatal("relaunch must create a new run row")
	}
	if next.Worktree == old.Worktree || next.Worktree == "" {
		t.Fatalf("relaunch must create a distinct worktree, got %q (old %q)", next.Worktree, old.Worktree)
	}
	if next.Branch == old.Branch || next.Branch == "" {
		t.Fatalf("relaunch must create a new branch, got %q (old %q)", next.Branch, old.Branch)
	}
	if e.git.baseBranchFor(next.ID) != old.Branch {
		t.Fatalf("CreateRunCheckout base = %q, want old branch %q", e.git.baseBranchFor(next.ID), old.Branch)
	}
	if next.MemberID != member2.ID || next.Task != old.Task || next.Harness != old.Harness || next.Mode != old.Mode {
		t.Fatalf("relaunched run = %+v", next)
	}
	if next.Status != domain.RunRunning {
		t.Fatalf("relaunched run status = %s", next.Status)
	}
	if e.git.checkoutCount() != 2 {
		t.Fatalf("checkout count = %d, want 2 (old checkout survives)", e.git.checkoutCount())
	}
	if _, err = os.Stat(old.Worktree); err != nil {
		t.Fatalf("old checkout must survive relaunch: %v", err)
	}
	oldAgain, err := e.db.GetRun(t.Context(), old.ID)
	if err != nil {
		t.Fatalf("GetRun old: %v", err)
	}
	if oldAgain.Status != domain.RunInterrupted {
		t.Fatalf("old run mutated to %s", oldAgain.Status)
	}

	c := rt2.byName(string(next.ID))
	if c == nil {
		t.Fatal("no container for relaunched run")
	}
	c.exitNow(0)
	e.waitStoreStatus(t, next.ID, domain.RunNeedsAttention)
}

func TestRelaunchRejectsUnpublishedBranch(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := e.launchFake(t, "unpublished work")
	if err := e.sched.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2 := e.newScheduler(t, newFakeRuntime(), newFakePTY())
	startScheduler(t, s2)
	old := e.waitStoreStatus(t, run.ID, domain.RunInterrupted)
	if _, err := os.Stat(old.Worktree); err != nil {
		t.Fatalf("preserved checkout: %v", err)
	}
	published, err := e.git.WorkspaceBranchExists(t.Context(), e.ws.ID, old.Branch)
	if err != nil {
		t.Fatalf("WorkspaceBranchExists: %v", err)
	}
	if !published {
		t.Fatal("recovery did not publish source branch")
	}
	e.git.unpublishBranch(e.ws.ID, old.Branch)

	before, err := e.db.ListRunsByWorkspace(t.Context(), old.WorkspaceID)
	if err != nil {
		t.Fatalf("ListRunsByWorkspace before relaunch: %v", err)
	}
	next, err := s2.Relaunch(t.Context(), old.ID, e.member.ID)
	if next != nil {
		t.Fatalf("Relaunch returned run %+v for unpublished branch", next)
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Relaunch error = %v, want ErrInvalidTransition", err)
	}
	wantErr := ErrInvalidTransition.Error() + ": " + relaunchRequiresCheckout
	if err.Error() != wantErr {
		t.Fatalf("Relaunch error = %q, want %q", err, wantErr)
	}
	after, listErr := e.db.ListRunsByWorkspace(t.Context(), old.WorkspaceID)
	if listErr != nil {
		t.Fatalf("ListRunsByWorkspace after relaunch: %v", listErr)
	}
	if len(after) != len(before) {
		t.Fatalf("run count = %d after rejected relaunch, want %d", len(after), len(before))
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
		path, _, err := e.git.CreateRunCheckout(ctx, e.ws.ID, r.ID, "main", task)
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

	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
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

	ev := waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("reason = %q", p.Reason)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
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
	// (c) Status already needs-attention, destroy not done: cleanup the
	// leftover container, do not reattach, do not mark interrupted.
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
	if err = e.db.UpdateRunStatus(ctx, run.ID, domain.RunNeedsAttention, "", nil, nil); err != nil {
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
	if fresh.Status != domain.RunNeedsAttention {
		t.Fatalf("status = %s, want needs-attention (not interrupted)", fresh.Status)
	}
	if e.rt.attachCount() != before {
		t.Fatalf("reattached leftover container: attaches %d -> %d", before, e.rt.attachCount())
	}
	if pty2.session(run.ID) != nil {
		t.Fatal("must not reattach a leftover stopped container")
	}
}

// TestRelaunchResumesTheHarnessSession pins the failure table's server
// reboot row down to the argv: a launch names its own conversation with
// --session-id, relaunching a run the reboot interrupted resumes that exact
// ID, and relaunching a run that simply finished starts a fresh one. The
// registry's claude profile is used directly because a Config.Harnesses
// override deliberately drops the registry's flags - nothing checks the
// override is still that CLI.
func TestRelaunchResumesTheHarnessSession(t *testing.T) {
	e := newTestEnv(t, nil)

	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, "resume me", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	session := run.HarnessSessionID
	if _, perr := uuid.Parse(session); perr != nil {
		t.Fatalf("launch pinned session %q is not a UUID: %v", session, perr)
	}
	launched := e.rt.byName(string(run.ID))
	if launched == nil {
		t.Fatal("no container for the launched run")
	}
	wantLaunch := []string{"claude", "--session-id", session, "--dangerously-skip-permissions", "resume me"}
	if !slices.Equal(launched.spec.Command, wantLaunch) {
		t.Fatalf("launch argv = %v, want the pinned session: %v", launched.spec.Command, wantLaunch)
	}
	if cerr := e.sched.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// The reboot: a new scheduler on a runtime whose containers are gone
	// interrupts the run and preserves its checkout.
	rt2 := newFakeRuntime()
	s2 := e.newScheduler(t, rt2, newFakePTY())
	startScheduler(t, s2)
	e.waitStoreStatus(t, run.ID, domain.RunInterrupted)

	next, err := s2.Relaunch(t.Context(), run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	c := rt2.byName(string(next.ID))
	if c == nil {
		t.Fatal("no container for the relaunched run")
	}
	want := []string{"claude", "--resume", session, "--dangerously-skip-permissions", "resume me"}
	if !slices.Equal(c.spec.Command, want) {
		t.Fatalf("relaunch of an interrupted run = %v, want %v", c.spec.Command, want)
	}
	if next.HarnessSessionID != session {
		t.Fatalf("relaunched run session = %q, want the resumed one %q", next.HarnessSessionID, session)
	}

	// A run that reached a terminal state on its own has no conversation to
	// resume, so its relaunch starts the agent fresh on a session of its own.
	c.exitNow(0)
	e.waitStoreStatus(t, next.ID, domain.RunNeedsAttention)
	if cerr := s2.CloseRun(t.Context(), next.ID, e.member.ID, domain.RunMerged); cerr != nil {
		t.Fatalf("CloseRun: %v", cerr)
	}
	after, err := s2.Relaunch(t.Context(), next.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch of a merged run: %v", err)
	}
	fresh := rt2.byName(string(after.ID))
	if fresh == nil {
		t.Fatal("no container for the second relaunch")
	}
	if slices.Contains(fresh.spec.Command, "--resume") || slices.Contains(fresh.spec.Command, "--continue") {
		t.Fatalf("relaunch of a merged run = %v, want no resume flag", fresh.spec.Command)
	}
	if after.HarnessSessionID == "" || after.HarnessSessionID == session {
		t.Fatalf("relaunch of a merged run session = %q, want a new one", after.HarnessSessionID)
	}
}

// relaunchSessionArgv returns the session flag and ID a relaunch of run
// used, so a test can assert which of the three paths it took.
func relaunchSessionArgv(t *testing.T, rt *fakeRuntime, run *domain.Run) (string, string) {
	t.Helper()
	c := rt.byName(string(run.ID))
	if c == nil {
		t.Fatalf("no container for run %s", run.ID)
	}
	argv := c.spec.Command
	for i, a := range argv {
		switch a {
		case "--session-id", "--resume":
			if i+1 >= len(argv) {
				t.Fatalf("argv %v: %s has no value", argv, a)
			}
			return a, argv[i+1]
		case "--continue":
			return a, ""
		}
	}
	return "", ""
}

// TestRelaunchOfARunThatNeverStartedPinsAFreshSession covers a queued or
// provisioning row that the reboot interrupted. Its session ID was stamped
// when the row was created, so no transcript stands behind it and
// claude --resume would exit 1 with "No conversation found with session
// ID". The relaunch must open a conversation of its own instead.
func TestRelaunchOfARunThatNeverStartedPinsAFreshSession(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, "never spoke", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	stamped := run.HarnessSessionID
	// Roll the row back to what a launch that died during provisioning
	// leaves behind: a checkout, a stamped session, and no agent that ever
	// ran. Recovery interrupts it exactly like a running row.
	run.Status, run.StartedAt = domain.RunQueued, nil
	if uerr := e.db.UpdateRun(ctx, run); uerr != nil {
		t.Fatalf("roll the row back to queued: %v", uerr)
	}
	if cerr := e.sched.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	rt2 := newFakeRuntime()
	s2 := e.newScheduler(t, rt2, newFakePTY())
	startScheduler(t, s2)
	interrupted := e.waitStoreStatus(t, run.ID, domain.RunInterrupted)
	if interrupted.StartedAt != nil {
		t.Fatal("the rolled-back row must stay unstarted")
	}

	next, err := s2.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	flag, id := relaunchSessionArgv(t, rt2, next)
	if flag != "--session-id" {
		t.Fatalf("relaunch of an unstarted run used %q, want a fresh --session-id", flag)
	}
	if id == stamped {
		t.Fatalf("relaunch reused the stamped session %q, which has no transcript", stamped)
	}
	if next.HarnessSessionID != id {
		t.Fatalf("run row session = %q, want the launched %q", next.HarnessSessionID, id)
	}
}

// TestRelaunchByAnotherMemberPinsAFreshSession covers the collaborator
// relaunch that steer_others allows and a handoff creates. The container
// mounts the actor's credential home, so the owner's transcript is not
// there to resume.
func TestRelaunchByAnotherMemberPinsAFreshSession(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, "not yours", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	owned := run.HarnessSessionID
	if cerr := e.sched.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	rt2 := newFakeRuntime()
	s2 := e.newScheduler(t, rt2, newFakePTY())
	startScheduler(t, s2)
	e.waitStoreStatus(t, run.ID, domain.RunInterrupted)

	other := &domain.Member{DisplayName: "Grace", PublicKey: testPublicKey(t), Color: "#3cb44b", Role: domain.RoleCollaborator}
	if cerr := e.db.CreateMember(ctx, other); cerr != nil {
		t.Fatalf("CreateMember: %v", cerr)
	}
	next, err := s2.Relaunch(ctx, run.ID, other.ID)
	if err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	flag, id := relaunchSessionArgv(t, rt2, next)
	if flag != "--session-id" {
		t.Fatalf("relaunch by another member used %q, want a fresh --session-id", flag)
	}
	if id == owned {
		t.Fatalf("relaunch resumed %q from another member's home", owned)
	}
}

// TestRelaunchTwiceRefusesToShareOneConversation pins the guard that keeps
// two agents from appending to one transcript. The checkout guard cannot
// catch this: the second relaunch gets a checkout of its own.
func TestRelaunchTwiceRefusesToShareOneConversation(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()

	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, "resume me once", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if cerr := e.sched.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	rt2 := newFakeRuntime()
	s2 := e.newScheduler(t, rt2, newFakePTY())
	startScheduler(t, s2)
	e.waitStoreStatus(t, run.ID, domain.RunInterrupted)

	first, err := s2.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("first Relaunch: %v", err)
	}
	before, err := e.db.ListRunsByWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		t.Fatalf("ListRunsByWorkspace: %v", err)
	}

	second, err := s2.Relaunch(ctx, run.ID, e.member.ID)
	if second != nil {
		t.Fatalf("second Relaunch returned run %+v, want a refusal", second)
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second Relaunch error = %v, want ErrInvalidTransition", err)
	}
	wantErr := ErrInvalidTransition.Error() +
		": agent conversation already resumed by active run " + string(first.ID)
	if err.Error() != wantErr {
		t.Fatalf("second Relaunch error = %q, want %q", err, wantErr)
	}
	after, err := e.db.ListRunsByWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		t.Fatalf("ListRunsByWorkspace after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("run count = %d after the refused relaunch, want %d", len(after), len(before))
	}

	// Once the first relaunch is done with the conversation, relaunching
	// the original row resumes it again: the two agents never overlap.
	c := rt2.byName(string(first.ID))
	if c == nil {
		t.Fatal("no container for the first relaunch")
	}
	c.exitNow(0)
	e.waitStoreStatus(t, first.ID, domain.RunNeedsAttention)
	if cerr := s2.CloseRun(ctx, first.ID, e.member.ID, domain.RunMerged); cerr != nil {
		t.Fatalf("CloseRun: %v", cerr)
	}
	third, err := s2.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch after the first one finished: %v", err)
	}
	flag, id := relaunchSessionArgv(t, rt2, third)
	if flag != "--resume" || id != run.HarnessSessionID {
		t.Fatalf("relaunch after the conversation was free = %s %s, want --resume %s",
			flag, id, run.HarnessSessionID)
	}
}

// TestRelaunchWithoutAPinnedSessionFallsBackToContinue covers the rows that
// predate session pinning: they carry no session ID, so the relaunch keeps
// the documented best-effort behavior instead of dropping resume entirely.
func TestRelaunchWithoutAPinnedSessionFallsBackToContinue(t *testing.T) {
	e := newTestEnv(t, nil)

	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, "resume me", "claude", domain.LaunchTUI)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// Age the row back to what a pre-pinning launch wrote.
	run.HarnessSessionID = ""
	if uerr := e.db.UpdateRun(t.Context(), run); uerr != nil {
		t.Fatalf("clear pinned session: %v", uerr)
	}
	if cerr := e.sched.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	rt2 := newFakeRuntime()
	s2 := e.newScheduler(t, rt2, newFakePTY())
	startScheduler(t, s2)
	e.waitStoreStatus(t, run.ID, domain.RunInterrupted)

	next, err := s2.Relaunch(t.Context(), run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	c := rt2.byName(string(next.ID))
	if c == nil {
		t.Fatal("no container for the relaunched run")
	}
	want := []string{"claude", "--continue", "--dangerously-skip-permissions", "resume me"}
	if !slices.Equal(c.spec.Command, want) {
		t.Fatalf("relaunch without a pinned session = %v, want %v", c.spec.Command, want)
	}
	if next.HarnessSessionID != "" {
		t.Fatalf("--continue relaunch recorded session %q, want none to pin", next.HarnessSessionID)
	}
}
