package mission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type storeRetirer struct {
	db       *store.DB
	closed   map[domain.RunID]domain.RunStatus
	tornDown []domain.RunID
	calls    int
	failOn   int
}

var errInjected = errors.New("injected failure")

func (r *storeRetirer) fail() error {
	r.calls++
	if r.calls == r.failOn {
		return errInjected
	}
	return nil
}

func (r *storeRetirer) CloseRun(ctx context.Context, run domain.RunID, _ domain.MemberID, outcome domain.RunStatus) error {
	if err := r.fail(); err != nil {
		return err
	}
	r.closed[run] = outcome
	return r.db.UpdateRunStatus(ctx, run, outcome, "closed", nil, nil)
}

func (r *storeRetirer) SetMissionArchived(ctx context.Context, mission domain.MissionID, runs []domain.RunID, _ domain.MemberID, at *time.Time) (bool, error) {
	changed, _, err := r.db.SetMissionArchived(ctx, mission, runs, at)
	return changed, err
}

func (r *storeRetirer) TeardownRun(_ context.Context, run domain.RunID, _ domain.MemberID) error {
	if err := r.fail(); err != nil {
		return err
	}
	r.tornDown = append(r.tornDown, run)
	return nil
}

func (r *storeRetirer) DeleteMission(ctx context.Context, mission *domain.Mission, runs []domain.RunID, _ domain.MemberID) error {
	return r.db.DeleteMission(ctx, mission.ID, runs)
}

type archiveFixture struct {
	db      *store.DB
	svc     *Service
	mission *domain.Mission
	human   domain.MemberID
	worker  domain.RunID
	retire  *storeRetirer
	bus     *recordingBus
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()
	ctx := context.Background()
	db, mission, _, submission, _, _ := setupSubmissionRegression(t)
	if _, err := db.AcceptSubmission(ctx, submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", "archive-accept", &store.SubmissionEvidenceValidation{Ref: submission.Ref, Evidence: submission.Evidence}); err != nil {
		t.Fatalf("accept submission: %v", err)
	}
	retire := &storeRetirer{db: db, closed: map[domain.RunID]domain.RunStatus{}}
	bus := &recordingBus{}
	svc, err := New(Config{Store: db, Missions: db, Retire: retire, Bus: bus, AuthorizationMu: &sync.Mutex{}})
	if err != nil {
		t.Fatalf("new mission service: %v", err)
	}
	return &archiveFixture{db: db, svc: svc, mission: mission, human: mission.AccountableHumanID, worker: submission.Ref.RunID, retire: retire, bus: bus}
}

func (f *archiveFixture) runsAre(t *testing.T, status domain.RunStatus) {
	t.Helper()
	for _, run := range []domain.RunID{f.mission.CurrentIntegratorRunID, f.worker} {
		if err := f.db.UpdateRunStatus(context.Background(), run, status, "", nil, nil); err != nil {
			t.Fatalf("set run %s %s: %v", run, status, err)
		}
	}
}

func (f *archiveFixture) complete(t *testing.T) {
	t.Helper()
	if _, err := f.db.CompleteMission(context.Background(), f.mission.ID, f.mission.CurrentIntegratorRunID); err != nil {
		t.Fatalf("complete mission: %v", err)
	}
	f.runsAre(t, domain.RunCompleted)
}

func (f *archiveFixture) params() protocol.MissionIDParams {
	return protocol.MissionIDParams{MissionID: string(f.mission.ID)}
}

func (f *archiveFixture) missionEvents() []events.MissionChangedPayload {
	var out []events.MissionChangedPayload
	for _, event := range f.bus.events {
		if payload, ok := event.Payload.(events.MissionChangedPayload); ok {
			out = append(out, payload)
		}
	}
	return out
}

func TestArchiveClosesAndArchivesEveryRunOfACompletedSwarm(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	f.complete(t)
	out, err := f.svc.Archive(ctx, f.human, f.params())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if out.Mission.ArchivedAt == nil {
		t.Fatalf("archive result = %+v, want archived_at", out.Mission)
	}
	for _, run := range []domain.RunID{f.mission.CurrentIntegratorRunID, f.worker} {
		if f.retire.closed[run] != domain.RunMerged {
			t.Fatalf("run %s closed as %q, want merged: the swarm delivered its work", run, f.retire.closed[run])
		}
		current, getErr := f.db.GetRun(ctx, run)
		if getErr != nil || current.ArchivedAt == nil || current.Status != domain.RunMerged {
			t.Fatalf("run %s after archive = %+v (err %v), want merged and archived", run, current, getErr)
		}
	}
	if got := f.missionEvents(); len(got) != 1 || got[0].MissionID != f.mission.ID {
		t.Fatalf("mission.changed events = %+v, want one for the swarm", got)
	}
	if _, againErr := f.svc.Archive(ctx, f.human, f.params()); againErr != nil {
		t.Fatalf("archive again: %v", againErr)
	}
	if got := f.missionEvents(); len(got) != 1 {
		t.Fatalf("a repeated archive published %d events, want none new", len(got)-1)
	}

	restored, err := f.svc.Unarchive(ctx, f.human, f.params())
	if err != nil || restored.Mission.ArchivedAt != nil {
		t.Fatalf("unarchive = %+v, %v; want no archived_at", restored.Mission, err)
	}
	for _, run := range []domain.RunID{f.mission.CurrentIntegratorRunID, f.worker} {
		current, getErr := f.db.GetRun(ctx, run)
		if getErr != nil || current.ArchivedAt != nil || current.Status != domain.RunMerged {
			t.Fatalf("run %s after unarchive = %+v (err %v), want restored and still merged", run, current, getErr)
		}
	}
}

func TestArchiveClosesACancelledSwarmsRunsWithoutMerging(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	if _, err := f.db.CancelMission(ctx, f.mission.ID, f.human, "archive-cancel"); err != nil {
		t.Fatalf("cancel mission: %v", err)
	}
	f.runsAre(t, domain.RunCompleted)
	if _, err := f.svc.Archive(ctx, f.human, f.params()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if f.retire.closed[f.worker] != domain.RunAbandoned || f.retire.closed[f.mission.CurrentIntegratorRunID] != domain.RunAbandoned {
		t.Fatalf("closed = %v, want both abandoned", f.retire.closed)
	}
}

func TestArchiveRefusesASwarmThatIsStillRunning(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	f.runsAre(t, domain.RunRunning)
	if _, err := f.svc.Archive(ctx, f.human, f.params()); !errors.Is(err, store.ErrMissionPhase) || !strings.Contains(err.Error(), "cancel it first") {
		t.Fatalf("archive an active swarm = %v, want ErrMissionPhase saying cancel it first", err)
	}
	if _, err := f.db.CompleteMission(ctx, f.mission.ID, f.mission.CurrentIntegratorRunID); err != nil {
		t.Fatalf("complete mission: %v", err)
	}
	_, err := f.svc.Archive(ctx, f.human, f.params())
	if !errors.Is(err, store.ErrMissionPhase) || !strings.Contains(err.Error(), string(f.worker)+" is running") {
		t.Fatalf("archive with a live run = %v, want ErrMissionPhase naming the running run", err)
	}
	if len(f.retire.closed) != 0 || f.reload(t).ArchivedAt != nil {
		t.Fatalf("a refused archive closed %v or archived the swarm", f.retire.closed)
	}
}

func TestArchiveAndDeleteAreForTheAccountableHumanOrAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	f.complete(t)
	other := regressionMember(t, f.db, "bystander")
	if _, err := f.svc.Archive(ctx, other.ID, f.params()); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("foreign archive = %v, want ErrDenied", err)
	}
	if err := f.svc.Delete(ctx, other.ID, f.params()); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("foreign delete = %v, want ErrDenied", err)
	}
	other.Role = domain.RoleAdmin
	if err := f.db.UpdateMember(ctx, other); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := f.svc.Archive(ctx, other.ID, f.params()); err != nil {
		t.Fatalf("admin archive: %v", err)
	}
}

func TestDeleteRemovesAnArchivedSwarmAndItsRuns(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	f.complete(t)
	if err := f.svc.Delete(ctx, f.human, f.params()); !errors.Is(err, store.ErrMissionPhase) || !strings.Contains(err.Error(), "archive it first") {
		t.Fatalf("delete a completed swarm = %v, want ErrMissionPhase saying archive it first", err)
	}
	if _, err := f.svc.Archive(ctx, f.human, f.params()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := f.svc.Delete(ctx, f.human, f.params()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.db.GetMission(ctx, f.mission.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mission after delete = %v, want ErrNotFound", err)
	}
	for _, run := range []domain.RunID{f.mission.CurrentIntegratorRunID, f.worker} {
		if _, err := f.db.GetRun(ctx, run); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("run %s after delete = %v, want ErrNotFound", run, err)
		}
	}
	got := f.missionEvents()
	if len(got) == 0 || !got[len(got)-1].Deleted || got[len(got)-1].MissionID != f.mission.ID {
		t.Fatalf("mission.changed events = %+v, want a final deleted one", got)
	}
}

func TestDeleteRefusesASwarmWithALiveRun(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	if _, err := f.db.CancelMission(ctx, f.mission.ID, f.human, "delete-cancel"); err != nil {
		t.Fatalf("cancel mission: %v", err)
	}
	f.runsAre(t, domain.RunRunning)
	if err := f.svc.Delete(ctx, f.human, f.params()); !errors.Is(err, store.ErrMissionPhase) || !strings.Contains(err.Error(), "wait for its runs to stop") {
		t.Fatalf("delete with live runs = %v, want ErrMissionPhase", err)
	}
	if len(f.retire.tornDown) != 0 {
		t.Fatalf("a refused delete tore down %v", f.retire.tornDown)
	}
	f.runsAre(t, domain.RunAbandoned)
	if err := f.svc.Delete(ctx, f.human, f.params()); err != nil {
		t.Fatalf("delete a cancelled swarm: %v", err)
	}
}

func (f *archiveFixture) reload(t *testing.T) *domain.Mission {
	t.Helper()
	m, err := f.db.GetMission(context.Background(), f.mission.ID)
	if err != nil {
		t.Fatalf("reload mission: %v", err)
	}
	return m
}

func TestArchivedSwarmSurvivesReconcileUntilExplicitDeletion(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	f.complete(t)
	archivedAt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	f.svc.cfg.Now = func() time.Time { return archivedAt }
	if _, err := f.svc.Archive(ctx, f.human, f.params()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	f.svc.cfg.Runs = &recordingLauncher{db: f.db}
	later := archivedAt.AddDate(1, 0, 0)
	f.svc.cfg.Now = func() time.Time { return later }
	for range 2 {
		if err := f.svc.reconcile(ctx); err != nil {
			t.Fatalf("reconcile archived swarm: %v", err)
		}
	}
	if m := f.reload(t); m.ArchivedAt == nil || !m.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("mission after reconcile = %+v, want its original archive timestamp", m)
	}
	for _, id := range f.runs() {
		run, err := f.db.GetRun(ctx, id)
		if err != nil || run.ArchivedAt == nil || !run.ArchivedAt.Equal(archivedAt) {
			t.Fatalf("run %s after reconcile = %+v, %v; want its original archive timestamp", id, run, err)
		}
	}
	submissions, err := f.db.ListSubmissions(ctx, f.mission.ID, "")
	if err != nil || len(submissions) != 1 || submissions[0].State != domain.SubmissionAccepted {
		t.Fatalf("submissions after reconcile = %+v, %v; want accepted work preserved", submissions, err)
	}
	for _, event := range f.missionEvents() {
		if event.Deleted {
			t.Fatal("reconcile published a mission deletion")
		}
	}
	if err := f.svc.Delete(ctx, f.human, f.params()); err != nil {
		t.Fatalf("explicit delete: %v", err)
	}
	if _, err := f.db.GetMission(ctx, f.mission.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mission after explicit delete = %v, want ErrNotFound", err)
	}
	for _, id := range f.runs() {
		if _, err := f.db.GetRun(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("run %s after explicit delete = %v, want ErrNotFound", id, err)
		}
	}
}

func TestSwarmOperationsRefuseAnotherMembersProtectedRun(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	if _, err := f.db.CancelMission(ctx, f.mission.ID, f.human, "protected-cancel"); err != nil {
		t.Fatalf("cancel mission: %v", err)
	}
	f.runsAre(t, domain.RunAbandoned)
	owner := regressionMember(t, f.db, "owner")
	worker, err := f.db.GetRun(ctx, f.worker)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	worker.MemberID = owner.ID
	if err := f.db.UpdateRun(ctx, worker); err != nil {
		t.Fatalf("hand worker off: %v", err)
	}
	if err := f.db.SetRunProtected(ctx, f.worker, true); err != nil {
		t.Fatalf("protect worker: %v", err)
	}
	want := "run " + string(f.worker) + ": permission denied: run is protected"
	if _, err := f.svc.Archive(ctx, f.human, f.params()); !errors.Is(err, permissions.ErrDenied) || !strings.Contains(err.Error(), want) {
		t.Fatalf("archive over a protected run = %v, want ErrDenied containing %q", err, want)
	}
	if err := f.svc.Delete(ctx, f.human, f.params()); !errors.Is(err, permissions.ErrDenied) || !strings.Contains(err.Error(), want) {
		t.Fatalf("delete over a protected run = %v, want ErrDenied containing %q", err, want)
	}
	if len(f.retire.tornDown) != 0 || f.reload(t).ArchivedAt != nil {
		t.Fatalf("a refused operation tore down %v or archived the swarm", f.retire.tornDown)
	}
	owner.Role = domain.RoleAdmin
	if err := f.db.UpdateMember(ctx, owner); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := f.svc.Delete(ctx, owner.ID, f.params()); err != nil {
		t.Fatalf("admin delete: %v", err)
	}
}

func (f *archiveFixture) runs() []domain.RunID {
	return []domain.RunID{f.mission.CurrentIntegratorRunID, f.worker}
}

func TestArchiveThatFailsPartWayArchivesNothing(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	f.complete(t)
	f.retire.failOn = 2
	if _, err := f.svc.Archive(ctx, f.human, f.params()); !errors.Is(err, errInjected) {
		t.Fatalf("archive with a failing close = %v, want the injected error", err)
	}
	if f.reload(t).ArchivedAt != nil {
		t.Fatal("a failed archive archived the swarm")
	}
	for _, run := range f.runs() {
		current, err := f.db.GetRun(ctx, run)
		if err != nil || current.ArchivedAt != nil {
			t.Fatalf("run %s after a failed archive = %+v (err %v), want unarchived like its swarm", run, current, err)
		}
	}
	if _, err := f.svc.Archive(ctx, f.human, f.params()); err != nil {
		t.Fatalf("retry archive: %v", err)
	}
	for _, run := range f.runs() {
		if current, err := f.db.GetRun(ctx, run); err != nil || current.ArchivedAt == nil {
			t.Fatalf("run %s after the retried archive = %+v (err %v), want archived", run, current, err)
		}
	}
}

func TestDeleteThatFailsPartWayFinishesOnRetry(t *testing.T) {
	ctx := context.Background()
	f := newArchiveFixture(t)
	if _, err := f.db.CancelMission(ctx, f.mission.ID, f.human, "retry-cancel"); err != nil {
		t.Fatalf("cancel mission: %v", err)
	}
	f.runsAre(t, domain.RunAbandoned)
	f.retire.failOn = 2
	if err := f.svc.Delete(ctx, f.human, f.params()); !errors.Is(err, errInjected) {
		t.Fatalf("delete with a failing run teardown = %v, want the injected error", err)
	}
	if _, err := f.db.GetMission(ctx, f.mission.ID); err != nil {
		t.Fatalf("mission after a failed delete = %v, want it kept", err)
	}
	subs, err := f.db.ListSubmissions(ctx, f.mission.ID, "")
	if err != nil || len(subs) != 1 || subs[0].State != domain.SubmissionAccepted {
		t.Fatalf("submissions after a failed delete = %+v (err %v), want the accepted one kept", subs, err)
	}
	for _, run := range f.runs() {
		if _, err := f.db.GetRun(ctx, run); err != nil {
			t.Fatalf("run %s after a failed delete = %v, want it kept", run, err)
		}
	}
	for _, event := range f.missionEvents() {
		if event.Deleted {
			t.Fatal("a failed delete published mission.changed with deleted")
		}
	}
	if err := f.svc.Delete(ctx, f.human, f.params()); err != nil {
		t.Fatalf("retry delete: %v", err)
	}
	if _, err := f.db.GetMission(ctx, f.mission.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mission after the retried delete = %v, want ErrNotFound", err)
	}
	for _, run := range f.runs() {
		if _, err := f.db.GetRun(ctx, run); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("run %s after the retried delete = %v, want ErrNotFound", run, err)
		}
	}
}
