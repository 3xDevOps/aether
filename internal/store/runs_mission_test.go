package store

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRunSnapshotsProjectMissionMembership(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 2, 3)
	task := mustCreateMissionTask(t, db, mission.ID, "bounded worker")
	createRun := func(id domain.RunID, status domain.RunStatus) *domain.Run {
		t.Helper()
		run := &domain.Run{
			ID: id, WorkspaceID: workspace.ID, MemberID: member.ID,
			Task: "mission integrator worker", Harness: "claude", Mode: domain.LaunchTUI, Status: status,
		}
		var err error
		if id == "" {
			err = db.CreateRun(ctx, run)
		} else {
			err = db.CreateRunWithID(ctx, run)
		}
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		return run
	}
	ordinary := createRun("", domain.RunRunning)
	integrator := createRun(mission.CurrentIntegratorRunID, domain.RunRunning)
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "first-worker")
	if err != nil {
		t.Fatalf("reserve first worker: %v", err)
	}
	finished := createRun(attempt.RunID, domain.RunCompleted)
	if err := db.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCompleted, ""); err != nil {
		t.Fatalf("complete first worker: %v", err)
	}
	retry, _, err := reserveMissionAttempt(t, db, mission, task, "retry-worker")
	if err != nil {
		t.Fatalf("reserve retry worker: %v", err)
	}
	worker := createRun(retry.RunID, domain.RunRunning)
	for _, key := range []string{"question-one", "question-two"} {
		if err := db.CreateRoomMessage(ctx, &RoomMessage{
			WorkspaceID: workspace.ID, RunID: worker.ID, ActorID: member.ID,
			Kind: RoomMessageQuestion, Body: "Can I proceed?", IdempotencyKey: key,
		}); err != nil {
			t.Fatalf("create worker question: %v", err)
		}
	}

	type membership struct {
		mission    domain.MissionID
		role       string
		integrator domain.RunID
	}
	want := map[domain.RunID]membership{
		ordinary.ID:   {},
		integrator.ID: {mission.ID, "integrator", integrator.ID},
		finished.ID:   {mission.ID, "worker", integrator.ID},
		worker.ID:     {mission.ID, "worker", integrator.ID},
	}
	assertRun := func(run *domain.Run) {
		t.Helper()
		expected, ok := want[run.ID]
		if !ok {
			t.Fatalf("unexpected run %s", run.ID)
		}
		got := membership{run.MissionID, run.MissionRole, run.IntegratorRunID}
		if got != expected {
			t.Fatalf("run %s membership = %+v, want %+v", run.ID, got, expected)
		}
		if run.ID == worker.ID && run.UnansweredQuestions != 2 {
			t.Fatalf("worker unanswered questions = %d, want 2", run.UnansweredQuestions)
		}
	}
	assertSnapshots := func() {
		t.Helper()
		for id := range want {
			run, err := db.GetRun(ctx, id)
			if err != nil {
				t.Fatalf("GetRun %s: %v", id, err)
			}
			assertRun(run)
		}
		for _, list := range []struct {
			name string
			get  func() ([]*domain.Run, error)
		}{
			{"workspace", func() ([]*domain.Run, error) { return db.ListRunsByWorkspace(ctx, workspace.ID) }},
			{"member", func() ([]*domain.Run, error) { return db.ListRunsByMember(ctx, member.ID) }},
			{"active", func() ([]*domain.Run, error) { return db.ListActiveRuns(ctx) }},
		} {
			runs, err := list.get()
			if err != nil {
				t.Fatalf("list by %s: %v", list.name, err)
			}
			expectedCount := len(want)
			if list.name == "active" {
				expectedCount--
			}
			if len(runs) != expectedCount {
				t.Fatalf("list by %s = %d runs, want %d", list.name, len(runs), expectedCount)
			}
			for _, run := range runs {
				assertRun(run)
			}
		}
	}
	assertSnapshots()
	for _, key := range []string{"replace-initial", "replace-again"} {
		previous := mission.CurrentIntegratorRunID
		mission, err = db.ReplaceIntegrator(ctx, mission.ID, mission.IntegratorGeneration, mission.Integrator, member.ID, member.ID, key)
		if err != nil {
			t.Fatalf("replace integrator: %v", err)
		}
		current := createRun(mission.CurrentIntegratorRunID, domain.RunRunning)
		want[previous] = membership{}
		want[current.ID] = membership{mission.ID, "integrator", current.ID}
		want[finished.ID] = membership{mission.ID, "worker", current.ID}
		want[worker.ID] = membership{mission.ID, "worker", current.ID}
		assertSnapshots()
	}
}
