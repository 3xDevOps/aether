package mission

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func integratorReport(fix reconcileReportFixture, outcome store.CoordOutcome) *store.CoordReport {
	return &store.CoordReport{
		ID: "report-integrator", WorkspaceID: fix.mission.WorkspaceID, RunID: fix.mission.CurrentIntegratorRunID,
		Outcome: outcome, Summary: "integrator " + string(outcome),
	}
}

// TestIntegratorSuccessReportCompletesTheMissionAndStopsLeftoverWorkers: the
// swarm ends when its integrator says it is done. The integrator's own run is
// not stopped here; it finishes through the ordinary reported-outcome path.
func TestIntegratorSuccessReportCompletesTheMissionAndStopsLeftoverWorkers(t *testing.T) {
	ctx := context.Background()
	fix, _ := setupReconcileReport(t, store.CoordOutcomeSuccess)
	report := integratorReport(fix, store.CoordOutcomeSuccess)
	if err := fix.svc.ReconcileReport(ctx, fix.mission.CurrentIntegratorRunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport for the integrator: %v", err)
	}
	completed, err := fix.db.GetMission(ctx, fix.mission.ID)
	if err != nil {
		t.Fatalf("reload mission: %v", err)
	}
	if completed.Phase != domain.MissionPhaseCompleted {
		t.Fatalf("phase after the integrator's success report = %s, want completed", completed.Phase)
	}
	// A replayed report from the outbox changes nothing.
	if err = fix.svc.ReconcileReport(ctx, fix.mission.CurrentIntegratorRunID, report, fix.packet); err != nil {
		t.Fatalf("replayed integrator report: %v", err)
	}

	if err = fix.svc.reconcileMission(ctx, completed); err != nil {
		t.Fatalf("reconcile completed mission: %v", err)
	}
	if len(fix.canceller.runs) != 1 || fix.canceller.runs[0] != fix.attempt.RunID {
		t.Fatalf("stopped runs = %v, want only the leftover worker %s", fix.canceller.runs, fix.attempt.RunID)
	}
	if _, err = fix.db.UpdateRunStatus(ctx, fix.attempt.RunID, domain.RunFailed, "killed", "", nil, nil); err != nil {
		t.Fatalf("settle worker run: %v", err)
	}
	fix.svc.cfg.ObserveMissionRun = func(context.Context, domain.RunID) (MissionRunObservation, error) {
		return MissionRunObservation{State: MissionRunStopped, RetentionSettled: true}, nil
	}
	if err = fix.svc.reconcileMission(ctx, completed); err != nil {
		t.Fatalf("reconcile settled mission: %v", err)
	}
	attempt, err := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if err != nil || attempt.State != domain.AttemptCancelled {
		t.Fatalf("leftover worker after completion = %+v (err %v), want cancelled", attempt, err)
	}
}

// TestIntegratorFailureReportLeavesTheMissionActive: a failed integrator run
// ends, and the mission waits for Replace integrator.
func TestIntegratorFailureReportLeavesTheMissionActive(t *testing.T) {
	ctx := context.Background()
	fix, _ := setupReconcileReport(t, store.CoordOutcomeFailure)
	report := integratorReport(fix, store.CoordOutcomeFailure)
	if err := fix.svc.ReconcileReport(ctx, fix.mission.CurrentIntegratorRunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport for the integrator: %v", err)
	}
	m, err := fix.db.GetMission(ctx, fix.mission.ID)
	if err != nil {
		t.Fatalf("reload mission: %v", err)
	}
	if m.Phase != domain.MissionPhaseActive {
		t.Fatalf("phase after the integrator's failure report = %s, want active", m.Phase)
	}
	if len(fix.canceller.runs) != 0 {
		t.Fatalf("stopped runs = %v, want none", fix.canceller.runs)
	}
}

func TestIntegratorReportLeavesACancelledMissionCancelled(t *testing.T) {
	ctx := context.Background()
	fix, _ := setupReconcileReport(t, store.CoordOutcomeSuccess)
	if _, err := fix.db.CancelMission(ctx, fix.mission.ID, fix.mission.AccountableHumanID, "cancel-1"); err != nil {
		t.Fatalf("cancel mission: %v", err)
	}
	report := integratorReport(fix, store.CoordOutcomeSuccess)
	if err := fix.svc.ReconcileReport(ctx, fix.mission.CurrentIntegratorRunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport on a cancelled mission: %v", err)
	}
	m, err := fix.db.GetMission(ctx, fix.mission.ID)
	if err != nil || m.Phase != domain.MissionPhaseCancelled {
		t.Fatalf("mission after a late success report = %+v (err %v), want cancelled", m, err)
	}
}

// TestIntegratorCannotReportSuccessWhilePlanning: completion is only reachable
// from active, so coord.report refuses a planning success report before it is
// recorded. A failure report still ends the run for Replace integrator.
func TestIntegratorCannotReportSuccessWhilePlanning(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "integrator")
	m := regressionMission(t, db, workspace.ID, member.ID)
	regressionRun(t, db, m.CurrentIntegratorRunID, workspace.ID, member.ID, "integrator")
	svc, err := New(Config{Store: db, Missions: db, AuthorizationMu: &sync.Mutex{}})
	if err != nil {
		t.Fatalf("new mission service: %v", err)
	}
	if err := svc.ValidateReport(ctx, m.CurrentIntegratorRunID, store.CoordOutcomeSuccess); !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("planning success report = %v, want ErrMissionPhase", err)
	}
	if err := svc.ValidateReport(ctx, m.CurrentIntegratorRunID, store.CoordOutcomeFailure); err != nil {
		t.Fatalf("planning failure report: %v", err)
	}
}

// TestCompletingAMissionRetainsASubmittedWorker: a worker whose submission is
// in when the mission ends finished its work, so it is retained and recorded
// completed rather than killed and recorded cancelled.
func TestCompletingAMissionRetainsASubmittedWorker(t *testing.T) {
	ctx := context.Background()
	fix, report := setupReconcileReport(t, store.CoordOutcomeSuccess)
	if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
		t.Fatalf("worker success report: %v", err)
	}
	if err := fix.svc.ReconcileReport(ctx, fix.mission.CurrentIntegratorRunID, integratorReport(fix, store.CoordOutcomeSuccess), fix.packet); err != nil {
		t.Fatalf("integrator success report: %v", err)
	}
	completed, err := fix.db.GetMission(ctx, fix.mission.ID)
	if err != nil {
		t.Fatalf("reload mission: %v", err)
	}
	killed, retained := &recordingCanceller{}, &recordingCanceller{}
	fix.svc.cfg.Cancel, fix.svc.cfg.Complete = killed, retained
	if err = fix.svc.reconcileMission(ctx, completed); err != nil {
		t.Fatalf("reconcile completed mission: %v", err)
	}
	if len(killed.runs) != 0 || len(retained.runs) != 1 || retained.runs[0] != fix.attempt.RunID {
		t.Fatalf("killed %v, retained %v; want only %s retained", killed.runs, retained.runs, fix.attempt.RunID)
	}
	if _, err = fix.db.UpdateRunStatus(ctx, fix.attempt.RunID, domain.RunCompleted, "worker finished; retained container", "", nil, nil); err != nil {
		t.Fatalf("settle worker run: %v", err)
	}
	fix.svc.cfg.ObserveMissionRun = func(context.Context, domain.RunID) (MissionRunObservation, error) {
		return MissionRunObservation{State: MissionRunStopped, RetentionSettled: true}, nil
	}
	if err = fix.svc.reconcileMission(ctx, completed); err != nil {
		t.Fatalf("reconcile settled mission: %v", err)
	}
	attempt, err := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if err != nil || attempt.State != domain.AttemptCompleted {
		t.Fatalf("submitted worker after completion = %+v (err %v), want completed", attempt, err)
	}
}

// TestIntegratorReopensACompletedMissionByTakingUpNewWork: an interactive
// integrator stays open after its success report, so a follow-up prompt has
// to be able to dispatch again. A new task or a worker start moves the
// mission back to active once the completion's leftover workers are stopped.
// A refused or replayed call does not, a replayed report cannot complete the
// reopened mission, and a reconcile pass still holding the completed mission
// must not stop the worker just dispatched.
func TestIntegratorReopensACompletedMissionByTakingUpNewWork(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-1", title: "first"})
	pending, err := f.db.GetTask(ctx, domain.TaskID(f.propose(t, "propose-pending")))
	if err != nil {
		t.Fatalf("load pending task: %v", err)
	}
	report := func(id string) {
		t.Helper()
		r := &store.CoordReport{
			ID: id, WorkspaceID: f.mission.WorkspaceID, RunID: f.mission.CurrentIntegratorRunID,
			Outcome: store.CoordOutcomeSuccess, Summary: "done",
		}
		if reportErr := f.svc.ReconcileReport(ctx, f.mission.CurrentIntegratorRunID, r, protocol.EvidencePacket{}); reportErr != nil {
			t.Fatalf("integrator success report %s: %v", id, reportErr)
		}
	}
	wantPhase := func(after string, want domain.MissionPhase) *domain.Mission {
		t.Helper()
		m := f.reloadMission(t)
		if m.Phase != want {
			t.Fatalf("phase after %s = %s, want %s", after, m.Phase, want)
		}
		return m
	}
	report("report-1")
	completed := wantPhase("the success report", domain.MissionPhaseCompleted)

	f.phaseRefusal(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(pending.ID), Revision: 1, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-while-completed",
	})
	if startErr := f.startWorker(t, pending, "dispatch-pending"); !errors.Is(startErr, store.ErrMissionNotReady) {
		t.Fatalf("worker.start on an unaccepted task = %v, want ErrMissionNotReady", startErr)
	}
	f.propose(t, "propose-pending")
	wantPhase("refused and replayed calls", domain.MissionPhaseCompleted)

	if startErr := f.startWorker(t, tasks[0], "dispatch-follow-up"); startErr != nil {
		t.Fatalf("worker.start on a completed mission: %v", startErr)
	}
	wantPhase("worker.start", domain.MissionPhaseActive)
	report("report-1")
	wantPhase("the first report's replay", domain.MissionPhaseActive)
	if reconcileErr := f.svc.reconcileMission(ctx, completed); reconcileErr != nil {
		t.Fatalf("reconcile with the completed mission: %v", reconcileErr)
	}
	if len(f.canceller.runs) != 0 {
		t.Fatalf("stopped runs = %v, want none: the mission was reopened", f.canceller.runs)
	}

	report("report-2")
	completed = wantPhase("the follow-up's success report", domain.MissionPhaseCompleted)
	_, proposeErr := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "second follow-up", Objective: "second follow-up"},
		IdempotencyKey: "propose-follow-up",
	})
	if !errors.Is(proposeErr, store.ErrMissionNotReady) {
		t.Fatalf("task.propose before the leftover worker is stopped = %v, want ErrMissionNotReady", proposeErr)
	}
	if reconcileErr := f.svc.reconcileMission(ctx, completed); reconcileErr != nil {
		t.Fatalf("reconcile completed mission: %v", reconcileErr)
	}
	if len(f.canceller.runs) != 1 {
		t.Fatalf("stopped runs = %v, want the leftover worker", f.canceller.runs)
	}
	if _, err = f.db.UpdateRunStatus(ctx, f.canceller.runs[0], domain.RunFailed, "killed", "", nil, nil); err != nil {
		t.Fatalf("settle worker run: %v", err)
	}
	f.svc.cfg.ObserveMissionRun = func(context.Context, domain.RunID) (MissionRunObservation, error) {
		return MissionRunObservation{State: MissionRunStopped, RetentionSettled: true}, nil
	}
	if reconcileErr := f.svc.reconcileMission(ctx, completed); reconcileErr != nil {
		t.Fatalf("reconcile settled mission: %v", reconcileErr)
	}
	f.propose(t, "propose-follow-up")
	wantPhase("task.propose", domain.MissionPhaseActive)
}
