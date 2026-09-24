package mission

import (
	"context"
	"errors"
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
	_, err = svc.Create(context.Background(), member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(workspace.ID), Objective: "objective", IdempotencyKey: "create",
		Integrator:            protocol.MissionIntegrator{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"},
		ExecutionChoices:      []protocol.MissionExecutionChoice{{AccountMemberID: string(member.ID), Harness: "claude", Mode: "headless"}},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1,
	})
	want := "integrator choice account=" + string(member.ID) + " harness=claude mode=tui must be one of execution_choices"
	if err == nil || err.Error() != want {
		t.Fatalf("create error = %v, want %q", err, want)
	}
}

func TestCreateReportsPersistedMissionWhenIntegratorLaunchFails(t *testing.T) {
	for name, tc := range map[string]struct {
		writesRow bool
		next      string
	}{
		"no run row":     {next: "did not launch; the server retries the launch periodically"},
		"failed run row": {writesRow: true, next: "failed to start; replace the integrator from the Swarms page"},
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
				Integrator:            protocol.MissionIntegrator(integrator),
				ExecutionChoices:      []protocol.MissionExecutionChoice{integrator},
				MaxConcurrentAttempts: 1, MaxTotalAttempts: 1,
			})
			if !errors.Is(err, cause) {
				t.Fatalf("create error = %v, want launch failure %v", err, cause)
			}
			persisted, getErr := db.GetMission(ctx, domain.MissionID(out.Mission.ID))
			if getErr != nil || persisted.Phase != domain.MissionPhasePlanning {
				t.Fatalf("persisted mission = %+v, %v; want planning", persisted, getErr)
			}
			want := "mission " + string(persisted.ID) + " exists but its integrator run " + string(persisted.CurrentIntegratorRunID) + " " + tc.next + ": " + cause.Error()
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

// TestReconcileRecordsAndClearsTheIntegratorLaunchError: each failed relaunch
// of a reserved integrator replaces the recorded error, and the first launch
// that succeeds clears it.
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
		Integrator:            protocol.MissionIntegrator(integrator),
		ExecutionChoices:      []protocol.MissionExecutionChoice{integrator},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1,
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
	if err := db.UpdateRunStatus(ctx, m.CurrentIntegratorRunID, domain.RunFailed, "container start failed", nil, &now); err != nil {
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

// TestReconcileLeavesADeletedIntegratorRunDeleted: once the current
// integrator's row existed, a missing row means a human deleted the run, and
// only replacing the integrator starts another.
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
		Integrator:            protocol.MissionIntegrator(integrator),
		ExecutionChoices:      []protocol.MissionExecutionChoice{integrator},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1,
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

// TestHeadlessIntegratorIsRefused: a headless harness exits after one turn,
// so it could never be asked a question or told of a decision. Workers keep
// both modes.
func TestHeadlessIntegratorIsRefused(t *testing.T) {
	ctx := context.Background()
	const want = "integrator mode must be tui: a headless integrator exits after one turn and cannot be asked or told"
	f := newPlanGateFixture(t)
	headless := protocol.MissionExecutionChoice{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchHeadless)}
	_, err := f.svc.Create(ctx, f.member.ID, protocol.MissionCreateParams{
		WorkspaceID: string(f.workspace.ID), Objective: "objective", IdempotencyKey: "create-headless",
		Integrator:            protocol.MissionIntegrator(headless),
		ExecutionChoices:      []protocol.MissionExecutionChoice{headless},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1,
	})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != want {
		t.Fatalf("create with a headless integrator = %v, want invalid params %q", err, want)
	}
	missions, err := f.db.ListMissions(ctx, f.workspace.ID)
	if err != nil || len(missions) != 1 {
		t.Fatalf("missions after a refused create = %d (err %v), want only the fixture's", len(missions), err)
	}

	_, err = f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
		MissionID: string(f.mission.ID), ExpectedGeneration: f.mission.IntegratorGeneration,
		Integrator: protocol.MissionIntegrator(headless), IdempotencyKey: "replace-headless",
	})
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams || rpcErr.Message != want {
		t.Fatalf("replace with a headless integrator = %v, want invalid params %q", err, want)
	}
	if got := f.reloadMission(t).IntegratorGeneration; got != f.mission.IntegratorGeneration {
		t.Fatalf("integrator generation after a refused replace = %d, want %d", got, f.mission.IntegratorGeneration)
	}
}

// TestReplaceIntegratorAcceptsAHeadlessChoiceAsTUI: a mission created before
// integrators had to be interactive may list only headless choices, and its
// integrator is replaced by the same account and harness in tui. Create
// keeps the exact tuple; see TestCreateNamesIntegratorChoiceMissingFromExecutionChoices.
func TestReplaceIntegratorAcceptsAHeadlessChoiceAsTUI(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	legacy := &domain.Mission{
		WorkspaceID: workspace.ID, Objective: "legacy swarm", AccountableHumanID: member.ID,
		Integrator:            domain.MissionIntegrator{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchHeadless},
		ExecutionChoices:      []domain.MissionExecutionChoice{{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchHeadless}},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1, IdempotencyKey: "legacy",
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

// TestCreateReplayLaunchesNothingForADeletedOrEndedIntegrator: repeating
// mission.create with the same key retries a launch that never happened, and
// nothing else.
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
				Integrator:            protocol.MissionIntegrator(integrator),
				ExecutionChoices:      []protocol.MissionExecutionChoice{integrator},
				MaxConcurrentAttempts: 1, MaxTotalAttempts: 1,
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

// TestReplaceIntegratorRecordsWhyTheNewRunDidNotLaunch: a replacement starts
// with no launch error, so a failed launch must record its own.
func TestReplaceIntegratorRecordsWhyTheNewRunDidNotLaunch(t *testing.T) {
	for name, tc := range map[string]struct {
		writesRow bool
		next      string
	}{
		"no run row":     {next: "did not launch; the server retries the launch periodically"},
		"failed run row": {writesRow: true, next: "failed to start; replace the integrator from the Swarms page"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newPlanGateFixture(t)
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
			want := "mission " + out.Mission.ID + " exists but its integrator run " + out.Mission.CurrentIntegratorRunID + " " + tc.next + ": " + cause.Error()
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
