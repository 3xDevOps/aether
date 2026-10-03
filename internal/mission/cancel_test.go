package mission

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func (f *missionFixture) cancel(actor domain.MemberID, key string) (protocol.MissionCancelResult, error) {
	return f.svc.Cancel(context.Background(), actor, protocol.MissionCancelParams{
		MissionID: string(f.mission.ID), IdempotencyKey: key,
	})
}

// settleRuns makes every run the fixture's scheduler stopped terminal and
// reports it settled, as the scheduler does once a killed run is retained.
func (f *missionFixture) settleRuns(t *testing.T, runs ...domain.RunID) {
	t.Helper()
	for _, run := range runs {
		if err := f.db.UpdateRunStatus(t.Context(), run, domain.RunFailed, "killed", nil, nil); err != nil {
			t.Fatalf("settle run %s: %v", run, err)
		}
	}
	f.svc.cfg.ObserveMissionRun = func(context.Context, domain.RunID) (MissionRunObservation, error) {
		return MissionRunObservation{State: MissionRunStopped, RetentionSettled: true}, nil
	}
}

func TestCancelEndsAPlanningMissionAndStopsItsIntegrator(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	f.propose(t, "propose-1")
	out, err := f.cancel(f.member.ID, "cancel-1")
	if err != nil {
		t.Fatalf("cancel in planning: %v", err)
	}
	if out.Mission.Phase != string(domain.MissionPhaseCancelled) {
		t.Fatalf("cancel result phase = %s, want cancelled", out.Mission.Phase)
	}
	// Stopping is the reconcile loop's job and is retried every pass until
	// the run is terminal.
	cancelled := f.reloadMission(t)
	for range 2 {
		if reconcileErr := f.svc.reconcileMission(ctx, cancelled); reconcileErr != nil {
			t.Fatalf("reconcile cancelled mission: %v", reconcileErr)
		}
	}
	if len(f.canceller.runs) != 2 || f.canceller.runs[0] != cancelled.CurrentIntegratorRunID {
		t.Fatalf("cancelled runs = %v, want the integrator run twice", f.canceller.runs)
	}
	relaunchErr := f.svc.launchRecovered(ctx, MissionLaunchRequest{
		WorkspaceID: cancelled.WorkspaceID, MissionID: cancelled.ID,
		IntegratorGeneration: cancelled.IntegratorGeneration,
		RunID:                cancelled.CurrentIntegratorRunID, ActorRunID: cancelled.CurrentIntegratorRunID,
		RunOwnerID: cancelled.IntegratorRunOwnerID, AccountOwner: cancelled.Integrator.AccountMemberID,
		Task: cancelled.Objective, Harness: cancelled.Integrator.Harness, Mode: cancelled.Integrator.Mode,
	})
	if !errors.Is(relaunchErr, store.ErrMissionStale) {
		t.Fatalf("relaunch of a cancelled mission = %v, want ErrMissionStale", relaunchErr)
	}
	if _, replaceErr := f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
		MissionID: string(cancelled.ID), ExpectedGeneration: cancelled.IntegratorGeneration,
		Integrator:     protocol.MissionIntegrator{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchTUI)},
		IdempotencyKey: "replace-after-cancel",
	}); !errors.Is(replaceErr, store.ErrMissionPhase) {
		t.Fatalf("replace integrator after cancel = %v, want ErrMissionPhase", replaceErr)
	}
}

// TestCancelInActiveTearsDownWorkersAndTheIntegrator: a running swarm has a
// stop button, and it stops every agent the swarm runs.
func TestCancelInActiveTearsDownWorkersAndTheIntegrator(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a"}, taskSpec{key: "propose-b", title: "task b"})
	for i, task := range tasks {
		if err := f.startWorker(t, task, "dispatch-"+string(rune('a'+i))); err != nil {
			t.Fatalf("worker.start: %v", err)
		}
	}
	attempts, err := f.db.ListAttempts(ctx, f.mission.ID, "")
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %v (err %v), want two", attempts, err)
	}
	if _, err := f.cancel(f.member.ID, "cancel-1"); err != nil {
		t.Fatalf("cancel in active: %v", err)
	}
	if reconcileErr := f.svc.reconcileMission(ctx, f.reloadMission(t)); reconcileErr != nil {
		t.Fatalf("reconcile cancelled mission: %v", reconcileErr)
	}
	stopped := map[domain.RunID]bool{}
	for _, run := range f.canceller.runs {
		stopped[run] = true
	}
	want := []domain.RunID{f.mission.CurrentIntegratorRunID, attempts[0].RunID, attempts[1].RunID}
	for _, run := range want {
		if !stopped[run] {
			t.Fatalf("stopped runs = %v, want the integrator and both workers %v", f.canceller.runs, want)
		}
	}
	// A worker's capacity is released only once its run has settled.
	for _, attempt := range attempts {
		current, getErr := f.db.GetAttempt(ctx, attempt.ID)
		if getErr != nil || !current.State.HoldsConcurrency() {
			t.Fatalf("attempt before settlement = %+v (err %v), want still live", current, getErr)
		}
	}
	f.settleRuns(t, f.mission.CurrentIntegratorRunID, attempts[0].RunID, attempts[1].RunID)
	if reconcileErr := f.svc.reconcileMission(ctx, f.reloadMission(t)); reconcileErr != nil {
		t.Fatalf("reconcile settled mission: %v", reconcileErr)
	}
	for _, attempt := range attempts {
		current, getErr := f.db.GetAttempt(ctx, attempt.ID)
		if getErr != nil || current.State != domain.AttemptCancelled {
			t.Fatalf("attempt after settlement = %+v (err %v), want cancelled", current, getErr)
		}
	}
}

func TestCancelRefusesAnEndedMission(t *testing.T) {
	f := newMissionFixture(t)
	if _, err := f.cancel(f.member.ID, "cancel-1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := f.cancel(f.member.ID, "cancel-2"); !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("cancel in cancelled = %v, want ErrMissionPhase", err)
	}

	completed := newMissionFixture(t)
	completed.activate(t, taskSpec{key: "propose-a", title: "task a"})
	if _, err := completed.db.CompleteMission(context.Background(), completed.mission.ID, completed.mission.CurrentIntegratorRunID); err != nil {
		t.Fatalf("complete mission: %v", err)
	}
	if _, err := completed.cancel(completed.member.ID, "cancel-completed"); !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("cancel in completed = %v, want ErrMissionPhase", err)
	}
}

func TestCancelIsForTheAccountableHumanOrAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	other := regressionMember(t, f.db, "bystander")
	if _, err := f.cancel(other.ID, "cancel-foreign"); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("foreign cancel = %v, want ErrDenied", err)
	}
	if got := f.reloadMission(t).Phase; got != domain.MissionPhasePlanning {
		t.Fatalf("phase after a refused cancel = %s, want planning", got)
	}
	other.Role = domain.RoleAdmin
	if err := f.db.UpdateMember(ctx, other); err != nil {
		t.Fatalf("promote bystander: %v", err)
	}
	if _, err := f.cancel(other.ID, "cancel-admin"); err != nil {
		t.Fatalf("admin cancel: %v", err)
	}
}

func TestCancelReplaysOnItsKeyAndRefusesItOnAnotherMission(t *testing.T) {
	f := newMissionFixture(t)
	first, err := f.cancel(f.member.ID, "cancel-1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	replayed, err := f.cancel(f.member.ID, "cancel-1")
	if err != nil {
		t.Fatalf("replayed cancel: %v", err)
	}
	if replayed.Mission.ID != first.Mission.ID || replayed.Mission.Phase != string(domain.MissionPhaseCancelled) {
		t.Fatalf("replay = %+v, want the same cancelled mission %s", replayed.Mission, first.Mission.ID)
	}

	second := &domain.Mission{
		WorkspaceID: f.workspace.ID, Objective: "second mission", AccountableHumanID: f.member.ID,
		Integrator:       f.mission.Integrator,
		ExecutionChoices: f.mission.ExecutionChoices,
		IdempotencyKey:   "second-mission",
	}
	if err := f.db.CreateMission(context.Background(), second); err != nil {
		t.Fatalf("create second mission: %v", err)
	}
	if _, err := f.svc.Cancel(context.Background(), f.member.ID, protocol.MissionCancelParams{
		MissionID: string(second.ID), IdempotencyKey: "cancel-1",
	}); !errors.Is(err, store.ErrMissionIdempotencyConflict) {
		t.Fatalf("cancel of another mission under a used key = %v, want ErrMissionIdempotencyConflict", err)
	}
}
