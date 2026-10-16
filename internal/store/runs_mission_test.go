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
	teammate := mustCreateTeammate(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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
	if updateErr := db.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCompleted, ""); updateErr != nil {
		t.Fatalf("complete first worker: %v", updateErr)
	}
	retry, _, err := reserveMissionAttempt(t, db, mission, task, "retry-worker")
	if err != nil {
		t.Fatalf("reserve retry worker: %v", err)
	}
	worker := createRun(retry.RunID, domain.RunRunning)
	for _, key := range []string{"question-one", "question-two"} {
		if createErr := db.CreateRoomMessage(ctx, &RoomMessage{
			WorkspaceID: workspace.ID, RunID: worker.ID, ActorID: teammate.ID,
			Kind: RoomMessageQuestion, Body: "Can I proceed?", IdempotencyKey: key,
		}); createErr != nil {
			t.Fatalf("create worker question: %v", createErr)
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
			run, getErr := db.GetRun(ctx, id)
			if getErr != nil {
				t.Fatalf("GetRun %s: %v", id, getErr)
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
			runs, listErr := list.get()
			if listErr != nil {
				t.Fatalf("list by %s: %v", list.name, listErr)
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

func TestRunSnapshotSharedAttempts(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	teammate := mustCreateTeammate(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	task := mustCreateMissionTask(t, db, mission.ID, "shared run")
	worker := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
	reserve := func(m *domain.Mission, task *domain.Task, key string) {
		t.Helper()
		_, _, err := db.ReserveAttempt(ctx, &domain.AttemptReservation{
			MissionID: m.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
			DispatchKey: key, Harness: "claude", Mode: domain.LaunchHeadless,
			IntegratorGeneration: m.IntegratorGeneration, AssignedRunID: worker.ID,
		})
		if err != nil {
			t.Fatalf("reserve shared run: %v", err)
		}
	}
	reserve(mission, task, "first")
	reserve(mission, task, "second")
	if err := db.CreateRoomMessage(ctx, &RoomMessage{
		WorkspaceID: workspace.ID, RunID: worker.ID, ActorID: teammate.ID,
		Kind: RoomMessageQuestion, Body: "Proceed?", IdempotencyKey: "question",
	}); err != nil {
		t.Fatal(err)
	}
	assertSnapshots := func(missionID domain.MissionID, role string) {
		t.Helper()
		listed, err := db.ListRunsByWorkspace(ctx, workspace.ID)
		if err != nil {
			t.Fatal(err)
		}
		single, err := db.GetRun(ctx, worker.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, snapshot := range append(listed, single) {
			if snapshot.UnansweredQuestions != 1 || snapshot.MissionID != missionID || snapshot.MissionRole != role {
				t.Fatalf("shared run snapshot: questions=%d mission=%s role=%s", snapshot.UnansweredQuestions, snapshot.MissionID, snapshot.MissionRole)
			}
		}
	}
	assertSnapshots(mission.ID, "worker")
	other := mustCreatePlanningMission(t, db, workspace.ID, member.ID, "other-mission")
	if _, err := db.db.ExecContext(ctx, `UPDATE missions SET phase='active' WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	otherTask := mustCreateMissionTask(t, db, other.ID, "conflicting ownership")
	reserve(other, otherTask, "third")
	assertSnapshots("", "")
}

// An integrator waits on its swarm while a worker attempt is live or a
// worker's mail to it is unacknowledged; other mail does not count.
func TestIntegratorWaitingOnWorkers(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	task := mustCreateMissionTask(t, db, mission.ID, "bounded worker")
	createRun := func(id domain.RunID) *domain.Run {
		t.Helper()
		run := &domain.Run{ID: id, WorkspaceID: workspace.ID, MemberID: member.ID, Task: "t", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning}
		create := db.CreateRunWithID
		if id == "" {
			create = db.CreateRun
		}
		if err := create(ctx, run); err != nil {
			t.Fatalf("create run: %v", err)
		}
		return run
	}
	integrator := createRun(mission.CurrentIntegratorRunID)
	ordinary := createRun("")
	waiting := func(want bool, what string) {
		t.Helper()
		got, err := db.IntegratorWaiting(ctx, integrator.ID)
		if err != nil || got != want {
			t.Fatalf("%s: IntegratorWaiting = %v, %v; want %v", what, got, err, want)
		}
	}
	waiting(false, "no workers")
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "worker")
	if err != nil {
		t.Fatalf("reserve worker: %v", err)
	}
	worker := createRun(attempt.RunID)
	waiting(true, "a live worker")
	if err = db.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCompleted, ""); err != nil {
		t.Fatalf("complete worker: %v", err)
	}
	if err = db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: workspace.ID, FromRun: ordinary.ID, ToRun: integrator.ID, Body: "hi"}, 10); err != nil {
		t.Fatalf("append peer mail: %v", err)
	}
	waiting(false, "the worker finished; only peer mail is unread")
	if err = db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: workspace.ID, FromRun: worker.ID, ToRun: integrator.ID, Body: "done"}, 10); err != nil {
		t.Fatalf("append worker mail: %v", err)
	}
	waiting(true, "unread worker mail")
	_, token, _, err := db.DeliverRunMessages(ctx, integrator.ID, "", 10)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if _, _, _, err = db.DeliverRunMessages(ctx, integrator.ID, token, 10); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	waiting(false, "worker mail acknowledged")
}
