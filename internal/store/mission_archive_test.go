package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestMissionArchiveMovesTheSwarmAndItsRunsTogether(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	run := mission.CurrentIntegratorRunID
	if err := db.CreateRunWithID(ctx, &domain.Run{ID: run, WorkspaceID: workspace.ID, MemberID: member.ID, Task: "integrate", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunCompleted}); err != nil {
		t.Fatalf("create integrator run: %v", err)
	}
	runs := []domain.RunID{run, "run-gone"}
	now := time.Now().UTC()
	if _, _, err := db.SetMissionArchived(ctx, mission.ID, runs, &now); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("archive an active mission = %v, want ErrMissionPhase", err)
	}
	if _, err := db.CancelMission(ctx, mission.ID, member.ID, "archive-cancel"); err != nil {
		t.Fatalf("CancelMission: %v", err)
	}
	if _, _, err := db.SetMissionArchived(ctx, mission.ID, runs, &now); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("archive with a completed, unclosed run = %v, want ErrMissionPhase", err)
	}
	archivedAt := func() (*time.Time, *time.Time) {
		t.Helper()
		m, err := db.GetMission(ctx, mission.ID)
		if err != nil {
			t.Fatalf("GetMission: %v", err)
		}
		r, err := db.GetRun(ctx, run)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		return m.ArchivedAt, r.ArchivedAt
	}
	if m, r := archivedAt(); m != nil || r != nil {
		t.Fatalf("a refused archive left mission %v and run %v archived", m, r)
	}
	if err := db.UpdateRunStatus(ctx, run, domain.RunAbandoned, "closed", nil, nil); err != nil {
		t.Fatalf("close run: %v", err)
	}
	for _, at := range []*time.Time{&now, nil} {
		for _, want := range []bool{true, false} {
			changed, changedRuns, err := db.SetMissionArchived(ctx, mission.ID, runs, at)
			if err != nil || changed != want || (len(changedRuns) == 1) != want {
				t.Fatalf("set archived %v = %v, %v, %v; want changed %v for the mission and its run", at, changed, changedRuns, err, want)
			}
		}
		if m, r := archivedAt(); (m != nil) != (at != nil) || (r != nil) != (at != nil) {
			t.Fatalf("after set archived %v: mission %v, run %v; want both to match", at, m, r)
		}
	}
}

func TestDeleteMissionRemovesItsRecordsAndReleasesItsRuns(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	if err := db.CreateRunWithID(ctx, &domain.Run{ID: mission.CurrentIntegratorRunID, WorkspaceID: workspace.ID, MemberID: member.ID, Task: "integrate", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunQueued}); err != nil {
		t.Fatalf("create integrator run: %v", err)
	}
	task := mustCreateMissionTask(t, db, mission.ID, "accepted output")
	submission := mustSubmitMissionAttempt(t, db, mission, task, "delete-output")
	mustAcceptMissionSubmission(t, db, mission, submission, "delete-accept")
	if err := db.AppendRunMessage(ctx, &RunMessage{WorkspaceID: workspace.ID, FromRun: submission.Ref.RunID, ToRun: mission.CurrentIntegratorRunID, Body: "done"}, 10); err != nil {
		t.Fatalf("AppendRunMessage: %v", err)
	}
	if err := db.DeleteMission(ctx, mission.ID, nil); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("delete an active mission = %v, want ErrMissionPhase", err)
	}
	if err := db.DeleteRun(ctx, submission.Ref.RunID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete a run its mission still references = %v, want ErrInUse", err)
	}
	if _, err := db.CancelMission(ctx, mission.ID, member.ID, "delete-cancel"); err != nil {
		t.Fatalf("CancelMission: %v", err)
	}
	if err := db.DeleteMission(ctx, mission.ID, nil); err != nil {
		t.Fatalf("DeleteMission: %v", err)
	}
	for _, table := range []string{"missions WHERE id", "mission_tasks WHERE mission_id", "mission_attempts WHERE mission_id", "mission_submissions WHERE mission_id", "mission_acceptances WHERE mission_id", "run_messages WHERE mission_id"} {
		var n int
		if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` = ?`, mission.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s after delete = %d rows (err %v), want none", table, n, err)
		}
	}
	for _, run := range []domain.RunID{mission.CurrentIntegratorRunID, submission.Ref.RunID} {
		if err := db.DeleteRun(ctx, run); err != nil {
			t.Fatalf("delete run %s after its mission: %v", run, err)
		}
	}
	if err := db.DeleteMission(ctx, mission.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete a deleted mission = %v, want ErrNotFound", err)
	}
}

func TestDeleteMissionRemovesTheGivenRunsWithIt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	task := mustCreateMissionTask(t, db, mission.ID, "accepted output")
	submission := mustSubmitMissionAttempt(t, db, mission, task, "cascade-output")
	mustAcceptMissionSubmission(t, db, mission, submission, "cascade-accept")
	runs := []domain.RunID{submission.Ref.RunID, "run-already-gone"}
	if err := db.DeleteMission(ctx, mission.ID, runs); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("delete an active mission = %v, want ErrMissionPhase", err)
	}
	if _, err := db.GetRun(ctx, submission.Ref.RunID); err != nil {
		t.Fatalf("worker after a refused delete = %v, want it kept", err)
	}
	if _, err := db.CancelMission(ctx, mission.ID, member.ID, "cascade-cancel"); err != nil {
		t.Fatalf("CancelMission: %v", err)
	}
	if err := db.DeleteMission(ctx, mission.ID, runs); err != nil {
		t.Fatalf("DeleteMission: %v", err)
	}
	if _, err := db.GetRun(ctx, submission.Ref.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("worker after delete = %v, want ErrNotFound", err)
	}
	if _, err := db.GetMission(ctx, mission.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mission after delete = %v, want ErrNotFound", err)
	}
}
