package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestMissionArchiveIsForAFinishedMissionAndRestorable(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
	now := time.Now().UTC()
	if _, err := db.SetMissionArchived(ctx, mission.ID, &now); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("archive an active mission = %v, want ErrMissionPhase", err)
	}
	if _, err := db.CancelMission(ctx, mission.ID, member.ID, "archive-cancel"); err != nil {
		t.Fatalf("CancelMission: %v", err)
	}
	for _, want := range []bool{true, false} {
		if changed, err := db.SetMissionArchived(ctx, mission.ID, &now); err != nil || changed != want {
			t.Fatalf("archive = %v, %v; want changed %v", changed, err, want)
		}
	}
	got, err := db.GetMission(ctx, mission.ID)
	if err != nil || got.ArchivedAt == nil || got.ArchivedAt.Unix() != now.Unix() {
		t.Fatalf("archived mission = %+v, %v; want archived_at %v", got, err, now)
	}
	for _, want := range []bool{true, false} {
		if changed, restoreErr := db.SetMissionArchived(ctx, mission.ID, nil); restoreErr != nil || changed != want {
			t.Fatalf("restore = %v, %v; want changed %v", changed, restoreErr, want)
		}
	}
	if got, err = db.GetMission(ctx, mission.ID); err != nil || got.ArchivedAt != nil {
		t.Fatalf("restored mission = %+v, %v", got, err)
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
	if err := db.DeleteMission(ctx, mission.ID); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("delete an active mission = %v, want ErrMissionPhase", err)
	}
	if err := db.DeleteRun(ctx, submission.Ref.RunID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete a run its mission still references = %v, want ErrInUse", err)
	}
	if _, err := db.CancelMission(ctx, mission.ID, member.ID, "delete-cancel"); err != nil {
		t.Fatalf("CancelMission: %v", err)
	}
	if err := db.DeleteMission(ctx, mission.ID); err != nil {
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
	if err := db.DeleteMission(ctx, mission.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete a deleted mission = %v, want ErrNotFound", err)
	}
}
