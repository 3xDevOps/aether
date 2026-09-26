package store

import (
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestWorkspaceDeletionRetiresFinishedMissionReferences(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := t.Context()
	ws := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, ws.ID, member.ID, 1, 2)
	if err := db.CheckWorkspaceDeletion(ctx, ws.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("pending integrator deletion = %v", err)
	}
	if _, err := db.RecordIntegratorLaunch(ctx, mission.ID, mission.CurrentIntegratorRunID, "", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	task := mustCreateMissionTask(t, db, mission.ID, "finished task")
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CheckWorkspaceDeletion(ctx, ws.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("queued attempt deletion = %v", err)
	}
	run := &domain.Run{ID: attempt.RunID, WorkspaceID: ws.ID, MemberID: member.ID, Task: "finished", Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunCompleted}
	if err = db.CreateRunWithID(ctx, run); err != nil {
		t.Fatal(err)
	}
	submission, err := db.SubmitAttempt(ctx, attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.SubmissionRef{WorkspaceID: ws.ID, RunID: run.ID, EvidenceRef: "packet", RetainedRevision: "revision"}, []domain.SubmissionEvidence{{Kind: "retained_packet", Ref: "packet", Available: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptSubmission(ctx, submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", "accept"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAttemptState(ctx, attempt.ID, run.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCompleted, "retained"); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckWorkspaceDeletion(ctx, ws.ID); err != nil {
		t.Fatalf("finished mission blocks deletion: %v", err)
	}
	if err := db.DeleteWorkspaceMissions(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRun(ctx, run.ID); err != nil {
		t.Fatalf("mission references still block run removal: %v", err)
	}
	if err := db.DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetMission(ctx, mission.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mission remains: %v", err)
	}
	if _, err := db.GetSubmission(ctx, submission.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("submission remains: %v", err)
	}
}
