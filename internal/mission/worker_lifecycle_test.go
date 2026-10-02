package mission

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type confirmedWorkerLauncher struct {
	*recordingLauncher
	status domain.RunStatus
	err    error
	calls  int
}

func (l *confirmedWorkerLauncher) LaunchMission(ctx context.Context, req MissionLaunchRequest) (*domain.Run, error) {
	l.calls++
	run, err := l.recordingLauncher.LaunchMission(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := l.db.UpdateRunStatus(ctx, run.ID, l.status, "", nil, nil); err != nil {
		return nil, err
	}
	run.Status = l.status
	return run, l.err
}

func TestWorkerStartReturnsConfirmedLifecycle(t *testing.T) {
	for _, mode := range []domain.LaunchMode{domain.LaunchTUI, domain.LaunchHeadless} {
		t.Run(string(mode), func(t *testing.T) {
			f := newPlanGateFixture(t)
			task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
			launcher := &confirmedWorkerLauncher{recordingLauncher: f.launcher, status: domain.RunRunning}
			f.svc.cfg.Runs = launcher
			params := protocol.WorkerStartParams{
				MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
				DispatchKey: "confirmed-launch", Harness: "claude", Mode: string(mode), AccountOwnerID: string(f.member.ID),
			}
			out := f.mustCall(t, protocol.MethodWorkerStart, params).(protocol.WorkerStartResult)
			if out.Attempt.State != string(domain.AttemptRunning) || out.Attempt.StartedAt == nil {
				t.Fatalf("worker.start after confirmed launch = %+v, want running with started_at", out.Attempt)
			}
			replayed := f.mustCall(t, protocol.MethodWorkerStart, params).(protocol.WorkerStartResult)
			if !replayed.Replayed || replayed.Attempt.ID != out.Attempt.ID || replayed.Attempt.State != out.Attempt.State || launcher.calls != 1 {
				t.Fatalf("dispatch replay = %+v, launches=%d", replayed, launcher.calls)
			}
			listed := f.mustCall(t, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: string(f.mission.ID)}).(protocol.WorkerListResult)
			if len(listed.Attempts) != 1 || listed.Attempts[0].State != string(domain.AttemptRunning) || listed.Attempts[0].StartedAt == nil {
				t.Fatalf("durable worker lifecycle = %+v", listed.Attempts)
			}
		})
	}
}

func TestReconcilePromotesOnlyConfirmedLiveWorkers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status domain.RunStatus
		state  MissionRunState
		err    error
		want   domain.AttemptState
	}{
		{"running", domain.RunRunning, MissionRunActive, nil, domain.AttemptRunning},
		{"attention", domain.RunNeedsAttention, MissionRunActive, nil, domain.AttemptRunning},
		{"queued", domain.RunQueued, MissionRunPending, nil, domain.AttemptLaunching},
		{"provisioning", domain.RunProvisioning, MissionRunActive, nil, domain.AttemptLaunching},
		{"unknown-owner", domain.RunRunning, MissionRunUnknown, nil, domain.AttemptLaunching},
		{"observer-error", domain.RunRunning, MissionRunActive, errors.New("observer unavailable"), domain.AttemptLaunching},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlanGateFixture(t)
			task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
			if err := f.startWorker(t, task, "recover-worker"); err != nil {
				t.Fatal(err)
			}
			attempts, err := f.db.ListAttempts(t.Context(), f.mission.ID, task.ID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("reserved attempts = %v, %v", attempts, err)
			}
			attempt := attempts[0]
			if err := f.db.UpdateRunStatus(t.Context(), attempt.RunID, tc.status, "", nil, nil); err != nil {
				t.Fatal(err)
			}
			f.svc.cfg.ObserveMissionRun = func(context.Context, domain.RunID) (MissionRunObservation, error) {
				return MissionRunObservation{State: tc.state}, tc.err
			}
			if err := f.svc.reconcileMission(t.Context(), f.reloadMission(t)); err != nil {
				t.Fatal(err)
			}
			out := f.mustCall(t, protocol.MethodWorkerInspect, protocol.WorkerInspectParams{AttemptID: string(attempt.ID)}).(protocol.WorkerInspectResult)
			if out.Attempt.State != string(tc.want) || (out.Attempt.StartedAt != nil) != (tc.want == domain.AttemptRunning) {
				t.Fatalf("worker after recovery = %+v, want %s", out.Attempt, tc.want)
			}
		})
	}
}

func TestFailedWorkerLaunchRemainsUnknownUntilConfirmedRecovery(t *testing.T) {
	f := newPlanGateFixture(t)
	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	launchErr := errors.New("launch response lost")
	launcher := &confirmedWorkerLauncher{recordingLauncher: f.launcher, status: domain.RunRunning, err: launchErr}
	f.svc.cfg.Runs = launcher
	if err := f.startWorker(t, task, "uncertain-worker"); !errors.Is(err, launchErr) {
		t.Fatalf("worker launch error = %v, want %v", err, launchErr)
	}
	attempts, err := f.db.ListAttempts(t.Context(), f.mission.ID, task.ID)
	if err != nil || len(attempts) != 1 || attempts[0].State != domain.AttemptUnknown || attempts[0].StartedAt != nil {
		t.Fatalf("uncertain worker = %+v, %v", attempts, err)
	}
	f.svc.cfg.ObserveMissionRun = func(context.Context, domain.RunID) (MissionRunObservation, error) {
		return MissionRunObservation{State: MissionRunActive}, nil
	}
	if reconcileErr := f.svc.reconcileMission(t.Context(), f.reloadMission(t)); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	attempt, err := f.db.GetAttempt(t.Context(), attempts[0].ID)
	if err != nil || attempt.State != domain.AttemptRunning || attempt.StartedAt == nil || attempt.LastError != "" || launcher.calls != 1 {
		t.Fatalf("confirmed recovery = %+v, launches=%d, error=%v", attempt, launcher.calls, err)
	}
	if stateErr := f.db.UpdateAttemptState(t.Context(), attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCancelled, "cancelled"); stateErr != nil {
		t.Fatal(stateErr)
	}
	if reconcileErr := f.svc.reconcileMission(t.Context(), f.reloadMission(t)); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	settled, err := f.db.GetAttempt(t.Context(), attempt.ID)
	if err != nil || settled.State != domain.AttemptCancelled || launcher.calls != 1 {
		t.Fatalf("recovery revived a settled worker: %+v, %v", settled, err)
	}
}
