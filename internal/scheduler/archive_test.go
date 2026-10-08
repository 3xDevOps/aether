package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
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
	if payload.ArchivedAt == nil {
		t.Fatalf("archived event payload = %+v, want archived_at set", payload)
	}
	archivedAt, aerr := time.Parse(time.RFC3339, *payload.ArchivedAt)
	if aerr != nil {
		t.Fatalf("parse archived_at: %v", aerr)
	}
	if !archivedAt.Equal(first.ArchivedAt.Truncate(time.Second)) {
		t.Fatalf("event archived_at = %v, want persisted timestamp %v", archivedAt, first.ArchivedAt)
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
	if archivedPayload.ArchivedAt == nil {
		t.Fatalf("archived event payload = %+v, want archived_at set", archivedPayload)
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
	if restoredPayload.ArchivedAt != nil {
		t.Fatalf("restore event payload = %+v, want archived_at nil", restoredPayload)
	}
	waitTimelineEvent(t, sub, r.ID, events.TimelineNote)

	unarchived, err := e.sched.SetArchived(ctx, r.ID, e.member.ID, false)
	if err != nil {
		t.Fatalf("restore of an unarchived run: %v", err)
	}
	if unarchived.ArchivedAt != nil {
		t.Fatal("restoring an unarchived run set ArchivedAt")
	}
}

func TestSetMissionArchivedPublishesEachRunItMoves(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	sub := e.subscribe(t)
	m := &domain.Mission{
		WorkspaceID: e.ws.ID, Objective: "archive swarm", AccountableHumanID: e.member.ID,
		Integrator:     domain.MissionIntegrator{AccountMemberID: e.member.ID, Harness: "fake", Mode: domain.LaunchTUI},
		IdempotencyKey: "archive-swarm",
	}
	if err := e.db.CreateMission(ctx, m); err != nil {
		t.Fatalf("create mission: %v", err)
	}
	if _, err := e.db.CancelMission(ctx, m.ID, e.member.ID, "archive-swarm-cancel"); err != nil {
		t.Fatalf("cancel mission: %v", err)
	}
	r := createRun(t, e, domain.RunAbandoned)
	now := time.Now().UTC()
	for _, at := range []*time.Time{&now, nil} {
		changed, err := e.sched.SetMissionArchived(ctx, m.ID, []domain.RunID{r.ID}, e.member.ID, at)
		if err != nil || !changed {
			t.Fatalf("SetMissionArchived(%v) = %v, %v; want the swarm changed", at, changed, err)
		}
		if payload := waitArchivedEvent(t, sub, r.ID); (payload.ArchivedAt != nil) != (at != nil) {
			t.Fatalf("run.archived payload after SetMissionArchived(%v) = %+v", at, payload)
		}
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
	if restoredPayload.ArchivedAt != nil {
		t.Fatalf("relaunch restore event payload = %+v, want archived_at nil", restoredPayload)
	}
	waitTimelineEvent(t, sub, run.ID, events.TimelineNote)
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

func TestRelaunchKeepsArchiveTimestampWhenPromotionFails(t *testing.T) {
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
// returns it, so callers can inspect the actor who deleted the run.
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

func TestDeleteRemovesAnOldArchivedRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	sub := e.subscribe(t)
	r := createRun(t, e, domain.RunMerged)
	backdateArchived(t, e, r, 365*24*time.Hour)

	if err := e.sched.DeleteRun(ctx, r.ID, e.member.ID); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}

	ev := waitDeletedEvent(t, sub, r.ID)
	if ev.ActorID != e.member.ID {
		t.Fatalf("run.deleted actor = %q, want %q", ev.ActorID, e.member.ID)
	}
	if _, err := e.db.GetRun(ctx, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after explicit delete: err = %v, want ErrNotFound", err)
	}
}

// Expiring a retained container releases compute but preserves archived runs,
// even when they were archived long ago.
func TestArchivedRunSurvivesRetainedContainerExpiry(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.RunContainerTTL = time.Hour
	})
	ctx := t.Context()
	run, _ := e.launchFake(t, "retain archived history")
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
	backdateArchived(t, e, closed, 365*24*time.Hour)

	kept, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after archive: %v", err)
	}
	if kept.Reason != retainedCloseReason {
		t.Fatalf("run reason after archive = %q, want %q", kept.Reason, retainedCloseReason)
	}

	// Expire the retained container the way the container TTL sweep would,
	// exactly as TestRetainedExpiryDestroysContainerAndHidesRelaunch does.
	e.sched.mu.Lock()
	entry := e.sched.runs[run.ID]
	past := time.Now().UTC().Add(-time.Second)
	entry.retainedUntil = &past
	if sidecarErr := e.sched.writeSidecar(entry.sidecar()); sidecarErr != nil {
		e.sched.mu.Unlock()
		t.Fatalf("write expired sidecar: %v", sidecarErr)
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

	fresh, err := e.db.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after container expiry: %v", err)
	}
	if fresh.ArchivedAt == nil || kept.ArchivedAt == nil || !fresh.ArchivedAt.Equal(*kept.ArchivedAt) {
		t.Fatalf("container expiry changed archive timestamp: %v -> %v", kept.ArchivedAt, fresh.ArchivedAt)
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

type failingDeleteRunStore struct {
	store.Store
}

func (s *failingDeleteRunStore) DeleteRun(context.Context, domain.RunID) error {
	return errors.New("test: delete failed")
}

// A failed explicit deletion must not announce that the run was deleted.
func TestDeleteArchivedRunKeepsRecordOnDeleteFailure(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := t.Context()
	sub := e.subscribe(t)
	r := createRun(t, e, domain.RunMerged)
	backdateArchived(t, e, r, 365*24*time.Hour)
	e.sched.cfg.Store = &failingDeleteRunStore{Store: e.db}

	if err := e.sched.DeleteRun(ctx, r.ID, e.member.ID); err == nil {
		t.Fatal("DeleteRun succeeded despite the injected store failure")
	}
	if n := countDeletedEvents(sub); n != 0 {
		t.Fatalf("published %d run.deleted events despite failed delete", n)
	}
	if _, err := e.db.GetRun(ctx, r.ID); err != nil {
		t.Fatalf("GetRun after failed delete: %v", err)
	}
}
