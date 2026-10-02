package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestAcceptSubmissionEvidenceRefreshIsAtomicAndIdentityBound(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 1, 2)
	task := &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{
		Title: "bounded transcript", Objective: "bounded transcript", Status: domain.TaskRevisionAccepted,
		EvidenceRequirements: []domain.EvidenceRequirement{{Kind: "transcript"}},
	}}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	attempt, _, err := reserveMissionAttempt(t, db, mission, task, "historical-evidence")
	if err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: attempt.RunID, WorkspaceID: workspace.ID, MemberID: member.ID, Task: "bounded transcript", Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunQueued}
	if err := db.CreateRunWithID(ctx, run); err != nil {
		t.Fatal(err)
	}
	ref := domain.SubmissionRef{WorkspaceID: workspace.ID, RunID: run.ID, EvidenceRef: "historical-packet", RetainedRevision: "retained-revision"}
	oldFacts := []domain.SubmissionEvidence{{Kind: "retained_packet", Ref: ref.EvidenceRef, Available: true}, {Kind: "transcript", Ref: ref.EvidenceRef, Available: false, Detail: "old observation"}}
	submission, err := db.SubmitAttempt(ctx, attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, ref, oldFacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	refreshed := append([]domain.SubmissionEvidence(nil), oldFacts...)
	refreshed[1].Available, refreshed[1].Truncated, refreshed[1].Detail = true, true, "transcript capped at 16 MiB"
	for _, scenario := range []string{"missing-validation", "workspace", "run", "packet", "revision", "fact-kind", "fact-ref", "fact-count", "unavailable", "generation", "accepted-set"} {
		t.Run(scenario, func(t *testing.T) {
			validated := &SubmissionEvidenceValidation{Ref: ref, Evidence: append([]domain.SubmissionEvidence(nil), refreshed...)}
			generation, set := mission.IntegratorGeneration, mission.AcceptedSetVersion
			want := ErrMissionNotReady
			switch scenario {
			case "missing-validation":
				validated = nil
			case "workspace":
				validated.Ref.WorkspaceID = "other-workspace"
				want = ErrMissionStale
			case "run":
				validated.Ref.RunID = "other-run"
				want = ErrMissionStale
			case "packet":
				validated.Ref.EvidenceRef = "other-packet"
				want = ErrMissionStale
			case "revision":
				validated.Ref.RetainedRevision = "other-revision"
				want = ErrMissionStale
			case "fact-kind":
				validated.Evidence[1].Kind = "input"
			case "fact-ref":
				validated.Evidence[1].Ref = "other-packet"
			case "fact-count":
				validated.Evidence = validated.Evidence[:1]
			case "unavailable":
				validated.Evidence[1].Available = false
			case "generation":
				generation++
				want = ErrMissionStale
			case "accepted-set":
				set++
				want = ErrMissionStale
			}
			if _, err := db.AcceptSubmission(ctx, submission.ID, mission.CurrentIntegratorRunID, generation, set, "", "refresh", validated); !errors.Is(err, want) {
				t.Fatalf("acceptance = %v, want %v", err, want)
			}
			after, err := db.GetSubmission(ctx, submission.ID)
			if err != nil || !reflect.DeepEqual(submission, after) {
				t.Fatalf("failed acceptance partially refreshed proposal: %#v, %v", after, err)
			}
			current, err := db.GetMission(ctx, mission.ID)
			if err != nil || current.AcceptedSetVersion != mission.AcceptedSetVersion {
				t.Fatalf("failed acceptance advanced accepted set: %#v, %v", current, err)
			}
		})
	}
	accepted, err := db.AcceptSubmission(ctx, submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", "refresh", &SubmissionEvidenceValidation{Ref: ref, Evidence: refreshed})
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.GetSubmission(ctx, submission.ID)
	if err != nil || after.State != domain.SubmissionAccepted || !reflect.DeepEqual(after.Evidence, refreshed) || after.Ref != submission.Ref || after.ProposedByRunID != submission.ProposedByRunID || !after.CreatedAt.Equal(submission.CreatedAt) {
		t.Fatalf("accepted historical proposal = %#v, %v", after, err)
	}
	// The same receipt must not require, consume, or persist a later refresh.
	replayed, err := db.AcceptSubmission(ctx, submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", "refresh", nil)
	if err != nil || !reflect.DeepEqual(accepted, replayed) {
		t.Fatalf("receipt replay = %#v, %v", replayed, err)
	}
	unchanged, err := db.GetSubmission(ctx, submission.ID)
	if err != nil || !reflect.DeepEqual(after, unchanged) {
		t.Fatalf("receipt replay rewrote accepted evidence: %#v, %v", unchanged, err)
	}
}
