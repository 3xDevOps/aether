package mission

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type emptyMissionLauncher struct{}

func (emptyMissionLauncher) LaunchMission(context.Context, MissionLaunchRequest) (*domain.Run, error) {
	panic("empty mission database must not launch a run")
}

func TestStartupRecoveryFailureCanCloseAndRetry(t *testing.T) {
	db := openMissionRegressionDB(t)
	svc, err := New(Config{Store: db, Runs: emptyMissionLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 2 {
		if startErr := svc.Start(ctx); !errors.Is(startErr, context.Canceled) {
			t.Fatalf("startup recovery error = %v, want context cancellation", startErr)
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- svc.Close() }()
	select {
	case closeErr := <-closed:
		if closeErr != nil {
			t.Fatalf("close after failed startup: %v", closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close hung after failed startup")
	}
	if startErr := svc.Start(context.Background()); startErr != nil {
		t.Fatalf("retry startup with a healthy store: %v", startErr)
	}
	if closeErr := svc.Close(); closeErr != nil {
		t.Fatalf("close after successful retry: %v", closeErr)
	}
}

// failingMissionLauncher fails the launch. With a store it first writes the
// reserved run as failed, as the scheduler does when provisioning fails.
type failingMissionLauncher struct {
	db  *store.DB
	err error
}

func (l failingMissionLauncher) LaunchMission(ctx context.Context, req MissionLaunchRequest) (*domain.Run, error) {
	if l.db != nil {
		if err := l.db.CreateRunWithID(ctx, &domain.Run{
			ID: req.RunID, WorkspaceID: req.WorkspaceID, MemberID: req.RunOwnerID, Task: req.Task,
			Harness: req.Harness, Mode: req.Mode, Status: domain.RunFailed,
		}); err != nil {
			return nil, err
		}
	}
	return nil, l.err
}

func TestCreateNamesIntegratorChoiceMissingFromExecutionChoices(t *testing.T) {
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	svc, err := New(Config{Store: db, Runs: emptyMissionLauncher{}, RequireCoordination: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		integrator protocol.MissionIntegrator
		choice     protocol.MissionExecutionChoice
		want       string
	}{
		"mode": {
			integrator: protocol.MissionIntegrator{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"},
			choice:     protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "headless"},
			want:       "integrator choice account=" + string(member.ID) + " harness=claude mode=tui model= effort= must be one of execution_choices",
		},
		"model": {
			integrator: protocol.MissionIntegrator{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui", Model: "opus"},
			choice:     protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui", Model: "sonnet"},
			want:       "integrator choice account=" + string(member.ID) + " harness=claude mode=tui model=opus effort= must be one of execution_choices",
		},
		"effort": {
			integrator: protocol.MissionIntegrator{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui", Effort: "high"},
			choice:     protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui", Effort: "low"},
			want:       "integrator choice account=" + string(member.ID) + " harness=claude mode=tui model= effort=high must be one of execution_choices",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), member.ID, protocol.MissionCreateParams{
				WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create-" + name,
				Integrator: tc.integrator, ExecutionChoices: []protocol.MissionExecutionChoice{tc.choice},
			})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("create error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRecoveryTreatsAChangedModelAsStale(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	out := f.mustCall(t, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
		DispatchKey: "haiku", Harness: "claude", Mode: string(domain.LaunchHeadless), Model: "haiku", AccountOwnerID: string(f.member.ID),
	}).(protocol.WorkerStartResult)
	m := f.reloadMission(t)
	for name, req := range map[string]MissionLaunchRequest{
		"recovered integrator assignment changed": {
			WorkspaceID: m.WorkspaceID, MissionID: m.ID, IntegratorGeneration: m.IntegratorGeneration,
			RunID: m.CurrentIntegratorRunID, ActorRunID: m.CurrentIntegratorRunID,
			RunOwnerID: m.IntegratorRunOwnerID, RequestedAccount: m.Integrator.AccountMemberID,
			Task: m.Objective, Harness: m.Integrator.Harness, Mode: m.Integrator.Mode, Model: "opus",
		},
		"recovered worker assignment changed": {
			WorkspaceID: m.WorkspaceID, MissionID: m.ID, AttemptID: domain.AttemptID(out.Attempt.ID),
			IntegratorGeneration: out.Attempt.IntegratorGeneration,
			RunID:                domain.RunID(out.Attempt.RunID), ActorRunID: domain.RunID(out.Attempt.ActorRunID),
			RunOwnerID: domain.MemberID(out.Attempt.RunOwnerID), RequestedAccount: domain.MemberID(out.Attempt.AccountOwnerID),
			Task: task.Revision.Objective, Harness: out.Attempt.Harness, Mode: domain.LaunchMode(out.Attempt.Mode),
		},
	} {
		if err := f.svc.launchRecovered(ctx, req); !errors.Is(err, store.ErrMissionStale) || !strings.Contains(err.Error(), name) {
			t.Fatalf("recovery with a changed model = %v, want %q", err, name)
		}
	}
}

func TestRecoveryTreatsAChangedEffortAsStale(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixtureFor(t, "claude", domain.LaunchTUI, domain.MissionExecutionChoice{Harness: "claude", Mode: domain.LaunchHeadless, Model: "haiku", Effort: "high"})
	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	out := f.mustCall(t, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
		DispatchKey: "haiku-high", Harness: "claude", Mode: string(domain.LaunchHeadless), Model: "haiku", Effort: "high", AccountOwnerID: string(f.member.ID),
	}).(protocol.WorkerStartResult)
	m := f.reloadMission(t)
	for name, req := range map[string]MissionLaunchRequest{
		"recovered integrator assignment changed": {
			WorkspaceID: m.WorkspaceID, MissionID: m.ID, IntegratorGeneration: m.IntegratorGeneration,
			RunID: m.CurrentIntegratorRunID, ActorRunID: m.CurrentIntegratorRunID,
			RunOwnerID: m.IntegratorRunOwnerID, RequestedAccount: m.Integrator.AccountMemberID,
			Task: m.Objective, Harness: m.Integrator.Harness, Mode: m.Integrator.Mode, Effort: "high",
		},
		"recovered worker assignment changed": {
			WorkspaceID: m.WorkspaceID, MissionID: m.ID, AttemptID: domain.AttemptID(out.Attempt.ID),
			IntegratorGeneration: out.Attempt.IntegratorGeneration,
			RunID:                domain.RunID(out.Attempt.RunID), ActorRunID: domain.RunID(out.Attempt.ActorRunID),
			RunOwnerID: domain.MemberID(out.Attempt.RunOwnerID), RequestedAccount: domain.MemberID(out.Attempt.AccountOwnerID),
			Task: task.Revision.Objective, Harness: out.Attempt.Harness, Mode: domain.LaunchMode(out.Attempt.Mode), Model: out.Attempt.Model,
		},
	} {
		if err := f.svc.launchRecovered(ctx, req); !errors.Is(err, store.ErrMissionStale) || !strings.Contains(err.Error(), name) {
			t.Fatalf("recovery with a changed effort = %v, want %q", err, name)
		}
	}
}

func TestCreateReportsPersistedMissionWhenIntegratorLaunchFails(t *testing.T) {
	for name, tc := range map[string]struct {
		writesRow bool
		next      string
	}{
		"no run row":     {next: "did not launch; the server retries the launch periodically, follow it with aether swarm show <mission-id>"},
		"failed run row": {writesRow: true, next: "failed to start; replace it with aether swarm replace-integrator <mission-id> --agent <agent> or from its swarm page, or read it with aether swarm show <mission-id>"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openMissionRegressionDB(t)
			workspace := regressionWorkspace(t, db)
			member := regressionMember(t, db, "accountable")
			cause := errors.New("harness image missing")
			launcher := failingMissionLauncher{err: cause}
			if tc.writesRow {
				launcher.db = db
			}
			svc, err := New(Config{Store: db, Runs: launcher, RequireCoordination: func() error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			integrator := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"}
			out, err := svc.Create(ctx, member.ID, protocol.MissionCreateParams{
				WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create",
				Integrator:       protocol.MissionIntegrator(integrator),
				ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
			})
			if !errors.Is(err, cause) {
				t.Fatalf("create error = %v, want launch failure %v", err, cause)
			}
			persisted, getErr := db.GetMission(ctx, domain.MissionID(out.Mission.ID))
			if getErr != nil || persisted.Phase != domain.MissionPhasePlanning {
				t.Fatalf("persisted mission = %+v, %v; want planning", persisted, getErr)
			}
			want := "swarm " + string(persisted.ID) + " exists but its integrator run " + string(persisted.CurrentIntegratorRunID) + " " + strings.ReplaceAll(tc.next, "<mission-id>", string(persisted.ID)) + ": " + cause.Error()
			if err.Error() != want {
				t.Fatalf("create error = %q, want %q", err, want)
			}
			if persisted.IntegratorLaunchError != cause.Error() || persisted.IntegratorLaunchErrorAt == nil ||
				out.Mission.IntegratorLaunchError != cause.Error() || out.Mission.IntegratorLaunchErrorAt == "" {
				t.Fatalf("launch error = %q at %v, result %q at %q; want %q with a time",
					persisted.IntegratorLaunchError, persisted.IntegratorLaunchErrorAt,
					out.Mission.IntegratorLaunchError, out.Mission.IntegratorLaunchErrorAt, cause)
			}
		})
	}
}

// clearHold runs beforeClear ahead of every write that would clear the
// integrator's launch error.
type clearHold struct {
	store.MissionStore
	beforeClear func()
}

func (h clearHold) RecordIntegratorLaunch(ctx context.Context, id domain.MissionID, run domain.RunID, launchErr string, launched bool, at time.Time) (bool, error) {
	if launchErr == "" {
		h.beforeClear()
	}
	return h.MissionStore.RecordIntegratorLaunch(ctx, id, run, launchErr, launched, at)
}

// A launch releases its locks at admission, so a same-key replay can read the
// integrator's row while it still provisions. When the launch then fails, the
// replay's later write must not clear the cause the launch recorded.
func TestCreateReplayKeepsALaunchFailureRecordedAfterItReadTheRow(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	cause := errors.New("container start failed")
	launcher := &admittingLauncher{db: db, admitted: make(chan struct{}, 1), release: make(chan struct{}), err: cause}
	original := make(chan error, 1)
	var originalErr error
	hold := clearHold{MissionStore: db, beforeClear: func() {
		close(launcher.release)
		originalErr = <-original
	}}
	svc, err := New(Config{Store: db, Missions: hold, Runs: launcher, RequireCoordination: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"}
	params := protocol.MissionCreateParams{
		WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create",
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
	}
	go func() {
		_, createErr := svc.Create(ctx, member.ID, params)
		original <- createErr
	}()
	select {
	case <-launcher.admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the integrator launch was never admitted")
	}

	out, err := svc.Create(ctx, member.ID, params)
	if err != nil {
		t.Fatalf("same-key replay: %v", err)
	}
	if !errors.Is(originalErr, cause) {
		t.Fatalf("original create = %v, want %v", originalErr, cause)
	}
	m, err := db.GetMission(ctx, domain.MissionID(out.Mission.ID))
	if err != nil {
		t.Fatal(err)
	}
	if m.IntegratorLaunchError != cause.Error() || m.IntegratorLaunchErrorAt == nil || !m.IntegratorRunLaunched {
		t.Fatalf("after the replay: error %q at %v, launched %v; want %q with a time and launched",
			m.IntegratorLaunchError, m.IntegratorLaunchErrorAt, m.IntegratorRunLaunched, cause)
	}
}

func TestReconcileRecordsAndClearsTheIntegratorLaunchError(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	svc, err := New(Config{Store: db, Runs: failingMissionLauncher{err: errors.New("harness image missing")}, RequireCoordination: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"}
	out, _ := svc.Create(ctx, member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create",
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
	})
	id := domain.MissionID(out.Mission.ID)
	reconcile := func() *domain.Mission {
		t.Helper()
		m, getErr := db.GetMission(ctx, id)
		if getErr != nil {
			t.Fatalf("get mission: %v", getErr)
		}
		if reconcileErr := svc.reconcileMission(ctx, m); reconcileErr != nil {
			t.Fatalf("reconcile: %v", reconcileErr)
		}
		m, getErr = db.GetMission(ctx, id)
		if getErr != nil {
			t.Fatalf("get mission: %v", getErr)
		}
		return m
	}

	svc.cfg.Runs = failingMissionLauncher{err: errors.New("docker daemon unreachable")}
	if m := reconcile(); m.IntegratorLaunchError != "docker daemon unreachable" || m.IntegratorLaunchErrorAt == nil || m.IntegratorRunLaunched {
		t.Fatalf("after a failed relaunch: error %q at %v, launched %v; want the new cause and not launched",
			m.IntegratorLaunchError, m.IntegratorLaunchErrorAt, m.IntegratorRunLaunched)
	}

	svc.cfg.Runs = &recordingLauncher{db: db}
	m := reconcile()
	if m.IntegratorLaunchError != "" || m.IntegratorLaunchErrorAt != nil || !m.IntegratorRunLaunched {
		t.Fatalf("after a successful relaunch: error %q at %v, launched %v; want none and launched",
			m.IntegratorLaunchError, m.IntegratorLaunchErrorAt, m.IntegratorRunLaunched)
	}
	if _, runErr := db.GetRun(ctx, m.CurrentIntegratorRunID); runErr != nil {
		t.Fatalf("relaunched integrator run: %v", runErr)
	}

	// A row that failed while provisioning keeps the cause a human still
	// needs, and still counts as launched.
	now := time.Now()
	if _, err := db.UpdateRunStatus(ctx, m.CurrentIntegratorRunID, domain.RunFailed, "container start failed", "", nil, &now); err != nil {
		t.Fatalf("mark integrator run failed: %v", err)
	}
	if _, err := db.RecordIntegratorLaunch(ctx, id, m.CurrentIntegratorRunID, "container start failed", true, now); err != nil {
		t.Fatalf("record launch error: %v", err)
	}
	if m := reconcile(); m.IntegratorLaunchError != "container start failed" || m.IntegratorLaunchErrorAt == nil || !m.IntegratorRunLaunched {
		t.Fatalf("with a failed run row: error %q at %v, launched %v; want the cause kept and launched",
			m.IntegratorLaunchError, m.IntegratorLaunchErrorAt, m.IntegratorRunLaunched)
	}
}

// Once the integrator's row existed, a missing row means a human deleted it.
func TestReconcileLeavesADeletedIntegratorRunDeleted(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	svc, err := New(Config{Store: db, Runs: &recordingLauncher{db: db}, RequireCoordination: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"}
	out, err := svc.Create(ctx, member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create",
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
	})
	if err != nil || !out.Mission.IntegratorRunLaunched {
		t.Fatalf("create = %+v, %v; want a launched integrator", out.Mission, err)
	}
	if deleteErr := db.DeleteRun(ctx, domain.RunID(out.Mission.CurrentIntegratorRunID)); deleteErr != nil {
		t.Fatalf("delete integrator run: %v", deleteErr)
	}
	svc.cfg.Runs = emptyMissionLauncher{}
	m, err := db.GetMission(ctx, domain.MissionID(out.Mission.ID))
	if err != nil {
		t.Fatal(err)
	}
	if reconcileErr := svc.reconcileMission(ctx, m); reconcileErr != nil {
		t.Fatalf("reconcile: %v", reconcileErr)
	}
	if _, runErr := db.GetRun(ctx, m.CurrentIntegratorRunID); !errors.Is(runErr, store.ErrNotFound) {
		t.Fatalf("deleted integrator run after reconcile = %v, want ErrNotFound", runErr)
	}

	svc.cfg.Runs = &recordingLauncher{db: db}
	replaced, err := svc.ReplaceIntegrator(ctx, member.ID, protocol.MissionReplaceIntegratorParams{
		MissionID: out.Mission.ID, ExpectedGeneration: m.IntegratorGeneration,
		Integrator: protocol.MissionIntegrator(integrator), IdempotencyKey: "replace-1",
	})
	if err != nil || !replaced.Mission.IntegratorRunLaunched || replaced.Mission.CurrentIntegratorRunID == out.Mission.CurrentIntegratorRunID {
		t.Fatalf("replace = %+v, %v; want a new launched integrator run", replaced.Mission, err)
	}
}

func TestIntegratorModes(t *testing.T) {
	const refused = "integrator mode must be tui or acp: a headless integrator exits after one turn and cannot be asked or told"
	ctx := context.Background()
	f := newMissionFixture(t)
	headless := protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchHeadless)}
	_, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: "create-headless",
		Integrator:       protocol.MissionIntegrator(headless),
		ExecutionChoices: []protocol.MissionExecutionChoice{headless},
	})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != refused {
		t.Fatalf("create with a headless integrator = %v, want invalid params %q", err, refused)
	}
	missions, err := f.db.ListMissions(ctx, f.workspace.ID)
	if err != nil || len(missions) != 1 {
		t.Fatalf("missions after a refused create = %d (err %v), want only the fixture's", len(missions), err)
	}
	_, err = f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
		MissionID: string(f.mission.ID), ExpectedGeneration: f.mission.IntegratorGeneration,
		Integrator: protocol.MissionIntegrator(headless), IdempotencyKey: "replace-headless",
	})
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != refused {
		t.Fatalf("replace with a headless integrator = %v, want invalid params %q", err, refused)
	}
	if got := f.reloadMission(t).IntegratorGeneration; got != f.mission.IntegratorGeneration {
		t.Fatalf("integrator generation after a refused replace = %d, want %d", got, f.mission.IntegratorGeneration)
	}

	enhanced := protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchACP)}
	created, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: "create-enhanced",
		Integrator:       protocol.MissionIntegrator(enhanced),
		ExecutionChoices: []protocol.MissionExecutionChoice{enhanced},
	})
	if err != nil {
		t.Fatalf("create with an enhanced integrator: %v", err)
	}
	if created.Mission.Integrator.Mode != string(domain.LaunchACP) {
		t.Fatalf("integrator mode %q, want acp", created.Mission.Integrator.Mode)
	}
}

// Missions created before integrators had to be interactive may list only
// headless choices.
func TestReplaceIntegratorAcceptsAHeadlessChoiceAsTUI(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	legacy := &domain.Mission{
		WorkspaceID: workspace.ID, Objective: "legacy swarm", AccountableHumanID: member.ID,
		Integrator:       domain.MissionIntegrator{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchHeadless},
		ExecutionChoices: []domain.MissionExecutionChoice{{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchHeadless}},
		IdempotencyKey:   "legacy",
	}
	if err := db.CreateMission(ctx, legacy); err != nil {
		t.Fatalf("create legacy mission: %v", err)
	}
	svc, err := New(Config{Store: db, Runs: &recordingLauncher{db: db}, RequireCoordination: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	replace := func(mode domain.LaunchMode, key string) (protocol.MissionReplaceIntegratorResult, error) {
		return svc.ReplaceIntegrator(ctx, member.ID, protocol.MissionReplaceIntegratorParams{
			MissionID: string(legacy.ID), ExpectedGeneration: legacy.IntegratorGeneration, IdempotencyKey: key,
			Integrator: protocol.MissionIntegrator{AccountMemberID: string(member.ID), Harness: "claude", Mode: string(mode)},
		})
	}
	var rpcErr *protocol.Error
	if _, headlessErr := replace(domain.LaunchHeadless, "replace-headless"); !errors.As(headlessErr, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
		t.Fatalf("headless replacement = %v, want invalid params", headlessErr)
	}
	out, err := replace(domain.LaunchTUI, "replace-tui")
	if err != nil {
		t.Fatalf("tui replacement: %v", err)
	}
	if out.Mission.Integrator.Mode != string(domain.LaunchTUI) || out.Mission.IntegratorGeneration != legacy.IntegratorGeneration+1 {
		t.Fatalf("replaced integrator = %+v at generation %d, want tui at %d", out.Mission.Integrator, out.Mission.IntegratorGeneration, legacy.IntegratorGeneration+1)
	}
}

func TestSwarmRefusesAgentsAetherCannotWake(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixtureFor(t, "claude", domain.LaunchTUI, domain.MissionExecutionChoice{Harness: "copilot", Mode: domain.LaunchTUI})
	choice := func(agent string, mode domain.LaunchMode) protocol.MissionExecutionChoice {
		return protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: agent, Mode: string(mode)}
	}
	create := func(key string, integrator protocol.MissionExecutionChoice, workers ...protocol.MissionExecutionChoice) error {
		_, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
			WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: key,
			Integrator: protocol.MissionIntegrator(integrator), ExecutionChoices: append([]protocol.MissionExecutionChoice{integrator}, workers...),
		})
		return err
	}
	replace := func(mode domain.LaunchMode) error {
		_, err := f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
			MissionID: string(f.mission.ID), ExpectedGeneration: f.reloadMission(t).IntegratorGeneration, IdempotencyKey: "replace-" + string(mode),
			Integrator: protocol.MissionIntegrator(choice("copilot", mode)),
		})
		return err
	}
	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	_, startErr := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
		DispatchKey: "copilot", Harness: "copilot", Mode: string(domain.LaunchTUI), AccountOwnerID: string(f.member.ID),
	})
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"standard copilot integrator":       {create("copilot-integrator", choice("copilot", domain.LaunchTUI)), "integrator copilot in Standard mode has no idle wake; start it in Enhanced"},
		"standard copilot worker":           {create("copilot-worker", choice("claude", domain.LaunchTUI), choice("copilot", domain.LaunchTUI)), "worker copilot in Standard mode has no idle wake; start it in Enhanced"},
		"standard antigravity worker":       {create("antigravity-worker", choice("claude", domain.LaunchACP), choice("antigravity", domain.LaunchTUI)), "worker antigravity in Standard mode has no idle wake, and it has no Enhanced mode; start it as a Background worker"},
		"worker start of an older approval": {startErr, "worker copilot in Standard mode has no idle wake; start it in Enhanced"},
		"standard copilot replacement":      {replace(domain.LaunchTUI), "integrator copilot in Standard mode has no idle wake; start it in Enhanced"},
	} {
		var rpcErr *protocol.Error
		if !errors.As(tc.err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != tc.want {
			t.Errorf("%s = %v, want invalid params %q", name, tc.err, tc.want)
		}
	}
	if err := create("wakeable", choice("copilot", domain.LaunchACP), choice("claude", domain.LaunchTUI), choice("codex", domain.LaunchTUI), choice("copilot", domain.LaunchHeadless), choice("antigravity", domain.LaunchHeadless)); err != nil {
		t.Errorf("create with agents Aether can wake: %v", err)
	}
	if err := replace(domain.LaunchACP); err != nil {
		t.Errorf("replace with an Enhanced integrator of a Standard choice: %v", err)
	}
}

func TestSwarmReplaysCallsMadeBeforeTheWakeRule(t *testing.T) {
	ctx := context.Background()
	codex := domain.MissionExecutionChoice{Harness: "codex", Mode: domain.LaunchTUI}
	f := newMissionFixtureFor(t, "codex", domain.LaunchTUI, codex)
	choices := make([]protocol.MissionExecutionChoice, 0, len(f.mission.ExecutionChoices))
	for _, c := range f.mission.ExecutionChoices {
		choices = append(choices, protocol.MissionExecutionChoice{AccountMemberID: string(c.AccountMemberID), Harness: c.Harness, Mode: string(c.Mode), Model: c.Model})
	}
	replayed, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(f.workspace.ID), Objective: f.mission.Objective, IdempotencyKey: f.mission.IdempotencyKey,
		Integrator:       protocol.MissionIntegrator{AccountMemberID: string(f.member.ID), Harness: "codex", Mode: string(domain.LaunchTUI)},
		ExecutionChoices: choices,
	})
	if err != nil || replayed.Mission.ID != string(f.mission.ID) {
		t.Fatalf("create replay = %+v, %v, want mission %s", replayed.Mission, err, f.mission.ID)
	}

	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	if _, _, err = f.db.ReserveAttempt(ctx, &domain.AttemptReservation{
		MissionID: f.mission.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision, DispatchKey: "before-the-rule",
		Harness: codex.Harness, Mode: codex.Mode, ActorRunID: f.mission.CurrentIntegratorRunID,
		AuthorizingHumanID: f.member.ID, RunOwnerID: f.member.ID, AccountOwnerID: f.member.ID,
		AuthorityGeneration: f.mission.IntegratorGeneration, IntegratorGeneration: f.mission.IntegratorGeneration,
	}); err != nil {
		t.Fatalf("reserve an attempt before the rule: %v", err)
	}
	out, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: string(task.ID), TaskRevision: task.CurrentRevision,
		DispatchKey: "before-the-rule", Harness: codex.Harness, Mode: string(codex.Mode), AccountOwnerID: string(f.member.ID),
	})
	if err != nil || !out.(protocol.WorkerStartResult).Replayed {
		t.Fatalf("worker start replay = %+v, %v, want the reserved attempt", out, err)
	}
}

func TestCreateReplayLaunchesNothingForADeletedOrEndedIntegrator(t *testing.T) {
	for name, tc := range map[string]struct {
		first  Launcher
		settle func(*testing.T, *store.DB, protocol.Mission, domain.MemberID)
	}{
		"deleted run": {
			first: nil,
			settle: func(t *testing.T, db *store.DB, m protocol.Mission, _ domain.MemberID) {
				if err := db.DeleteRun(context.Background(), domain.RunID(m.CurrentIntegratorRunID)); err != nil {
					t.Fatalf("delete integrator run: %v", err)
				}
			},
		},
		"cancelled before launch": {
			first: failingMissionLauncher{err: errors.New("harness image missing")},
			settle: func(t *testing.T, db *store.DB, m protocol.Mission, member domain.MemberID) {
				if _, err := db.CancelMission(context.Background(), domain.MissionID(m.ID), member, "cancel-1"); err != nil {
					t.Fatalf("cancel mission: %v", err)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openMissionRegressionDB(t)
			workspace := regressionWorkspace(t, db)
			member := regressionMember(t, db, "accountable")
			first := tc.first
			if first == nil {
				first = &recordingLauncher{db: db}
			}
			svc, err := New(Config{Store: db, Runs: first, RequireCoordination: func() error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			integrator := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"}
			params := protocol.MissionCreateParams{
				WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create",
				Integrator:       protocol.MissionIntegrator(integrator),
				ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
			}
			created, _ := svc.Create(ctx, member.ID, params)
			tc.settle(t, db, created.Mission, member.ID)

			svc.cfg.Runs = emptyMissionLauncher{}
			replayed, err := svc.Create(ctx, member.ID, params)
			if err != nil || replayed.Mission.ID != created.Mission.ID {
				t.Fatalf("same-key create = %+v, %v; want mission %s and no error", replayed.Mission, err, created.Mission.ID)
			}
			if _, runErr := db.GetRun(ctx, domain.RunID(created.Mission.CurrentIntegratorRunID)); !errors.Is(runErr, store.ErrNotFound) {
				t.Fatalf("integrator run after the replay = %v, want ErrNotFound", runErr)
			}
		})
	}
}

func TestReplaceReplayLaunchesNothingForADeletedRun(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	svc, err := New(Config{Store: db, Runs: &recordingLauncher{db: db}, RequireCoordination: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"}
	created, err := svc.Create(ctx, member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create",
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	params := protocol.MissionReplaceIntegratorParams{
		MissionID: created.Mission.ID, ExpectedGeneration: created.Mission.IntegratorGeneration,
		Integrator: protocol.MissionIntegrator(integrator), IdempotencyKey: "replace-1",
	}
	replaced, err := svc.ReplaceIntegrator(ctx, member.ID, params)
	if err != nil || replaced.RunID == "" {
		t.Fatalf("replace = %+v, %v; want a launched run", replaced, err)
	}
	if deleteErr := db.DeleteRun(ctx, domain.RunID(replaced.RunID)); deleteErr != nil {
		t.Fatalf("delete replacement run: %v", deleteErr)
	}

	svc.cfg.Runs = emptyMissionLauncher{}
	replayed, err := svc.ReplaceIntegrator(ctx, member.ID, params)
	if err != nil || replayed.RunID != replaced.RunID || replayed.Mission.IntegratorGeneration != replaced.Mission.IntegratorGeneration {
		t.Fatalf("same-key replace = %+v, %v; want run %s at generation %d and no error", replayed, err, replaced.RunID, replaced.Mission.IntegratorGeneration)
	}
	if _, runErr := db.GetRun(ctx, domain.RunID(replaced.RunID)); !errors.Is(runErr, store.ErrNotFound) {
		t.Fatalf("replacement run after the replay = %v, want ErrNotFound", runErr)
	}
}

// A replacement starts with no launch error, so a failed launch must record
// its own.
func TestReplaceIntegratorRecordsWhyTheNewRunDidNotLaunch(t *testing.T) {
	for name, tc := range map[string]struct {
		writesRow bool
		next      string
	}{
		"no run row":     {next: "did not launch; the server retries the launch periodically, follow it with aether swarm show <mission-id>"},
		"failed run row": {writesRow: true, next: "failed to start; replace it with aether swarm replace-integrator <mission-id> --agent <agent> or from its swarm page, or read it with aether swarm show <mission-id>"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newMissionFixture(t)
			cause := errors.New("harness image missing")
			launcher := failingMissionLauncher{err: cause}
			if tc.writesRow {
				launcher.db = f.db
			}
			f.svc.cfg.Runs = launcher
			out, err := f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
				MissionID: string(f.mission.ID), ExpectedGeneration: f.mission.IntegratorGeneration, IdempotencyKey: "replace-1",
				Integrator: protocol.MissionIntegrator{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchTUI)},
			})
			want := "swarm " + out.Mission.ID + " exists but its integrator run " + out.Mission.CurrentIntegratorRunID + " " + strings.ReplaceAll(tc.next, "<mission-id>", out.Mission.ID) + ": " + cause.Error()
			if err == nil || err.Error() != want {
				t.Fatalf("replace error = %v, want %q", err, want)
			}
			m := f.reloadMission(t)
			if m.IntegratorLaunchError != cause.Error() || m.IntegratorLaunchErrorAt == nil || m.IntegratorRunLaunched != tc.writesRow {
				t.Fatalf("after a failed replacement: error %q at %v, launched %v; want %q with a time and launched %v",
					m.IntegratorLaunchError, m.IntegratorLaunchErrorAt, m.IntegratorRunLaunched, cause, tc.writesRow)
			}
		})
	}
}

// Provisioning that fails after the scheduler wrote the row still leaves a
// run that exists.
func TestReconcileMarksAFailedRowLaunched(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	svc, err := New(Config{Store: db, Runs: failingMissionLauncher{err: errors.New("harness image missing")}, RequireCoordination: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"}
	out, _ := svc.Create(ctx, member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create",
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
	})
	m, err := db.GetMission(ctx, domain.MissionID(out.Mission.ID))
	if err != nil {
		t.Fatal(err)
	}
	svc.cfg.Runs = failingMissionLauncher{db: db, err: errors.New("provisioning failed")}
	if reconcileErr := svc.reconcileMission(ctx, m); reconcileErr != nil {
		t.Fatalf("reconcile: %v", reconcileErr)
	}
	m, err = db.GetMission(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.IntegratorLaunchError != "provisioning failed" || m.IntegratorLaunchErrorAt == nil || !m.IntegratorRunLaunched {
		t.Fatalf("after a relaunch that wrote a failed row: error %q at %v, launched %v; want the cause and launched",
			m.IntegratorLaunchError, m.IntegratorLaunchErrorAt, m.IntegratorRunLaunched)
	}
}

// validatingLauncher is a launcher with no interactive command for the
// "legacy" harness.
type validatingLauncher struct {
	*recordingLauncher
	calls    int
	launches int
	// refuseAll stands for a harness definition removed after a request
	// already succeeded.
	refuseAll bool
	validated [][2]domain.MemberID
}

func (l *validatingLauncher) LaunchMission(ctx context.Context, req MissionLaunchRequest) (*domain.Run, error) {
	l.launches++
	return l.recordingLauncher.LaunchMission(ctx, req)
}

func (l *validatingLauncher) ValidateMissionLaunch(_ context.Context, member, account domain.MemberID, harnessName string, mode domain.LaunchMode, model, effort string) error {
	l.calls++
	l.validated = append(l.validated, [2]domain.MemberID{member, account})
	if l.refuseAll || (harnessName == "legacy" && mode == domain.LaunchTUI) {
		return errors.New(`scheduler: harness "legacy" has no command for mode "tui"`)
	}
	if harnessName == "legacy" && model != "" {
		return errors.New(`scheduler: agent "legacy" takes no model`)
	}
	if harnessName == "legacy" && effort != "" {
		return errors.New(`scheduler: agent "legacy" has no effort flag; launch it without an effort`)
	}
	return nil
}

func TestIntegratorTheSchedulerCannotLaunchIsRefused(t *testing.T) {
	ctx := context.Background()
	const want = `integrator harness legacy cannot launch in tui mode: scheduler: harness "legacy" has no command for mode "tui"`
	f := newMissionFixture(t)
	launcher := &validatingLauncher{recordingLauncher: f.launcher}
	f.svc.cfg.Runs = launcher
	legacy := protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: "legacy", Mode: string(domain.LaunchTUI)}
	_, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: "create-legacy",
		Integrator:       protocol.MissionIntegrator(legacy),
		ExecutionChoices: []protocol.MissionExecutionChoice{legacy},
	})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != want {
		t.Fatalf("create with a legacy integrator = %v, want invalid params %q", err, want)
	}
	if missions, listErr := f.db.ListMissions(ctx, f.workspace.ID); listErr != nil || len(missions) != 1 {
		t.Fatalf("missions after a refused create = %d (err %v), want only the fixture's", len(missions), listErr)
	}

	legacyMission := &domain.Mission{
		WorkspaceID: f.workspace.ID, Objective: "legacy swarm", AccountableHumanID: f.member.ID,
		Integrator:       domain.MissionIntegrator{AccountMemberID: f.member.ID, Harness: "legacy", Mode: domain.LaunchHeadless},
		ExecutionChoices: []domain.MissionExecutionChoice{{AccountMemberID: f.member.ID, Harness: "legacy", Mode: domain.LaunchHeadless}},
		IdempotencyKey:   "legacy-mission",
	}
	if createErr := f.db.CreateMission(ctx, legacyMission); createErr != nil {
		t.Fatalf("create legacy mission: %v", createErr)
	}
	_, err = f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
		MissionID: string(legacyMission.ID), ExpectedGeneration: legacyMission.IntegratorGeneration, IdempotencyKey: "replace-legacy",
		Integrator: protocol.MissionIntegrator(legacy),
	})
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != want {
		t.Fatalf("replace with a legacy integrator = %v, want invalid params %q", err, want)
	}
	if got, getErr := f.db.GetMission(ctx, legacyMission.ID); getErr != nil || got.IntegratorGeneration != legacyMission.IntegratorGeneration {
		t.Fatalf("legacy mission after a refused replace = %+v, %v; want generation %d", got, getErr, legacyMission.IntegratorGeneration)
	}

	calls := launcher.calls
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	if startErr := f.startWorker(t, tasks[0], "dispatch-a"); startErr != nil {
		t.Fatalf("worker start: %v", startErr)
	}
	if launcher.calls != calls {
		t.Fatalf("worker dispatch asked the validator %d times, want none", launcher.calls-calls)
	}
}

// A launch resolves the harness in the run owner's context, not the account's.
func TestCreateRefusesAWorkerModelOrEffortTheAgentCannotTake(t *testing.T) {
	ctx := context.Background()
	const want = `execution choice harness legacy cannot launch in headless mode with model "opus" and effort "": scheduler: agent "legacy" takes no model`
	const wantEffort = `execution choice harness legacy cannot launch in headless mode with model "" and effort "high": scheduler: agent "legacy" has no effort flag; launch it without an effort`
	f := newMissionFixture(t)
	f.svc.cfg.Runs = &validatingLauncher{recordingLauncher: f.launcher}
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchTUI)}
	create := func(worker protocol.MissionExecutionChoice, key string) error {
		_, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
			WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: key,
			Integrator:       protocol.MissionIntegrator(integrator),
			ExecutionChoices: []protocol.MissionExecutionChoice{integrator, worker},
		})
		return err
	}
	legacy := protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: "legacy", Mode: string(domain.LaunchHeadless), Model: "opus"}
	var rpcErr *protocol.Error
	if err := create(legacy, "create-model"); !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != want {
		t.Fatalf("create with a worker model the agent cannot take = %v, want invalid params %q", err, want)
	}
	legacy.Model, legacy.Effort = "", "high"
	if err := create(legacy, "create-effort"); !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != wantEffort {
		t.Fatalf("create with a worker effort the agent cannot take = %v, want invalid params %q", err, wantEffort)
	}
	legacy.Effort = ""
	if err := create(legacy, "create-no-model"); err != nil {
		t.Fatalf("create with a worker choice without a model or effort: %v", err)
	}
}

func TestLaunchValidationResolvesForTheRunOwner(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	launcher := &validatingLauncher{recordingLauncher: f.launcher}
	f.svc.cfg.Runs = launcher
	owner := regressionMember(t, f.db, "owner")
	if err := f.db.ShareAccount(ctx, owner.ID, f.member.ID); err != nil {
		t.Fatal(err)
	}
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(owner.ID), Harness: "claude", Mode: string(domain.LaunchTUI)}
	created, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: "create-shared",
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err = f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
		MissionID: created.Mission.ID, ExpectedGeneration: created.Mission.IntegratorGeneration, IdempotencyKey: "replace-shared",
		Integrator: protocol.MissionIntegrator(integrator),
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	want := [2]domain.MemberID{f.member.ID, owner.ID}
	if len(launcher.validated) != 2 || launcher.validated[0] != want || launcher.validated[1] != want {
		t.Fatalf("validated (run owner, account) = %v, want %v for the create and the replacement", launcher.validated, want)
	}
}

// A retry after a lost response returns the stored result even though the
// harness can no longer launch.
func TestLaunchValidationDoesNotRefuseAReplay(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	launcher := &validatingLauncher{recordingLauncher: f.launcher}
	f.svc.cfg.Runs = launcher
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchTUI)}
	create := func(key string) (protocol.MissionCreateResult, error) {
		return f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
			WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: key,
			Integrator:       protocol.MissionIntegrator(integrator),
			ExecutionChoices: []protocol.MissionExecutionChoice{integrator},
		})
	}
	created, err := create("create-lost")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	replace := func(key string) (protocol.MissionReplaceIntegratorResult, error) {
		return f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
			MissionID: created.Mission.ID, ExpectedGeneration: created.Mission.IntegratorGeneration, IdempotencyKey: key,
			Integrator: protocol.MissionIntegrator(integrator),
		})
	}
	replaced, err := replace("replace-lost")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	launcher.refuseAll = true
	launches := launcher.launches
	recreated, err := create("create-lost")
	if err != nil || recreated.Mission.ID != created.Mission.ID {
		t.Fatalf("create retry = %+v, %v; want mission %s", recreated.Mission, err, created.Mission.ID)
	}
	rereplaced, err := replace("replace-lost")
	if err != nil || rereplaced.RunID != replaced.RunID || rereplaced.Mission.IntegratorGeneration != replaced.Mission.IntegratorGeneration {
		t.Fatalf("replace retry = run %s generation %d, %v; want run %s generation %d",
			rereplaced.RunID, rereplaced.Mission.IntegratorGeneration, err, replaced.RunID, replaced.Mission.IntegratorGeneration)
	}
	if launcher.launches != launches {
		t.Fatalf("retries launched %d runs, want none", launcher.launches-launches)
	}

	var rpcErr *protocol.Error
	if _, createErr := create("create-new"); !errors.As(createErr, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
		t.Fatalf("first-time create = %v, want invalid params", createErr)
	}
	if _, replaceErr := replace("replace-new"); !errors.As(replaceErr, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
		t.Fatalf("first-time replace = %v, want invalid params", replaceErr)
	}
}
