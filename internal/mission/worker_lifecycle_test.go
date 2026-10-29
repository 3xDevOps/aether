package mission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/quota"
	"github.com/3xDevOps/Aether/internal/store"
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
	if _, err := l.db.UpdateRunStatus(ctx, run.ID, l.status, "", "", nil, nil); err != nil {
		return nil, err
	}
	run.Status = l.status
	return run, l.err
}

// admittingLauncher writes each reserved run's row, reports it admitted and
// then provisions until release closes. With err set, provisioning then
// fails the row and returns err.
type admittingLauncher struct {
	db       *store.DB
	admitted chan struct{}
	release  chan struct{}
	err      error
}

func (l *admittingLauncher) LaunchMission(ctx context.Context, req MissionLaunchRequest) (*domain.Run, error) {
	r := &domain.Run{
		ID: req.RunID, WorkspaceID: req.WorkspaceID, MemberID: req.RunOwnerID, AccountMemberID: req.AccountOwner,
		Task: req.Task, Harness: req.Harness, Mode: req.Mode, Status: domain.RunQueued,
	}
	if err := l.db.CreateRunWithID(ctx, r); err != nil {
		return nil, err
	}
	req.Admitted()
	l.admitted <- struct{}{}
	<-l.release
	if l.err != nil {
		now := time.Now()
		if _, err := l.db.UpdateRunStatus(ctx, r.ID, domain.RunFailed, l.err.Error(), "", nil, &now); err != nil {
			return nil, err
		}
		return nil, l.err
	}
	return r, nil
}

// worker.start releases the authorization and dispatch locks when its run
// is admitted, so a second worker is admitted while the first provisions.
func TestWorkerStartsOverlapAfterAdmission(t *testing.T) {
	f := newMissionFixture(t)
	tasks := f.activate(t, taskSpec{key: "first", title: "first"}, taskSpec{key: "second", title: "second"})
	launcher := &admittingLauncher{db: f.db, admitted: make(chan struct{}, 2), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(launcher.release) })
	t.Cleanup(release)
	f.svc.cfg.Runs = launcher
	errs := make(chan error, 2)
	for _, task := range tasks {
		params := protocol.WorkerStartParams{
			MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
			DispatchKey: "overlap-" + string(task.ID), Harness: "claude", Mode: string(domain.LaunchHeadless), AccountOwnerID: string(f.member.ID),
		}
		go func() {
			_, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, params)
			errs <- err
		}()
	}
	for range 2 {
		select {
		case <-launcher.admitted:
		case <-time.After(10 * time.Second):
			t.Fatal("a worker launch waited for another worker's provisioning")
		}
	}
	release()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("worker.start: %v", err)
		}
	}
}

func TestWorkerStartReturnsConfirmedLifecycle(t *testing.T) {
	for _, mode := range []domain.LaunchMode{domain.LaunchTUI, domain.LaunchHeadless} {
		t.Run(string(mode), func(t *testing.T) {
			f := newMissionFixture(t)
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

func TestWorkerStartPicksTheChoiceWithTheModel(t *testing.T) {
	f := newMissionFixture(t)
	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	start := func(model, key string) (any, error) {
		return f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, protocol.WorkerStartParams{
			MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
			DispatchKey: key, Harness: "claude", Mode: string(domain.LaunchHeadless), Model: model, AccountOwnerID: string(f.member.ID),
		})
	}
	if _, err := start("opus", "no-such-model"); err == nil || err.Error() != "mission: worker choice is not authorized" {
		t.Fatalf("worker.start with a model no choice has = %v, want not authorized", err)
	}
	out, err := start("haiku", "haiku")
	if err != nil {
		t.Fatalf("worker.start with model haiku: %v", err)
	}
	attempt := out.(protocol.WorkerStartResult).Attempt
	run, err := f.db.GetRun(context.Background(), domain.RunID(attempt.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Model != "haiku" || run.Model != "haiku" {
		t.Fatalf("attempt model %q, run model %q, want haiku", attempt.Model, run.Model)
	}
}

func TestWorkerStartPicksTheChoiceWithTheEffort(t *testing.T) {
	f := newMissionFixtureFor(t, "claude", domain.LaunchACP, domain.MissionExecutionChoice{Harness: "claude", Mode: domain.LaunchHeadless, Effort: "high"})
	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	start := func(effort, key string) (any, error) {
		return f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, protocol.WorkerStartParams{
			MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
			DispatchKey: key, Harness: "claude", Mode: string(domain.LaunchHeadless), Effort: effort, AccountOwnerID: string(f.member.ID),
		})
	}
	if _, err := start("max", "no-such-effort"); err == nil || err.Error() != "mission: worker choice is not authorized" {
		t.Fatalf("worker.start with an effort no choice has = %v, want not authorized", err)
	}
	out, err := start("high", "high")
	if err != nil {
		t.Fatalf("worker.start with effort high: %v", err)
	}
	attempt := out.(protocol.WorkerStartResult).Attempt
	run, err := f.db.GetRun(context.Background(), domain.RunID(attempt.RunID))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.db.GetAttempt(context.Background(), domain.AttemptID(attempt.ID))
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Effort != "high" || stored.Effort != "high" || run.Effort != "high" {
		t.Fatalf("attempt effort %q, stored %q, run effort %q, want high", attempt.Effort, stored.Effort, run.Effort)
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
			f := newMissionFixture(t)
			task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
			if err := f.startWorker(t, task, "recover-worker"); err != nil {
				t.Fatal(err)
			}
			attempts, err := f.db.ListAttempts(t.Context(), f.mission.ID, task.ID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("reserved attempts = %v, %v", attempts, err)
			}
			attempt := attempts[0]
			if _, err := f.db.UpdateRunStatus(t.Context(), attempt.RunID, tc.status, "", "", nil, nil); err != nil {
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
	f := newMissionFixture(t)
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

type claudeUsage map[domain.MemberID]float64

func (u claudeUsage) Read(_ context.Context, account domain.MemberID, _ bool) ([]quota.Provider, error) {
	resets := time.Now().Add(time.Hour)
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{
		{ID: "five_hour", UsedPercent: u[account], ResetsAt: &resets},
	}}}, nil
}

func TestWorkerStartFallsBackFromExhaustedDeclaredAccount(t *testing.T) {
	f := newMissionFixture(t)
	ctx := t.Context()
	owner := regressionMember(t, f.db, "owner")
	if _, err := f.db.ShareAccount(ctx, owner.ID, f.member.ID); err != nil {
		t.Fatal(err)
	}
	f.svc.cfg.Usage = claudeUsage{f.member.ID: 100, owner.ID: 20}
	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	launcher := &confirmedWorkerLauncher{recordingLauncher: f.launcher, status: domain.RunRunning}
	f.svc.cfg.Runs = launcher
	params := protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
		DispatchKey: "fallback-worker", Harness: "claude", Mode: string(domain.LaunchHeadless), AccountOwnerID: string(f.member.ID),
	}
	out := f.mustCall(t, protocol.MethodWorkerStart, params).(protocol.WorkerStartResult)
	if out.Attempt.AccountOwnerID != string(f.member.ID) || out.Attempt.LaunchAccountID != string(owner.ID) {
		t.Fatalf("attempt accounts = declared %s, launch %s; want %s, %s", out.Attempt.AccountOwnerID, out.Attempt.LaunchAccountID, f.member.ID, owner.ID)
	}
	run, err := f.db.GetRun(ctx, domain.RunID(out.Attempt.RunID))
	if err != nil || run.AccountMemberID != owner.ID || run.RequestedAccountMemberID != f.member.ID {
		t.Fatalf("worker run = %+v, %v; want account %s requested %s", run, err, owner.ID, f.member.ID)
	}
	replayed := f.mustCall(t, protocol.MethodWorkerStart, params).(protocol.WorkerStartResult)
	if !replayed.Replayed || replayed.Attempt.ID != out.Attempt.ID || replayed.Attempt.LaunchAccountID != string(owner.ID) || launcher.calls != 1 {
		t.Fatalf("dispatch replay = %+v, launches=%d", replayed, launcher.calls)
	}
	params.DispatchKey, params.AccountOwnerID = "fallback-account-as-choice", string(owner.ID)
	if _, err = f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, params); err == nil || !strings.Contains(err.Error(), "worker choice is not authorized") {
		t.Fatalf("worker.start on the fallback account = %v, want the declared choices only", err)
	}

	// The run hits its limit and moves to a third account: the worker is
	// checked against the account it runs on now, not the one it launched on.
	third := regressionMember(t, f.db, "third")
	if _, err = f.db.ShareAccount(ctx, third.ID, f.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.SetRunAccount(ctx, run.ID, third.ID, "usage limit", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.RevokeAccountShare(ctx, owner.ID, f.member.ID); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.db.GetAttempt(ctx, domain.AttemptID(out.Attempt.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.authorizeMissionTaskActor(ctx, f.reloadMission(t), attempt); err != nil {
		t.Fatalf("a moved worker is checked against the account it left: %v", err)
	}
	if _, err = f.db.RevokeAccountShare(ctx, third.ID, f.member.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.authorizeMissionTaskActor(ctx, f.reloadMission(t), attempt); err == nil {
		t.Fatal("a moved worker kept its authority after the account it runs on was revoked")
	}
}
