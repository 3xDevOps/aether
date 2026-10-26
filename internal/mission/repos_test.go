package mission

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// TestSwarmRunsCheckOutTheSwarmsRepos: the extra repos a swarm is created
// with reach its integrator and every worker launch, and stay the swarm's.
func TestSwarmRunsCheckOutTheSwarmsRepos(t *testing.T) {
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	api := &domain.Workspace{Name: "api"}
	if err := db.CreateWorkspace(ctx, api); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	member := regressionMember(t, db, "accountable")
	launcher := &recordingLauncher{db: db}
	svc, err := New(Config{Store: db, Runs: launcher, RequireCoordination: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "tui"}
	worker := protocol.MissionExecutionChoice{AccountMemberID: string(member.ID), Harness: "claude", Mode: "headless"}
	params := protocol.MissionCreateParams{
		WorkspaceID: string(workspace.ID), Objective: "rename the users endpoint", IdempotencyKey: "create",
		Integrator:        protocol.MissionIntegrator(integrator),
		ExecutionChoices:  []protocol.MissionExecutionChoice{integrator, worker},
		ExtraWorkspaceIDs: []string{string(api.ID)},
	}

	missing := params
	missing.IdempotencyKey, missing.ExtraWorkspaceIDs = "missing", []string{"ws-missing"}
	if _, err = svc.Create(ctx, member.ID, missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("create with an unknown repo = %v, want not found", err)
	}

	out, err := svc.Create(ctx, member.ID, params)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !slices.Equal(out.Mission.ExtraWorkspaceIDs, params.ExtraWorkspaceIDs) {
		t.Fatalf("created swarm repos = %v, want %v", out.Mission.ExtraWorkspaceIDs, params.ExtraWorkspaceIDs)
	}
	stored, err := db.GetMission(ctx, domain.MissionID(out.Mission.ID))
	if err != nil {
		t.Fatalf("reload swarm: %v", err)
	}
	f := &missionFixture{db: db, svc: svc, mission: stored, workspace: workspace, member: member, launcher: launcher}
	tasks := f.activate(t, taskSpec{key: "propose", title: "rename the client call", paths: []string{"client"}})
	if err = f.startWorker(t, tasks[0], "dispatch"); err != nil {
		t.Fatalf("worker.start: %v", err)
	}
	want := []domain.WorkspaceID{api.ID}
	if len(launcher.requests) != 2 {
		t.Fatalf("launches = %d, want the integrator and one worker", len(launcher.requests))
	}
	for _, req := range launcher.requests {
		if !slices.Equal(req.ExtraWorkspaces, want) {
			t.Fatalf("launch of run %s carries repos %v, want %v", req.RunID, req.ExtraWorkspaces, want)
		}
	}

	params.ExtraWorkspaceIDs = nil
	if _, err = svc.Create(ctx, member.ID, params); !errors.Is(err, store.ErrMissionIdempotencyConflict) {
		t.Fatalf("replay without the repo = %v, want an idempotency conflict", err)
	}
	if err = db.CheckWorkspaceDeletion(ctx, api.ID); !errors.Is(err, store.ErrInUse) {
		t.Fatalf("deleting the swarm's repo = %v, want in use", err)
	}
}
