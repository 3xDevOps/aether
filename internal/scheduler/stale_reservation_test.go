package scheduler

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/store"
)

func checkoutMissing(run domain.RunID) error {
	return fmt.Errorf("evidence: retain git state: %w: %s", gitengine.ErrCheckoutNotFound, run)
}

// The Nov 3 incident: a merged session lost its checkout, and its pending
// cleanup kept "<unknown>" reserved against every launch.
func TestRecoveryFinishesClosedRunWithoutCheckout(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run := syntheticDestroyPendingRun(t, e, "merged session lost its checkout")
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	sc.EvidencePending = true
	if err := e.sched.writeSidecar(sc); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	recovered := e.newScheduler(t, e.rt, newFakePTY())
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.refusal = checkoutMissing(run.ID)
	recovered.UseEvidence(capture)
	if err := recovered.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}

	recovered.mu.Lock()
	owner := recovered.runs[run.ID]
	recovered.mu.Unlock()
	if owner != nil {
		t.Fatalf("cleanup left an owner: %+v", owner)
	}
	if _, err := os.Stat(recovered.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("sidecar after cleanup: %v", err)
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if !strings.Contains(row.Reason, evidenceUnavailableReason) {
		t.Fatalf("reason = %q, want %q", row.Reason, evidenceUnavailableReason)
	}
	next := &supervised{runID: "run-next", memberID: e.member.ID}
	if err := recovered.reserveRunUser(ctx, next, "1000:1000", true); err != nil {
		t.Fatalf("launch after cleanup: %v", err)
	}
}

// Cleanup that keeps failing for another reason still retries, but a
// container the runtime confirmed gone no longer holds the home.
func TestGoneContainerReleasesHomeWhileCleanupRetries(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run := syntheticDestroyPendingRun(t, e, "evidence keeps failing")
	if c := e.rt.byName(string(run.ID)); c != nil {
		if err := e.rt.Destroy(ctx, c.id); err != nil {
			t.Fatalf("remove container: %v", err)
		}
	}

	recovered := e.newScheduler(t, e.rt, newFakePTY())
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.refusal = errors.New("evidence store unavailable")
	recovered.UseEvidence(capture)
	if err := recovered.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	recovered.mu.Lock()
	owner := recovered.runs[run.ID]
	pending := owner != nil && owner.destroyPending && owner.runUser == "" && owner.userReservation == nil
	recovered.mu.Unlock()
	if !pending {
		t.Fatalf("owner = %+v, want a pending cleanup without a reservation", owner)
	}
	next := &supervised{runID: "run-next", memberID: e.member.ID}
	if err := recovered.reserveRunUser(ctx, next, "1000:1000", true); err != nil {
		t.Fatalf("launch while cleanup retries: %v", err)
	}

	capture.mu.Lock()
	capture.refusal = nil
	capture.mu.Unlock()
	if err := recovered.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("closing the pending session again: %v", err)
	}
	recovered.mu.Lock()
	owner = recovered.runs[run.ID]
	recovered.mu.Unlock()
	if owner != nil {
		t.Fatalf("second close left the owner: %+v", owner)
	}
}

// A container the runtime cannot answer for keeps blocking, and the launch
// says why; once the runtime confirms it gone, the next launch proceeds.
func TestUnknownReservationResolvesAtLaunch(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run := syntheticDestroyPendingRun(t, e, "runtime down at boot")

	lookupErr := errors.New("runtime API unavailable")
	flaky := &creationKeyFailureRuntime{Runtime: e.rt, findErr: lookupErr}
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	recovered.cfg.Runtime = flaky
	if err := recovered.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}

	next := &supervised{runID: "run-next", memberID: e.member.ID}
	err := recovered.reserveRunUser(ctx, next, "1000:1000", true)
	if err == nil {
		t.Fatal("launch accepted while the pending container's identity is unknown")
	}
	for _, want := range []string{"session " + string(run.ID) + " (cleanup pending)", unknownRecoveryRunUser, lookupErr.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict %q does not name %q", err, want)
		}
	}

	if c := e.rt.byName(string(run.ID)); c != nil {
		if err := e.rt.Destroy(ctx, c.id); err != nil {
			t.Fatalf("remove container: %v", err)
		}
	}
	flaky.clearFindErr()
	if err := recovered.reserveRunUser(ctx, next, "1000:1000", true); err != nil {
		t.Fatalf("launch after the runtime confirmed the container gone: %v", err)
	}
}

func TestCloseWithoutCheckoutThenLaunch(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = -1 })
	ctx := t.Context()
	run, _ := e.launchFake(t, "checkout removed before close")
	e.sched.mu.Lock()
	e.sched.runs[run.ID].runUser = "1000:1000"
	e.sched.syncRunUserReservationsLocked()
	e.sched.mu.Unlock()

	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.refusal = checkoutMissing(run.ID)
	e.sched.UseEvidence(capture)
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if e.sched.RetainsContainer(ctx, run.ID) {
		t.Fatal("close left the container retained")
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if !strings.Contains(row.Reason, evidenceUnavailableReason) {
		t.Fatalf("reason = %q, want %q", row.Reason, evidenceUnavailableReason)
	}
	next := &supervised{runID: "run-next", memberID: e.member.ID}
	if err := e.sched.reserveRunUser(ctx, next, "2000:2000", true); err != nil {
		t.Fatalf("reservation after close: %v", err)
	}
	e.launchFake(t, "launch after close")
}

// A retry whose key misses a packet an earlier close already stored still
// finishes cleanup, without claiming the evidence is unavailable.
func TestMissingCheckoutKeepsEarlierEvidence(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run := syntheticDestroyPendingRun(t, e, "evidence captured before the checkout went")
	if err := e.db.CreateEvidencePacket(ctx, &store.EvidencePacket{
		WorkspaceID: e.ws.ID, RunID: run.ID,
		Origin:  store.EvidenceOrigin{Kind: store.EvidenceOriginServer},
		Trigger: store.EvidenceFinish, IdempotencyKey: "finish:" + string(run.ID) + ":launch:1:abc:merged",
		CapturedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateEvidencePacket: %v", err)
	}

	recovered := e.newScheduler(t, e.rt, newFakePTY())
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.refusal = checkoutMissing(run.ID)
	recovered.UseEvidence(capture)
	if err := recovered.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	if _, err := os.Stat(recovered.sidecarPath(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("sidecar after cleanup: %v", err)
	}
	if row := e.waitStoreStatus(t, run.ID, domain.RunMerged); strings.Contains(row.Reason, evidenceUnavailableReason) {
		t.Fatalf("reason = %q, want no unavailable note beside a stored packet", row.Reason)
	}
}

func TestCheckoutGCSettlesMissingCheckout(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = -time.Second
		cfg.CheckoutTTL = time.Hour
	})
	ctx := t.Context()
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	run, _ := e.launchFake(t, "checkout gone before gc")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	row, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	row.FinishedAt = &old
	if err = e.db.UpdateRun(ctx, row); err != nil {
		t.Fatal(err)
	}
	capture.mu.Lock()
	capture.refusal = checkoutMissing(run.ID)
	capture.mu.Unlock()

	e.sched.sweepCheckouts(ctx, time.Now().UTC().Add(-e.sched.cfg.CheckoutTTL))
	fresh, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Worktree != "" || !strings.Contains(fresh.Reason, evidenceUnavailableReason) {
		t.Fatalf("after gc: worktree %q, reason %q; want cleared with %q", fresh.Worktree, fresh.Reason, evidenceUnavailableReason)
	}
}

func TestEvidenceNoteKeepsTheReasonsCause(t *testing.T) {
	t.Parallel()
	cause := "exec: \"claude\": executable file not found"
	got := publicRunStatusReason(withCleanupNote("provisioning: "+strings.Repeat("step ", 60)+cause, evidenceUnavailableReason))
	if !strings.HasSuffix(got, "; "+evidenceUnavailableReason) || !strings.Contains(got, cause) ||
		len([]rune(got)) > maxPublicRunStatusReason {
		t.Fatalf("public reason = %q, want the cause and the note within %d runes", got, maxPublicRunStatusReason)
	}
}

// A pending cleanup recovery could not identify must not stop a live
// session getting its home back once the runtime confirms the container gone.
func TestUnknownReservationResolvesAtHomeRestore(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	ctx := t.Context()
	run := syntheticDestroyPendingRun(t, e, "runtime down at boot")

	lookupErr := errors.New("runtime API unavailable")
	flaky := &creationKeyFailureRuntime{Runtime: e.rt, findErr: lookupErr}
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	recovered.cfg.Runtime = flaky
	if err := recovered.recoverRuns(ctx); err != nil {
		t.Fatalf("recoverRuns: %v", err)
	}
	if c := e.rt.byName(string(run.ID)); c != nil {
		if err := e.rt.Destroy(ctx, c.id); err != nil {
			t.Fatalf("remove container: %v", err)
		}
	}

	live := &supervised{runID: "run-live", memberID: e.member.ID, runUser: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	recovered.mu.Lock()
	recovered.runs[live.runID] = live
	recovered.mu.Unlock()
	home, err := recovered.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Lchown(home, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if was, restoreErr := recovered.restoreHomeRoot(ctx, live); was != "" || restoreErr != nil {
		t.Fatalf("restore of an intact home = %q, %v; want nothing to do", was, restoreErr)
	}
	if err = os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })

	if _, err = recovered.restoreHomeRoot(ctx, live); err == nil || !strings.Contains(err.Error(), lookupErr.Error()) {
		t.Fatalf("restore while the pending container's identity is unknown: %v, want the lookup error", err)
	}
	flaky.clearFindErr()
	was, err := recovered.restoreHomeRoot(ctx, live)
	if err != nil || was == "" {
		t.Fatalf("restore after the runtime confirmed the container gone = %q, %v; want the replaced mode", was, err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("home mode = %04o, want 0700", got)
	}
}
