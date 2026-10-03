package mission

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func integratorReport(fix reconcileReportFixture, outcome store.CoordOutcome) *store.CoordReport {
	return &store.CoordReport{
		WorkspaceID: fix.mission.WorkspaceID, RunID: fix.mission.CurrentIntegratorRunID,
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
	if err = fix.db.UpdateRunStatus(ctx, fix.attempt.RunID, domain.RunFailed, "killed", nil, nil); err != nil {
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
	if err = fix.db.UpdateRunStatus(ctx, fix.attempt.RunID, domain.RunCompleted, "worker finished; retained container", nil, nil); err != nil {
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
