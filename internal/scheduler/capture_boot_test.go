package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// checkoutVMs is saveRuntime with each VM's git service: it serves from a
// boot until the VM is saved or powers off, also after a stop ended the
// session's main process, as a VM lingers for its checkout. BootForCapture
// starts a stopped VM and discards its saved state, as a cold start does.
type checkoutVMs struct {
	*saveRuntime
	// refuse fails the capacity probe, so admission refuses.
	refuse atomic.Bool
	boots  atomic.Int32

	mu      sync.Mutex
	down    map[runtime.ID]bool
	bootErr error
}

func (r *checkoutVMs) setDown(id runtime.ID, down bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down[id] = down
}

func (r *checkoutVMs) serves(run domain.RunID) bool {
	c := r.byName(string(run))
	r.mu.Lock()
	defer r.mu.Unlock()
	return c != nil && !r.down[c.id]
}

func (r *checkoutVMs) Save(ctx context.Context, id runtime.ID) error {
	err := r.saveRuntime.Save(ctx, id)
	if err == nil {
		r.setDown(id, true)
	}
	return err
}

func (r *checkoutVMs) Restore(ctx context.Context, id runtime.ID) error {
	err := r.saveRuntime.Restore(ctx, id)
	if err == nil {
		r.setDown(id, false)
	}
	return err
}

func (r *checkoutVMs) BootForCapture(ctx context.Context, id runtime.ID) error {
	r.boots.Add(1)
	r.mu.Lock()
	err := r.bootErr
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.saveMu.Lock()
	delete(r.saved, id)
	r.saveMu.Unlock()
	if err = r.Start(ctx, id); err != nil {
		return err
	}
	r.setDown(id, false)
	return nil
}

func (r *checkoutVMs) Capacity(context.Context) (runtime.HostCapacity, error) {
	if r.refuse.Load() {
		return runtime.HostCapacity{}, errors.New("capacity probe failed")
	}
	return healthyAdmissionHost(), nil
}

// powerOff ends c's VM as one that died does, or one whose linger ran out.
func (r *checkoutVMs) powerOff(c *fakeContainer, code int) {
	r.setDown(c.id, true)
	c.exitNow(code)
}

// vmCheckoutGit is fakeGit for checkouts on their VM's disk: a commit needs
// the VM's git service, and records the tree evidence retains once the VM is
// down, until the session runs again.
type vmCheckoutGit struct {
	*fakeGit
	rt         *checkoutVMs
	failCommit atomic.Bool

	mu    sync.Mutex
	final map[domain.RunID]bool
}

func (g *vmCheckoutGit) CommitAll(ctx context.Context, run domain.RunID, message string, author domain.GitIdentity, signingKey []byte) (string, error) {
	if !g.rt.serves(run) {
		return "", fmt.Errorf("fake git: session %s: %w", run, guest.ErrVMNotRunning)
	}
	if g.failCommit.CompareAndSwap(true, false) {
		return "", errors.New("fake git: commit failed")
	}
	commit, err := g.fakeGit.CommitAll(ctx, run, message, author, signingKey)
	if err == nil {
		g.mu.Lock()
		g.final[run] = true
		g.mu.Unlock()
	}
	return commit, err
}

func (g *vmCheckoutGit) StartDiffWatch(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) error {
	g.mu.Lock()
	delete(g.final, run)
	g.mu.Unlock()
	return g.fakeGit.StartDiffWatch(ctx, workspace, run)
}

func (g *vmCheckoutGit) evidenceErr(run domain.RunID) error {
	g.mu.Lock()
	final := g.final[run]
	g.mu.Unlock()
	if final || g.rt.serves(run) {
		return nil
	}
	return fmt.Errorf("gitengine: session %s has uncommitted work its VM holds: %w", run, guest.ErrVMNotRunning)
}

func newVMEnv(t *testing.T, mutate func(*Config)) (*testEnv, *checkoutVMs, *vmCheckoutGit, *schedulerEvidenceCapture) {
	t.Helper()
	var vr *checkoutVMs
	var vg *vmCheckoutGit
	e, _ := newACPEnv(t, func(cfg *Config) {
		cfg.VMSleepAfter = time.Nanosecond
		sr := &saveRuntime{acpRuntime: cfg.Runtime.(*acpRuntime), saved: map[runtime.ID]string{}}
		vr = &checkoutVMs{saveRuntime: sr, down: map[runtime.ID]bool{}}
		cfg.Runtime = vr
		vg = &vmCheckoutGit{fakeGit: cfg.Git.(*fakeGit), rt: vr, final: map[domain.RunID]bool{}}
		cfg.Git = vg
		cfg.hostMemory = func() (uint64, uint64, error) { return 32 << 30, 24 << 30, nil }
		if mutate != nil {
			mutate(cfg)
		}
	})
	e.onGuestCPU("0")
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.gitErr = vg.evidenceErr
	e.sched.UseEvidence(capture)
	return e, vr, vg, capture
}

func (e *testEnv) end(t *testing.T, run domain.RunID, how string) error {
	t.Helper()
	if how == "kill" {
		return e.sched.Kill(t.Context(), run, e.member.ID)
	}
	return e.sched.CloseRun(t.Context(), run, e.member.ID, domain.RunAbandoned)
}

func (e *testEnv) waitTerminal(t *testing.T, run domain.RunID) *domain.Run {
	t.Helper()
	var got *domain.Run
	waitFor(t, "the session ended", func() bool {
		got, _ = e.db.GetRun(t.Context(), run)
		return got != nil && got.Status.Terminal()
	})
	return got
}

// A sleeping VM that cannot wake, because its restore fails or admission
// refuses the wake, ends asleep, its work in the stopped VM: the capture
// boots the VM to commit it, and stops it again.
func TestEndingAVMThatCannotWakeBootsItToCommit(t *testing.T) {
	t.Parallel()
	for _, why := range []string{"restore", "admission"} {
		for _, how := range []string{"kill", "close"} {
			t.Run(why+" "+how, func(t *testing.T) {
				t.Parallel()
				endAVMThatCannotWake(t, why, how)
			})
		}
	}
}

func endAVMThatCannotWake(t *testing.T, why, how string) {
	e, vr, _, capture := newVMEnv(t, nil)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	c := e.container(t, run.ID)
	if why == "admission" {
		vr.refuse.Store(true)
	} else {
		vr.saveMu.Lock()
		vr.restoreErr = errors.New("restore: monitor exited")
		vr.saveMu.Unlock()
	}
	if err := e.end(t, run.ID, how); err != nil {
		t.Fatalf("%s of a VM that cannot wake: %v", how, err)
	}
	e.waitTerminal(t, run.ID)
	waitFor(t, "the work committed and captured", func() bool {
		return len(e.git.commitsFor(run.ID)) == 1 && e.git.publishedCount(run.ID) > 0 && capture.packetCount() == 1
	})
	if vr.boots.Load() != 1 {
		t.Fatalf("booted %d times for capture, want once", vr.boots.Load())
	}
	waitFor(t, "the VM stopped again", func() bool {
		return e.rt.byName(string(run.ID)) == nil || containerState(c) == "stopped"
	})
}

func TestAVMThatDiedIsBootedToCommitItsWork(t *testing.T) {
	t.Parallel()
	e, vr, _, capture := newVMEnv(t, nil)
	run, c := e.launchFake(t, "die")
	waitFor(t, "the VM running", func() bool { return containerState(c) == "running" })
	vr.powerOff(c, 255)
	waitFor(t, "the session's VM removed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	if got := e.git.commitsFor(run.ID); len(got) != 1 || !strings.HasPrefix(got[0], "wip: ") || vr.boots.Load() != 1 || capture.packetCount() != 1 {
		t.Fatalf("commits %q, %d capture boots, %d packets; want the work committed by one boot and captured", got, vr.boots.Load(), capture.packetCount())
	}
}

// A close whose commit failed leaves no record of what the checkout holds,
// so a capture after the closed VM's linger boots it to commit the work.
func TestAFailedCloseCommitIsMadeByBootingTheVM(t *testing.T) {
	t.Parallel()
	e, vr, vg, capture := newVMEnv(t, nil)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	vg.failCommit.Store(true)
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunAbandoned); err != nil {
		t.Fatal(err)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 0 {
		t.Fatalf("the close committed %q, want its commit to fail", got)
	}
	waitFor(t, "the closed VM stopped", func() bool { return containerState(e.container(t, run.ID)) == "stopped" })
	vr.setDown(e.cid(t, run.ID), true)
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Release(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Release of the closed VM: %v", err)
	}
	if got := e.git.commitsFor(run.ID); len(got) != 1 || vr.boots.Load() != 1 || capture.packetCount() != 2 {
		t.Fatalf("commits %q, %d capture boots, %d packets; want the work committed by one boot and captured as merged", got, vr.boots.Load(), capture.packetCount())
	}
	if e.rt.byName(string(run.ID)) != nil {
		t.Fatal("Release kept the closed VM")
	}
}

// A finish handled after the VM's linger ran out, here by the next server,
// boots the VM to commit the work, as completed work since the agent exited 0.
func TestAFinishAfterTheLingerBootsTheVMToCommit(t *testing.T) {
	t.Parallel()
	e, vr, _, capture := newVMEnv(t, nil)
	run, c := e.launchFake(t, "exit while the server is down")
	waitFor(t, "the VM running", func() bool { return containerState(c) == "running" })
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	vr.powerOff(c, 0)
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restarted.UseEvidence(capture)
	startScheduler(t, restarted)
	<-restarted.RecoveryReady()
	waitFor(t, "the session's VM removed", func() bool { return e.rt.byName(string(run.ID)) == nil })
	if got := e.git.commitsFor(run.ID); len(got) != 1 || !strings.HasPrefix(got[0], "aether: ") || vr.boots.Load() != 1 || capture.packetCount() != 1 {
		t.Fatalf("commits %q, %d capture boots, %d packets; want the completed work committed by one boot and captured", got, vr.boots.Load(), capture.packetCount())
	}
}

// Boots that keep failing back off, and the checkout stays however many
// fail. Once the VM is gone, no boot can commit its work: the session's
// reason records the work as lost and its resources are freed.
func TestFailingCaptureBootsKeepTheCheckoutUntilTheVMIsGone(t *testing.T) {
	t.Parallel()
	clock, advance := movableClock()
	e, vr, _, capture := newVMEnv(t, func(cfg *Config) {
		cfg.Now = clock
		cfg.CheckoutTTL = time.Hour
	})
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	e.sleepRun(t, run.ID)
	vr.saveMu.Lock()
	vr.restoreErr = errors.New("restore: monitor exited")
	vr.saveMu.Unlock()
	vr.mu.Lock()
	vr.bootErr = errors.New("boot: no memory")
	vr.mu.Unlock()
	if err := e.end(t, run.ID, "close"); err == nil || !strings.Contains(err.Error(), "boot: no memory") {
		t.Fatalf("close of a VM that cannot boot = %v, want the boot's error", err)
	}
	worktree := e.waitTerminal(t, run.ID).Worktree

	e.sched.sweepRetained(t.Context())
	if vr.boots.Load() != 1 {
		t.Fatalf("a sweep before the backoff booted again: %d boots", vr.boots.Load())
	}
	for range 7 {
		advance(cleanupRetryMax)
		e.sched.sweepRetained(t.Context())
	}
	if got := vr.boots.Load(); got != 8 {
		t.Fatalf("%d capture boots, want one each time the backoff passed", got)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("the checkout went while its VM could still commit it: %v", err)
	}
	if got, _ := e.db.GetRun(t.Context(), run.ID); got != nil {
		if _, note := withoutCleanupNote(got.Reason); note != "" {
			t.Fatalf("reason %q records an outcome while the VM is there", got.Reason)
		}
	}

	if err := e.rt.Destroy(t.Context(), e.cid(t, run.ID)); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Release(t.Context(), run.ID, e.member.ID); err == nil || !strings.Contains(err.Error(), "again at") {
		t.Fatalf("Release while the last boot backs off = %v, want the backoff named", err)
	}
	advance(cleanupRetryMax)
	if err := e.sched.Release(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatalf("Release once the VM is gone: %v", err)
	}
	got, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, note := withoutCleanupNote(got.Reason); note != workUncapturedReason {
		t.Fatalf("reason %q, want it to record the work as lost", got.Reason)
	}
	old := clock().Add(-2 * time.Hour)
	got.FinishedAt = &old
	if err = e.db.UpdateRun(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	e.sched.sweepCheckouts(t.Context(), clock().Add(-time.Hour))
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("the checkout of a session whose work is recorded lost stayed: %v", err)
	}
	if capture.packetCount() != 0 || vr.boots.Load() != 8 {
		t.Fatalf("%d packets and %d boots after the outcome", capture.packetCount(), vr.boots.Load())
	}
}
