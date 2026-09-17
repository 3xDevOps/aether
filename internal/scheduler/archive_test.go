package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/timeline"
)

// createRun creates a run owned by e.member directly in status, bypassing
// launch so archive tests can exercise every Final status without a real
// container.
func createRun(t *testing.T, e *testEnv, status domain.RunStatus) *domain.Run {
	t.Helper()
	r := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "archive me",
		Harness: "claude", Mode: domain.LaunchTUI, Status: status,
		Branch: "aether/run-archive-me",
	}
	if err := e.db.CreateRun(t.Context(), r); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return r
}

// SetArchived is refused on a run that has not reached a final
// disposition, naming the run's real status in the error, and leaves the
// column untouched.
func TestSetArchivedRefusedOnNonFinal(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()

	for _, status := range []domain.RunStatus{domain.RunRunning, domain.RunCompleted} {
		r := createRun(t, e, status)
		_, err := e.sched.SetArchived(ctx, r.ID, e.member.ID, true)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("SetArchived on %s run: err = %v, want ErrInvalidTransition", status, err)
		}
		if !strings.Contains(err.Error(), string(status)) {
			t.Fatalf("SetArchived on %s run error = %q, want it to name the real status", status, err)
		}
		fresh, gerr := e.db.GetRun(ctx, r.ID)
		if gerr != nil {
			t.Fatalf("GetRun: %v", gerr)
		}
		if fresh.ArchivedAt != nil {
			t.Fatalf("refused archive set archived_at anyway: %v", fresh.ArchivedAt)
		}
	}
}

// waitArchivedEvent reads sub until a run.archived event for run arrives
// and returns its typed payload.
func waitArchivedEvent(t *testing.T, sub events.Subscription, run domain.RunID) events.RunArchivedPayload {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("event stream closed while waiting for run.archived on run %s", run)
			}
			if p, isArchived := ev.Payload.(events.RunArchivedPayload); isArchived && ev.RunID == run {
				return p
			}
		case <-deadline:
			t.Fatalf("timed out waiting for run.archived on run %s", run)
		}
	}
}

// Archiving an already-archived run is a no-op that keeps the original
// timestamp and publishes no duplicate event.
func TestSetArchivedIdempotent(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	sub := e.subscribe(t)
	r := createRun(t, e, domain.RunMerged)

	first, err := e.sched.SetArchived(ctx, r.ID, e.member.ID, true)
	if err != nil {
		t.Fatalf("SetArchived: %v", err)
	}
	if first.ArchivedAt == nil {
		t.Fatal("archived run has no ArchivedAt")
	}
	payload := waitArchivedEvent(t, sub, r.ID)
	if payload.ArchivedAt == nil || payload.DeletesAt == nil {
		t.Fatalf("archived event payload = %+v, want archived_at and deletes_at set", payload)
	}
	archivedAt, aerr := time.Parse(time.RFC3339, *payload.ArchivedAt)
	if aerr != nil {
		t.Fatalf("parse archived_at: %v", aerr)
	}
	deletesAt, derr := time.Parse(time.RFC3339, *payload.DeletesAt)
	if derr != nil {
		t.Fatalf("parse deletes_at: %v", derr)
	}
	if !deletesAt.Equal(archivedAt.Add(domain.ArchiveRetention)) {
		t.Fatalf("deletes_at = %v, want archived_at + ArchiveRetention = %v", deletesAt, archivedAt.Add(domain.ArchiveRetention))
	}
	waitTimelineEvent(t, sub, r.ID, events.TimelineNote)

	second, err := e.sched.SetArchived(ctx, r.ID, e.member.ID, true)
	if err != nil {
		t.Fatalf("re-archive: %v", err)
	}
	if second.ArchivedAt == nil || !second.ArchivedAt.Equal(*first.ArchivedAt) {
		t.Fatalf("re-archive moved ArchivedAt: %v -> %v", first.ArchivedAt, second.ArchivedAt)
	}

	select {
	case ev := <-sub.Events():
		t.Fatalf("re-archiving an archived run published an event: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// Restore clears the column and publishes the restore event; restoring
// an unarchived run is a no-op.
func TestSetArchivedRestore(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	sub := e.subscribe(t)
	r := createRun(t, e, domain.RunFailed)

	if _, err := e.sched.SetArchived(ctx, r.ID, e.member.ID, true); err != nil {
		t.Fatalf("SetArchived: %v", err)
	}
	archivedPayload := waitArchivedEvent(t, sub, r.ID)
	if archivedPayload.ArchivedAt == nil || archivedPayload.DeletesAt == nil {
		t.Fatalf("archived event payload = %+v, want archived_at and deletes_at set", archivedPayload)
	}
	waitTimelineEvent(t, sub, r.ID, events.TimelineNote)

	restored, err := e.sched.SetArchived(ctx, r.ID, e.member.ID, false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.ArchivedAt != nil {
		t.Fatalf("restored run ArchivedAt = %v, want nil", restored.ArchivedAt)
	}
	restoredPayload := waitArchivedEvent(t, sub, r.ID)
	if restoredPayload.ArchivedAt != nil || restoredPayload.DeletesAt != nil {
		t.Fatalf("restore event payload = %+v, want both nil", restoredPayload)
	}
	ev := waitTimelineEvent(t, sub, r.ID, events.TimelineNote)
	if p := ev.Payload.(events.TimelinePayload); !strings.Contains(p.Message, "restored") {
		t.Fatalf("restore timeline message = %q, want it to say restored", p.Message)
	}

	unarchived, err := e.sched.SetArchived(ctx, r.ID, e.member.ID, false)
	if err != nil {
		t.Fatalf("restore of an unarchived run: %v", err)
	}
	if unarchived.ArchivedAt != nil {
		t.Fatal("restoring an unarchived run set ArchivedAt")
	}
}

// Relaunching an archived retained TUI run restores it, publishing the
// restore under the same archiveMu that guards SetArchived.
func TestRelaunchRestoresArchivedRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	sub := e.subscribe(t)
	run, _ := e.launchFake(t, "retain and archive")

	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunMerged)

	if _, err := e.sched.SetArchived(ctx, run.ID, e.member.ID, true); err != nil {
		t.Fatalf("SetArchived: %v", err)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineNote)
	archived, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatal("archived run has no ArchivedAt")
	}

	reopened, err := e.sched.Relaunch(ctx, run.ID, e.member.ID)
	if err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if reopened.Status != domain.RunRunning {
		t.Fatalf("reopened status = %s, want running", reopened.Status)
	}
	if reopened.ArchivedAt != nil {
		t.Fatalf("relaunched run returned ArchivedAt = %v, want nil", reopened.ArchivedAt)
	}
	restoredPayload := waitArchivedEvent(t, sub, run.ID)
	if restoredPayload.ArchivedAt != nil || restoredPayload.DeletesAt != nil {
		t.Fatalf("relaunch restore event payload = %+v, want both nil", restoredPayload)
	}
	ev := waitTimelineEvent(t, sub, run.ID, events.TimelineNote)
	if p := ev.Payload.(events.TimelinePayload); !strings.Contains(p.Message, "restored") {
		t.Fatalf("relaunch restore timeline message = %q, want it to say restored", p.Message)
	}
	fresh, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if fresh.ArchivedAt != nil {
		t.Fatalf("relaunched run still archived: %v", fresh.ArchivedAt)
	}
}

type failingUpdateRunStore struct {
	store.Store
}

func (s *failingUpdateRunStore) UpdateRun(context.Context, *domain.Run) error {
	return errors.New("test: promotion failed")
}

func TestRelaunchKeepsArchiveTimerWhenPromotionFails(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "archive then fail relaunch")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunMerged)
	archived, err := e.sched.SetArchived(ctx, run.ID, e.member.ID, true)
	if err != nil {
		t.Fatalf("SetArchived: %v", err)
	}

	e.sched.cfg.Store = &failingUpdateRunStore{Store: e.db}
	if _, relaunchErr := e.sched.Relaunch(ctx, run.ID, e.member.ID); relaunchErr == nil {
		t.Fatal("Relaunch succeeded despite promotion failure")
	}

	fresh, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if fresh.ArchivedAt == nil || !fresh.ArchivedAt.Equal(*archived.ArchivedAt) {
		t.Fatalf("ArchivedAt after failed relaunch = %v, want %v", fresh.ArchivedAt, archived.ArchivedAt)
	}
}

// waitDeletedEvent reads sub until a run.deleted event for run arrives and
// returns it, so callers can inspect the actor the sweep published under.
func waitDeletedEvent(t *testing.T, sub events.Subscription, run domain.RunID) events.Event {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("event stream closed while waiting for run.deleted on run %s", run)
			}
			if _, isDeleted := ev.Payload.(events.RunDeletedPayload); isDeleted && ev.RunID == run {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for run.deleted on run %s", run)
		}
	}
}

// backdateArchived archives r directly through the store, bypassing
// SetArchived's time.Now() so the test controls how old the archive is,
// exactly as the checkout GC tests backdate FinishedAt.
func backdateArchived(t *testing.T, e *testEnv, r *domain.Run, age time.Duration) {
	t.Helper()
	at := time.Now().UTC().Add(-age)
	if _, err := e.db.SetRunArchived(t.Context(), r.ID, &at); err != nil {
		t.Fatalf("SetRunArchived: %v", err)
	}
}

// A run archived more than the retention period ago is deleted, and the
// resulting run.deleted event carries the empty system actor.
func TestSweepArchivedDeletesPastRetention(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	sub := e.subscribe(t)
	r := createRun(t, e, domain.RunMerged)
	backdateArchived(t, e, r, domain.ArchiveRetention+24*time.Hour)

	e.sched.sweepArchived(ctx)

	ev := waitDeletedEvent(t, sub, r.ID)
	if ev.ActorID != "" {
		t.Fatalf("run.deleted actor = %q, want empty (system)", ev.ActorID)
	}
	if _, err := e.db.GetRun(ctx, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after sweep: err = %v, want ErrNotFound", err)
	}
}

// A run archived less than the retention period ago survives the sweep.
func TestSweepArchivedKeepsRunUnderRetention(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	r := createRun(t, e, domain.RunMerged)
	backdateArchived(t, e, r, domain.ArchiveRetention-24*time.Hour)

	e.sched.sweepArchived(ctx)

	if _, err := e.db.GetRun(ctx, r.ID); err != nil {
		t.Fatalf("GetRun after sweep: %v", err)
	}
}

// A run whose retained container is still held (Reason ==
// retainedCloseReason) is kept past its retention period instead of being
// routed through DeleteRun, which would deadlock: Relaunch locks
// lifecycleMu before archiveMu to restore a retained run, and DeleteRun
// would need that same lifecycleMu while archiveMu is already held by the
// sweep. Once the container TTL sweep expires it, the reason changes and
// a later archive sweep deletes it.
func TestSweepArchivedSkipsRetainedRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "retain past retention")
	if err := e.sched.CloseRun(ctx, run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	closed := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if closed.Reason != retainedCloseReason {
		t.Fatalf("close reason = %q, want %q", closed.Reason, retainedCloseReason)
	}
	// backdateArchived both archives and backdates in one store call;
	// SetArchived's own archived_at write only takes effect when the
	// column is still NULL, so archiving first would make the backdate a
	// silent no-op.
	backdateArchived(t, e, closed, domain.ArchiveRetention+24*time.Hour)

	e.sched.sweepArchived(ctx)

	kept, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after sweep: %v", err)
	}
	if kept.Reason != retainedCloseReason {
		t.Fatalf("run reason after sweep = %q, want %q (kept, not swept)", kept.Reason, retainedCloseReason)
	}

	// Expire the retained container the way the container TTL sweep would,
	// exactly as TestRetainedExpiryDestroysContainerAndHidesRelaunch does.
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	past := time.Now().UTC().Add(-time.Second)
	entry.retainedUntil = &past
	if err := e.sched.writeSidecar(entry.sidecar()); err != nil {
		e.sched.mu.Unlock()
		t.Fatalf("write expired sidecar: %v", err)
	}
	e.sched.mu.Unlock()
	e.sched.sweepRetained(ctx)
	waitFor(t, "expired container destroyed", func() bool {
		return e.rt.byName(string(run.ID)) == nil
	})
	expired := e.waitStoreStatus(t, run.ID, domain.RunMerged)
	if expired.Reason != retainedExpiredReason {
		t.Fatalf("expiry reason = %q, want %q", expired.Reason, retainedExpiredReason)
	}

	e.sched.sweepArchived(ctx)

	if _, err := e.db.GetRun(ctx, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after later sweep: err = %v, want ErrNotFound", err)
	}
}

// A restore that lands before the sweep picks up the run always wins.
// sweepArchived's list query already excludes a restored run (its
// archived_at is cleared), so sweepArchivedRun is called directly to
// exercise the re-read guard itself.
//
// A non-Final status alongside a set archived_at is not reachable through
// the store to test the same way: SetRunArchived only ever writes
// archived_at on a Final row, and UpdateRun does not write that column at
// all.
func TestSweepArchivedKeepsRestoredRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	r := createRun(t, e, domain.RunMerged)
	backdateArchived(t, e, r, domain.ArchiveRetention+24*time.Hour)
	cutoff := time.Now().UTC().Add(-domain.ArchiveRetention)

	if _, err := e.sched.SetArchived(ctx, r.ID, e.member.ID, false); err != nil {
		t.Fatalf("restore: %v", err)
	}

	e.sched.sweepArchivedRun(ctx, r.ID, cutoff)

	fresh, err := e.db.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun after sweep: %v", err)
	}
	if fresh.ArchivedAt != nil {
		t.Fatal("restored run has ArchivedAt set after sweep")
	}
}

// A run archived after the cutoff is not yet due; sweepArchivedRun is
// called directly (bypassing the list query) so its own re-read guard is
// what keeps it, not the caller's filtering.
func TestSweepArchivedRunKeepsRunArchivedAfterCutoff(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	r := createRun(t, e, domain.RunMerged)
	backdateArchived(t, e, r, domain.ArchiveRetention-24*time.Hour)
	cutoff := time.Now().UTC().Add(-domain.ArchiveRetention)

	e.sched.sweepArchivedRun(ctx, r.ID, cutoff)

	if _, err := e.db.GetRun(ctx, r.ID); err != nil {
		t.Fatalf("GetRun after sweep: %v", err)
	}
}

// countDeletedEvents drains what the subscription has already queued and
// counts run.deleted events among it, mirroring countTimelineEvents.
func countDeletedEvents(sub events.Subscription) int {
	n := 0
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				return n
			}
			if _, isDeleted := ev.Payload.(events.RunDeletedPayload); isDeleted {
				n++
			}
		default:
			return n
		}
	}
}

// A PublishRunBranch failure blocks deletion: the sweep never destroys the
// only copy of unpublished work unattended, publishes neither the
// deletion timeline note nor run.deleted, and retries on the next tick.
func TestSweepArchivedBlockedByPublishFailure(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	sub := e.subscribe(t)
	r := createRun(t, e, domain.RunMerged)
	path, branch, err := e.git.CreateRunCheckout(ctx, e.ws.ID, r.ID, "main", r.Task, "")
	if err != nil {
		t.Fatalf("CreateRunCheckout: %v", err)
	}
	r.Worktree, r.Branch = path, branch
	if updErr := e.db.UpdateRun(ctx, r); updErr != nil {
		t.Fatalf("UpdateRun: %v", updErr)
	}
	backdateArchived(t, e, r, domain.ArchiveRetention+24*time.Hour)
	e.git.publishErr = errors.New("publish unavailable")

	e.sched.sweepArchived(ctx)

	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("checkout path after blocked publish: %v", statErr)
	}
	if n := countTimelineEvents(sub, events.TimelineNote); n != 0 {
		t.Fatalf("published %d timeline notes despite blocked publish", n)
	}
	if n := countDeletedEvents(sub); n != 0 {
		t.Fatalf("published %d run.deleted events despite blocked publish", n)
	}
}

type failingDeleteRunStore struct {
	store.Store
}

func (s *failingDeleteRunStore) DeleteRun(context.Context, domain.RunID) error {
	return errors.New("test: delete failed")
}

// A DeleteRun failure must not leave a false "run was deleted" timeline
// note: the note is published only after DeleteRun returns nil, so a
// failed delete leaves no note for the next hourly retry to duplicate,
// and the run survives.
func TestSweepArchivedNoTimelineNoteOnDeleteFailure(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	sub := e.subscribe(t)
	r := createRun(t, e, domain.RunMerged)
	backdateArchived(t, e, r, domain.ArchiveRetention+24*time.Hour)
	e.sched.cfg.Store = &failingDeleteRunStore{Store: e.db}

	e.sched.sweepArchived(ctx)

	if n := countTimelineEvents(sub, events.TimelineNote); n != 0 {
		t.Fatalf("published %d timeline notes despite failed delete", n)
	}
	if n := countDeletedEvents(sub); n != 0 {
		t.Fatalf("published %d run.deleted events despite failed delete", n)
	}
	if _, err := e.db.GetRun(ctx, r.ID); err != nil {
		t.Fatalf("GetRun after failed sweep: %v", err)
	}
}

// The sweep's deletion is followed by a system timeline note explaining
// why the run disappeared - published only once DeleteRun has actually
// succeeded, so it arrives on the wire after run.deleted - and that note
// stays readable from the persisted event log after the run row itself is
// gone: the timeline does not depend on the row, only run.deleted does.
func TestSweepArchivedPublishesTimelineNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log, err := events.OpenSQLiteLog(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(context.Background(), log)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	e := newTestEnv(t, func(cfg *Config) { cfg.Bus = bus })
	e.bus = bus
	ctx := t.Context()
	sub := e.subscribe(t)
	r := createRun(t, e, domain.RunAbandoned)
	backdateArchived(t, e, r, domain.ArchiveRetention+24*time.Hour)

	e.sched.sweepArchived(ctx)

	waitDeletedEvent(t, sub, r.ID)
	ev := waitTimelineEvent(t, sub, r.ID, events.TimelineNote)
	if ev.ActorID != "" {
		t.Fatalf("timeline note actor = %q, want empty (system)", ev.ActorID)
	}
	p := ev.Payload.(events.TimelinePayload)
	if !strings.Contains(p.Message, "deleted") {
		t.Fatalf("timeline note message = %q, want it to say the run was deleted", p.Message)
	}

	reader := timeline.NewReader(log)
	page, err := reader.Page(ctx, timeline.Filter{
		Workspace: e.ws.ID, Run: r.ID, Types: []events.Type{events.TypeTimeline},
	}, 0, 0)
	if err != nil {
		t.Fatalf("timeline page: %v", err)
	}
	found := false
	for _, pe := range page.Events {
		if tp, ok := pe.Payload.(events.TimelinePayload); ok && strings.Contains(tp.Message, "deleted") {
			found = true
		}
	}
	if !found {
		t.Fatal("deleted-run timeline note not found in persisted event log")
	}
}

// Start's hourly GC tick runs sweepArchived at boot even when CheckoutTTL
// is disabled; only sweepCheckouts is gated on it.
func TestSweepArchivedRunsAtBootWithCheckoutTTLDisabled(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.CheckoutTTL = -1
	})
	ctx, cancel := context.WithCancel(t.Context())
	r := createRun(t, e, domain.RunFailed)
	backdateArchived(t, e, r, domain.ArchiveRetention+24*time.Hour)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := e.sched.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "boot sweepArchived to delete the run", func() bool {
		_, err := e.db.GetRun(ctx, r.ID)
		return errors.Is(err, store.ErrNotFound)
	})
}
