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
		})
	}
}
