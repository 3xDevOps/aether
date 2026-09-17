package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
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
