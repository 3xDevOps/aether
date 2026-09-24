package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func planningFixture(t *testing.T) (*DB, *domain.Mission, domain.MemberID) {
	t.Helper()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	return db, mustCreatePlanningMission(t, db, workspace.ID, member.ID, 2, 8, "plan-mission-1"), member.ID
}

func mustProposePlanTask(t *testing.T, db *DB, m *domain.Mission, title, key string) *domain.Task {
	t.Helper()
	return mustProposeScopedPlanTask(t, db, m, title, key, domain.TaskScope{})
}

func mustProposeScopedPlanTask(t *testing.T, db *DB, m *domain.Mission, title, key string, scope domain.TaskScope) *domain.Task {
	t.Helper()
	task := &domain.Task{MissionID: m.ID, Revision: &domain.TaskRevision{Title: title, Objective: title, Scope: scope, ProposedByRunID: m.CurrentIntegratorRunID}}
	created, _, err := db.CreateTaskWithIdempotency(context.Background(), task, key)
	if err != nil {
		t.Fatalf("CreateTaskWithIdempotency: %v", err)
	}
	return created
}

func mustAskAndAnswer(t *testing.T, db *DB, m *domain.Mission, member domain.MemberID) *domain.MissionQuestion {
	t.Helper()
	q, err := db.InsertMissionQuestion(context.Background(), m.ID, m.CurrentIntegratorRunID, "which checkout flow?", "ask-q1")
	if err != nil {
		t.Fatalf("InsertMissionQuestion: %v", err)
	}
	answered, err := db.AnswerMissionQuestion(context.Background(), q.ID, member, "the guest flow", "answer-q1")
	if err != nil {
		t.Fatalf("AnswerMissionQuestion: %v", err)
	}
	return answered
}

func mustCompleteClarification(t *testing.T, db *DB, m *domain.Mission, key string) {
	t.Helper()
	if _, err := db.CompleteMissionClarification(context.Background(), m.ID, m.CurrentIntegratorRunID, key); err != nil {
		t.Fatalf("CompleteMissionClarification: %v", err)
	}
}

// mustSubmittedPlan drives a fresh mission all the way to plan_review.
func mustSubmittedPlan(t *testing.T, db *DB, m *domain.Mission, member domain.MemberID) *domain.MissionPlanReview {
	t.Helper()
	mustAskAndAnswer(t, db, m, member)
	mustProposePlanTask(t, db, m, "bounded worker", "task-1")
	mustCompleteClarification(t, db, m, "clarify-1")
	review, err := db.SubmitMissionPlan(context.Background(), m.ID, m.CurrentIntegratorRunID, "what will be built and why", "submit-1")
	if err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}
	return review
}

// mustApprovedMission returns an active mission whose one task a human
// approved, with the declared scope as the approved scope union.
func mustApprovedMission(t *testing.T, db *DB, m *domain.Mission, member domain.MemberID, scope domain.TaskScope) *domain.Task {
	t.Helper()
	ctx := context.Background()
	task := mustProposeScopedPlanTask(t, db, m, "bounded worker", "task-1", scope)
	mustCompleteClarification(t, db, m, "clarify-1")
	if _, err := db.SubmitMissionPlan(ctx, m.ID, m.CurrentIntegratorRunID, "the initial plan", "submit-1"); err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}
	if _, err := db.DecideMissionPlan(ctx, m.ID, 1, domain.MissionPlanApprove, "", member, "decide-1"); err != nil {
		t.Fatalf("DecideMissionPlan: %v", err)
	}
	m.Phase, m.PlanVersion = domain.MissionPhaseActive, 1
	projected, err := db.ProjectTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	return projected
}

func missionPhase(t *testing.T, db *DB, id domain.MissionID) (domain.MissionPhase, uint64) {
	t.Helper()
	m, err := db.GetMission(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMission: %v", err)
	}
	return m.Phase, m.PlanVersion
}

func revisionStatus(t *testing.T, db *DB, task domain.TaskID, revision int) string {
	t.Helper()
	var status string
	if err := db.db.QueryRowContext(context.Background(), `SELECT status FROM mission_task_revisions WHERE task_id=? AND revision=?`, task, revision).Scan(&status); err != nil {
		t.Fatalf("read revision %d status: %v", revision, err)
	}
	return status
}

func TestMissionPhaseRefusesDispatchAndAcceptWhilePlanning(t *testing.T) {
	db, mission, _ := planningFixture(t)
	ctx := context.Background()
	task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")

	if _, _, err := reserveMissionAttempt(t, db, mission, task, "dispatch-a"); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("ReserveAttempt in planning = %v, want ErrMissionPhase", err)
	}
	if err := db.AcceptTaskRevision(ctx, task.ID, task.CurrentRevision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-1"); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("AcceptTaskRevision in planning = %v, want ErrMissionPhase", err)
	}
	worker := &domain.TaskRevision{Title: "worker edit", Objective: "worker edit", ProposedByRunID: "worker-run"}
	if _, err := db.ReviseTask(ctx, task.ID, worker, "worker-revise-1"); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("ReviseTask in planning = %v, want ErrMissionPhase", err)
	}
	// propose, revise, and abandon are the integrator's planning tools.
	if _, err := db.ProposeTaskRevision(ctx, task.ID, &domain.TaskRevision{Title: "sharper", Objective: "sharper", ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2"); err != nil {
		t.Fatalf("ProposeTaskRevision in planning: %v", err)
	}
	if err := db.AbandonTask(ctx, task.ID, 0, mission.IntegratorGeneration, "abandon-1"); err != nil {
		t.Fatalf("AbandonTask in planning: %v", err)
	}
}

func TestMissionPhaseFreezesEveryTaskMutationDuringPlanReview(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	mustAskAndAnswer(t, db, mission, member)
	mustCompleteClarification(t, db, mission, "clarify-1")
	if _, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1"); err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}

	revision := &domain.TaskRevision{Title: "sharper", Objective: "sharper", ProposedByRunID: mission.CurrentIntegratorRunID}
	for name, refusal := range map[string]error{
		"task.propose": func() error {
			_, _, e := db.CreateTaskWithIdempotency(ctx, &domain.Task{MissionID: mission.ID, Revision: revision}, "task-2")
			return e
		}(),
		"task.revise": func() error {
			_, e := db.ProposeTaskRevision(ctx, task.ID, revision, "propose-2")
			return e
		}(),
		"task.accept":  db.AcceptTaskRevision(ctx, task.ID, task.CurrentRevision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-1"),
		"task.abandon": db.AbandonTask(ctx, task.ID, 0, mission.IntegratorGeneration, "abandon-1"),
		"worker.start": func() error {
			_, _, e := reserveMissionAttempt(t, db, mission, task, "dispatch-a")
			return e
		}(),
		"mission.question.ask": func() error {
			_, e := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "late question", "ask-late")
			return e
		}(),
		"mission.clarification.complete": func() error {
			_, e := db.CompleteMissionClarification(ctx, mission.ID, mission.CurrentIntegratorRunID, "clarify-late")
			return e
		}(),
	} {
		if !errors.Is(refusal, ErrMissionPhase) {
			t.Errorf("%s in plan_review = %v, want ErrMissionPhase", name, refusal)
		}
	}
	// replace-integrator is the one recovery that plan_review still admits.
	if _, err := db.ReplaceIntegrator(ctx, mission.ID, mission.IntegratorGeneration, mission.Integrator, member, member, "replace-1"); err != nil {
		t.Fatalf("ReplaceIntegrator in plan_review: %v", err)
	}
}

func TestMissionDraftReviseAdvancesCurrentRevisionAndSupersedes(t *testing.T) {
	for _, phase := range []domain.MissionPhase{domain.MissionPhasePlanning, domain.MissionPhaseClarified} {
		t.Run(string(phase), func(t *testing.T) {
			db, mission, _ := planningFixture(t)
			ctx := context.Background()
			task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
			if phase == domain.MissionPhaseClarified {
				mustCompleteClarification(t, db, mission, "clarify-1")
			}

			revised, err := db.ProposeTaskRevision(ctx, task.ID, &domain.TaskRevision{Title: "sharper", Objective: "sharper scope", ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2")
			if err != nil {
				t.Fatalf("ProposeTaskRevision: %v", err)
			}
			if revised.Revision != 2 {
				t.Fatalf("revised revision = %d, want 2", revised.Revision)
			}
			projected, err := db.ProjectTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("ProjectTask: %v", err)
			}
			if projected.CurrentRevision != 2 {
				t.Fatalf("current revision = %d, want 2; the human would review a stale draft", projected.CurrentRevision)
			}
			if projected.PendingRevision != nil {
				t.Fatalf("pending revision = %+v, want none: the draft is the current revision", projected.PendingRevision)
			}
			if got := revisionStatus(t, db, task.ID, 1); got != string(domain.TaskRevisionSuperseded) {
				t.Fatalf("revision 1 status = %s, want superseded", got)
			}
			if got := revisionStatus(t, db, task.ID, 2); got != string(domain.TaskRevisionProposed) {
				t.Fatalf("revision 2 status = %s, want proposed", got)
			}
		})
	}
}

func TestMissionActiveReviseLeavesCurrentRevisionForAcceptance(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID, 1, 2)
	task := mustCreateMissionTask(t, db, mission.ID, "bounded worker")

	if _, err := db.ProposeTaskRevision(ctx, task.ID, &domain.TaskRevision{Title: "sharper", Objective: "sharper", ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2"); err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	projected, err := db.ProjectTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	if projected.CurrentRevision != 1 {
		t.Fatalf("current revision = %d, want 1: only task.accept advances it once active", projected.CurrentRevision)
	}
	if projected.PendingRevision == nil || projected.PendingRevision.Revision != 2 {
		t.Fatalf("pending revision = %+v, want revision 2", projected.PendingRevision)
	}
}

func TestMissionQuestionsRecordAskAnswerAndBounds(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()

	first, err := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "which checkout flow?", "ask-1")
	if err != nil {
		t.Fatalf("InsertMissionQuestion: %v", err)
	}
	if first.Seq != 1 || first.AnsweredAt != nil {
		t.Fatalf("first question = seq %d answered %v, want seq 1 unanswered", first.Seq, first.AnsweredAt)
	}
	replay, err := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "which checkout flow?", "ask-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replayed ask = %v, %v, want the original question", replay, err)
	}
	if _, conflictErr := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "a different question", "ask-1"); !errors.Is(conflictErr, ErrMissionIdempotencyConflict) {
		t.Fatalf("reused key with a new body = %v, want ErrMissionIdempotencyConflict", conflictErr)
	}
	if m, getErr := db.GetMission(ctx, mission.ID); getErr != nil || m.OpenQuestions != 1 {
		t.Fatalf("GetMission open questions = %v, %v, want 1", m, getErr)
	}

	answered, err := db.AnswerMissionQuestion(ctx, first.ID, member, "the guest flow", "answer-1")
	if err != nil {
		t.Fatalf("AnswerMissionQuestion: %v", err)
	}
	if answered.Answer != "the guest flow" || answered.AnsweredByMemberID != member || answered.AnsweredAt == nil {
		t.Fatalf("answered question = %+v, want the answer, the answering member, and a timestamp", answered)
	}
	if again, replayErr := db.AnswerMissionQuestion(ctx, first.ID, member, "the guest flow", "answer-1"); replayErr != nil || again.Answer != answered.Answer {
		t.Fatalf("replayed answer = %v, %v, want the original row", again, replayErr)
	}
	if _, conflictErr := db.AnswerMissionQuestion(ctx, first.ID, member, "no, the express flow", "answer-2"); !errors.Is(conflictErr, ErrConflict) {
		t.Fatalf("second answer = %v, want ErrConflict", conflictErr)
	}
	if _, emptyErr := db.AnswerMissionQuestion(ctx, first.ID, member, "   ", "answer-3"); emptyErr == nil {
		t.Fatal("whitespace answer was accepted, want a rejection before the transaction")
	}

	for i := 2; i <= domain.MaxMissionQuestions; i++ {
		if _, askErr := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, fmt.Sprintf("question %d", i), fmt.Sprintf("ask-%d", i)); askErr != nil {
			t.Fatalf("InsertMissionQuestion %d: %v", i, askErr)
		}
	}
	if _, limitErr := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "one too many", "ask-overflow"); !errors.Is(limitErr, ErrMissionLimit) {
		t.Fatalf("question %d = %v, want ErrMissionLimit", domain.MaxMissionQuestions+1, limitErr)
	}
	questions, err := db.ListMissionQuestions(ctx, mission.ID)
	if err != nil || len(questions) != domain.MaxMissionQuestions {
		t.Fatalf("ListMissionQuestions = %d questions, %v, want %d", len(questions), err, domain.MaxMissionQuestions)
	}
	for i, q := range questions {
		if q.Seq != i+1 {
			t.Fatalf("question %d has seq %d, want %d: the list is ordered by seq", i, q.Seq, i+1)
		}
	}
}

func TestMissionClarificationIsExplicitAndQuestionsAreOptional(t *testing.T) {
	db, mission, _ := planningFixture(t)
	ctx := context.Background()
	mustProposePlanTask(t, db, mission, "bounded worker", "task-1")

	// No question was needed: the integrator says so, and the plan submits.
	if _, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-early"); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("submit while planning = %v, want ErrMissionPhase", err)
	}
	mustCompleteClarification(t, db, mission, "clarify-1")
	if phase, _ := missionPhase(t, db, mission.ID); phase != domain.MissionPhaseClarified {
		t.Fatalf("phase = %s, want clarified", phase)
	}
	review, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1")
	if err != nil {
		t.Fatalf("SubmitMissionPlan with no questions: %v", err)
	}
	if review.SubmittedPhase != domain.MissionPhaseClarified {
		t.Fatalf("submitted_phase = %s, want clarified", review.SubmittedPhase)
	}
}

func TestMissionClarificationCompleteRefusesAnUnansweredQuestion(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	q, err := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "which checkout flow?", "ask-1")
	if err != nil {
		t.Fatalf("InsertMissionQuestion: %v", err)
	}
	_, err = db.CompleteMissionClarification(ctx, mission.ID, mission.CurrentIntegratorRunID, "clarify-1")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "1 questions are unanswered; wait for answers, then complete clarification") {
		t.Fatalf("complete with an open question = %v, want the unanswered-question refusal", err)
	}
	if _, answerErr := db.AnswerMissionQuestion(ctx, q.ID, member, "the guest flow", "answer-1"); answerErr != nil {
		t.Fatalf("AnswerMissionQuestion: %v", answerErr)
	}
	mustCompleteClarification(t, db, mission, "clarify-1")
	mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	if _, submitErr := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1"); submitErr != nil {
		t.Fatalf("SubmitMissionPlan after an answered question: %v", submitErr)
	}
}

func TestMissionQuestionInClarifiedReopensPlanning(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	mustCompleteClarification(t, db, mission, "clarify-1")

	q, err := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "one more thing?", "ask-late")
	if err != nil {
		t.Fatalf("InsertMissionQuestion in clarified: %v", err)
	}
	if phase, _ := missionPhase(t, db, mission.ID); phase != domain.MissionPhasePlanning {
		t.Fatalf("phase after a new question = %s, want planning", phase)
	}
	if _, submitErr := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1"); !errors.Is(submitErr, ErrMissionPhase) {
		t.Fatalf("submit after reopening = %v, want ErrMissionPhase", submitErr)
	}
	if _, completeErr := db.CompleteMissionClarification(ctx, mission.ID, mission.CurrentIntegratorRunID, "clarify-2"); !errors.Is(completeErr, ErrMissionPhase) {
		t.Fatalf("complete with the new question open = %v, want ErrMissionPhase", completeErr)
	}
	if _, answerErr := db.AnswerMissionQuestion(ctx, q.ID, member, "yes", "answer-late"); answerErr != nil {
		t.Fatalf("AnswerMissionQuestion: %v", answerErr)
	}
	mustCompleteClarification(t, db, mission, "clarify-2")
}

func TestMissionClarificationCompleteReplayAfterAReviseRoundConflicts(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	mustCompleteClarification(t, db, mission, "clarify-1")
	// The same key still replays while the mission sits in clarified.
	mustCompleteClarification(t, db, mission, "clarify-1")
	if _, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1"); err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}
	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanRevise, "narrow the scope", member, "decide-1"); err != nil {
		t.Fatalf("DecideMissionPlan: %v", err)
	}
	_, err := db.CompleteMissionClarification(ctx, mission.ID, mission.CurrentIntegratorRunID, "clarify-1")
	if !errors.Is(err, ErrMissionIdempotencyConflict) || !strings.Contains(err.Error(), "clarification was already completed for an earlier round") {
		t.Fatalf("replayed complete after a revise round = %v, want ErrMissionIdempotencyConflict", err)
	}
	mustCompleteClarification(t, db, mission, "clarify-2")
}

func TestMissionPlanSubmitRequiresAPendingRevision(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustAskAndAnswer(t, db, mission, member)
	mustCompleteClarification(t, db, mission, "clarify-1")

	_, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "propose at least one task or revision before submitting") {
		t.Fatalf("submit with no task = %v, want the empty-pending-set refusal", err)
	}

	task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	review, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1")
	if err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}
	if review.PlanVersion != 1 || review.Decision != "" || review.DecidedAt != nil {
		t.Fatalf("review = %+v, want version 1 with no decision", review)
	}
	if phase, version := missionPhase(t, db, mission.ID); phase != domain.MissionPhasePlanReview || version != 1 {
		t.Fatalf("mission = phase %s version %d, want plan_review and 1", phase, version)
	}
	reviews, listErr := db.ListMissionPlanReviews(ctx, mission.ID)
	if listErr != nil || len(reviews) != 1 || len(reviews[0].Items) != 1 {
		t.Fatalf("ListMissionPlanReviews = %v, %v, want one round with one item", reviews, listErr)
	}
	item := reviews[0].Items[0]
	if item.TaskID != task.ID || item.Revision != 1 || !item.NewTask || item.Material || len(item.Widening) != 0 || item.Title != "bounded worker" {
		t.Fatalf("plan item = %+v, want the new task's revision 1 widening nothing", item)
	}
	replay, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1")
	if err != nil || replay.PlanVersion != 1 {
		t.Fatalf("replayed submit = %v, %v, want plan version 1 with no increment", replay, err)
	}
	if phase, version := missionPhase(t, db, mission.ID); phase != domain.MissionPhasePlanReview || version != 1 {
		t.Fatalf("after replay mission = phase %s version %d, want plan_review and 1", phase, version)
	}
}

func TestMissionPlanSubmitReplayAfterDecisionIsAConflict(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustSubmittedPlan(t, db, mission, member)
	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanRevise, "narrow the scope", member, "decide-1"); err != nil {
		t.Fatalf("DecideMissionPlan: %v", err)
	}
	_, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "what will be built and why", "submit-1")
	if !errors.Is(err, ErrMissionIdempotencyConflict) || !strings.Contains(err.Error(), "plan version 1 was already decided") {
		t.Fatalf("replay after a decision = %v, want ErrMissionIdempotencyConflict naming the decided version", err)
	}
	// A fresh key opens the next round once clarification is complete again.
	mustCompleteClarification(t, db, mission, "clarify-2")
	next, nextErr := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "a narrower plan", "submit-2")
	if nextErr != nil || next.PlanVersion != 2 {
		t.Fatalf("second submit = %v, %v, want plan version 2", next, nextErr)
	}
}

func TestMissionPlanDecideApproveAcceptsExactlyTheRoundsItems(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustAskAndAnswer(t, db, mission, member)
	kept := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	dropped := mustProposePlanTask(t, db, mission, "second worker", "task-2")
	if _, err := db.ProposeTaskRevision(ctx, kept.ID, &domain.TaskRevision{Title: "sharper", Objective: "sharper", ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2"); err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if err := db.AbandonTask(ctx, dropped.ID, 0, mission.IntegratorGeneration, "abandon-1"); err != nil {
		t.Fatalf("AbandonTask: %v", err)
	}
	mustCompleteClarification(t, db, mission, "clarify-1")
	if _, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1"); err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}

	approved, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanApprove, "", member, "decide-1")
	if err != nil {
		t.Fatalf("DecideMissionPlan: %v", err)
	}
	if approved.Phase != domain.MissionPhaseActive {
		t.Fatalf("approved mission phase = %s, want active", approved.Phase)
	}
	if got := revisionStatus(t, db, kept.ID, 2); got != string(domain.TaskRevisionAccepted) {
		t.Fatalf("kept task revision 2 status = %s, want accepted", got)
	}
	if got := revisionStatus(t, db, kept.ID, 1); got != string(domain.TaskRevisionSuperseded) {
		t.Fatalf("kept task revision 1 status = %s, want superseded", got)
	}
	if got := revisionStatus(t, db, dropped.ID, 1); got != string(domain.TaskRevisionAbandoned) {
		t.Fatalf("abandoned task status = %s, want abandoned: approval must not resurrect it", got)
	}
	if approved.AcceptedSetVersion != 0 {
		t.Fatalf("accepted_set_version = %d, want 0: planning produces no accepted output", approved.AcceptedSetVersion)
	}
	var acceptedBy domain.MemberID
	if scanErr := db.db.QueryRowContext(ctx, `SELECT accepted_by_member_id FROM mission_task_revisions WHERE task_id=? AND revision=2`, kept.ID).Scan(&acceptedBy); scanErr != nil {
		t.Fatalf("read accepted_by_member_id: %v", scanErr)
	}
	if acceptedBy != member {
		t.Fatalf("accepted_by_member_id = %q, want the deciding member %q", acceptedBy, member)
	}
	reviews, reviewErr := db.ListMissionPlanReviews(ctx, mission.ID)
	if reviewErr != nil || len(reviews) != 1 || reviews[0].Decision != domain.MissionPlanApprove || reviews[0].DecidedByMemberID != member || reviews[0].DecidedAt == nil {
		t.Fatalf("ListMissionPlanReviews = %v, %v, want one approved review recorded against the deciding member", reviews, reviewErr)
	}
	current, projectErr := db.ProjectTask(ctx, kept.ID)
	if projectErr != nil {
		t.Fatalf("ProjectTask: %v", projectErr)
	}
	if _, _, reserveErr := reserveMissionAttempt(t, db, approved, current, "dispatch-a"); reserveErr != nil {
		t.Fatalf("ReserveAttempt after approval: %v", reserveErr)
	}
}

func TestMissionClarifiedReviseTwiceApprovesTheSecondRevision(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	mustCompleteClarification(t, db, mission, "clarify-1")
	for i, key := range []string{"propose-2", "propose-3"} {
		if _, err := db.ProposeTaskRevision(ctx, task.ID, &domain.TaskRevision{Title: fmt.Sprintf("draft %d", i), Objective: "sharper", ProposedByRunID: mission.CurrentIntegratorRunID}, key); err != nil {
			t.Fatalf("ProposeTaskRevision %s: %v", key, err)
		}
	}
	if _, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1"); err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}
	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanApprove, "", member, "decide-1"); err != nil {
		t.Fatalf("DecideMissionPlan: %v", err)
	}
	projected, err := db.ProjectTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	if projected.CurrentRevision != 3 || projected.Revision.Status != domain.TaskRevisionAccepted {
		t.Fatalf("approved task = revision %d status %s, want revision 3 accepted", projected.CurrentRevision, projected.Revision.Status)
	}
}

func TestMissionPlanDecideRefusesWrongPhaseVersionAndSecondDecision(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()

	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanApprove, "", member, "decide-early"); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("decide while planning = %v, want ErrMissionPhase", err)
	}
	mustSubmittedPlan(t, db, mission, member)
	if _, err := db.DecideMissionPlan(ctx, mission.ID, 7, domain.MissionPlanApprove, "", member, "decide-wrong-version"); !errors.Is(err, ErrConflict) {
		t.Fatalf("decide with a stale version = %v, want ErrConflict", err)
	}
	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, "escalate", "", member, "decide-bad"); err == nil || errors.Is(err, ErrMissionPhase) {
		t.Fatalf("decide with an unknown verdict = %v, want an invalid-request error", err)
	}
	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanRevise, "  ", member, "decide-no-feedback"); err == nil {
		t.Fatal("revise without feedback was accepted, want a rejection")
	}

	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanRevise, "narrow the scope", member, "decide-1"); err != nil {
		t.Fatalf("DecideMissionPlan: %v", err)
	}
	if phase, version := missionPhase(t, db, mission.ID); phase != domain.MissionPhasePlanning || version != 1 {
		t.Fatalf("after revise mission = phase %s version %d, want planning and 1", phase, version)
	}
	// Only a replay of the same key returns without deciding again; the row
	// itself refuses a second verdict even if the phase were forced back.
	if _, err := db.db.ExecContext(ctx, `UPDATE missions SET phase=? WHERE id=?`, domain.MissionPhasePlanReview, mission.ID); err != nil {
		t.Fatalf("force plan_review: %v", err)
	}
	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanApprove, "", member, "decide-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second decision on plan version 1 = %v, want ErrConflict", err)
	}
}

func TestMissionPlanDecideRejectIsTerminalForEveryMutation(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustSubmittedPlan(t, db, mission, member)

	rejected, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanReject, "out of scope", member, "decide-1")
	if err != nil {
		t.Fatalf("DecideMissionPlan: %v", err)
	}
	if rejected.Phase != domain.MissionPhaseRejected {
		t.Fatalf("rejected mission phase = %s, want rejected", rejected.Phase)
	}
	if _, askErr := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "another?", "ask-late"); !errors.Is(askErr, ErrMissionPhase) {
		t.Fatalf("ask after reject = %v, want ErrMissionPhase", askErr)
	}
	if _, replaceErr := db.ReplaceIntegrator(ctx, mission.ID, mission.IntegratorGeneration, mission.Integrator, member, member, "replace-1"); !errors.Is(replaceErr, ErrMissionPhase) {
		t.Fatalf("replace integrator after reject = %v, want ErrMissionPhase", replaceErr)
	}
	replay, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanReject, "out of scope", member, "decide-1")
	if err != nil || replay.Phase != domain.MissionPhaseRejected {
		t.Fatalf("replayed decision = %v, %v, want the rejected mission", replay, err)
	}
}

func TestMissionPlanApproveRefusesARoundWhoseItemsAreGone(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustSubmittedPlan(t, db, mission, member)
	// plan_review admits no task mutation, so these states can only arrive
	// through a write this transaction did not make. The guards still have to
	// hold: approving nothing would activate a mission with no work.
	if _, err := db.db.ExecContext(ctx, `UPDATE mission_task_revisions SET status='abandoned' WHERE task_id IN (SELECT id FROM mission_tasks WHERE mission_id=?)`, mission.ID); err != nil {
		t.Fatalf("abandon every revision: %v", err)
	}
	if _, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanApprove, "", member, "decide-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("approve an item that is no longer proposed = %v, want ErrConflict", err)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM mission_plan_items WHERE mission_id=?`, mission.ID); err != nil {
		t.Fatalf("delete plan items: %v", err)
	}
	_, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanApprove, "", member, "decide-2")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "the plan has no proposed task to accept; request changes to return the mission to planning") {
		t.Fatalf("approve with no item = %v, want the empty-plan refusal", err)
	}
	if phase, _ := missionPhase(t, db, mission.ID); phase != domain.MissionPhasePlanReview {
		t.Fatalf("mission phase = %s, want plan_review: the failed approval must roll back", phase)
	}
}

func TestMissionAbandonRacingPlanSubmitLeavesOneWinner(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustAskAndAnswer(t, db, mission, member)
	task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	mustCompleteClarification(t, db, mission, "clarify-1")

	var wg sync.WaitGroup
	wg.Add(2)
	var abandonErr, submitErr error
	go func() {
		defer wg.Done()
		abandonErr = db.AbandonTask(ctx, task.ID, 0, mission.IntegratorGeneration, "abandon-1")
	}()
	go func() {
		defer wg.Done()
		_, submitErr = db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1")
	}()
	wg.Wait()

	phase, _ := missionPhase(t, db, mission.ID)
	if submitErr == nil {
		// The submit won: the plan is frozen and the abandon is refused.
		if phase != domain.MissionPhasePlanReview {
			t.Fatalf("phase = %s, want plan_review after a successful submit", phase)
		}
		if abandonErr != nil && !errors.Is(abandonErr, ErrMissionPhase) {
			t.Fatalf("abandon = %v, want nil or ErrMissionPhase", abandonErr)
		}
		return
	}
	// The abandon won: the plan has no pending revision left to submit.
	if abandonErr != nil {
		t.Fatalf("neither side succeeded: abandon %v, submit %v", abandonErr, submitErr)
	}
	if !errors.Is(submitErr, ErrMissionPhase) || !strings.Contains(submitErr.Error(), "propose at least one task or revision") {
		t.Fatalf("submit = %v, want the empty-pending-set refusal", submitErr)
	}
	if phase != domain.MissionPhaseClarified {
		t.Fatalf("phase = %s, want clarified after a refused submit", phase)
	}
}

func TestListMissionsPageReportsOpenQuestionsPerMission(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	first := mustCreatePlanningMission(t, db, workspace.ID, member.ID, 1, 2, "plan-mission-1")
	second := mustCreatePlanningMission(t, db, workspace.ID, member.ID, 1, 2, "plan-mission-2")

	open, err := db.InsertMissionQuestion(ctx, first.ID, first.CurrentIntegratorRunID, "which checkout flow?", "ask-1")
	if err != nil {
		t.Fatalf("InsertMissionQuestion: %v", err)
	}
	if _, askErr := db.InsertMissionQuestion(ctx, first.ID, first.CurrentIntegratorRunID, "which payment provider?", "ask-2"); askErr != nil {
		t.Fatalf("InsertMissionQuestion: %v", askErr)
	}
	if _, answerErr := db.AnswerMissionQuestion(ctx, open.ID, member.ID, "the guest flow", "answer-1"); answerErr != nil {
		t.Fatalf("AnswerMissionQuestion: %v", answerErr)
	}

	missions, _, listErr := db.ListMissionsPage(ctx, workspace.ID, 0, "")
	if listErr != nil {
		t.Fatalf("ListMissionsPage: %v", listErr)
	}
	counts := make(map[domain.MissionID]int, len(missions))
	for _, m := range missions {
		counts[m.ID] = m.OpenQuestions
	}
	if counts[first.ID] != 1 || counts[second.ID] != 0 {
		t.Fatalf("open questions = %v, want 1 for %s and 0 for %s", counts, first.ID, second.ID)
	}
}

// TestMissionSelfAcceptanceStaysInsideTheApprovedPlan pins every reason the
// integrator has to hand a revision to a human instead of accepting it alone.
func TestMissionSelfAcceptanceStaysInsideTheApprovedPlan(t *testing.T) {
	approvedScope := domain.TaskScope{ExpectedPaths: []string{"internal/store"}, Exclusions: []string{"internal/store/migrate.go"}}
	propose := func(t *testing.T, db *DB, m *domain.Mission, task *domain.Task, key string, r *domain.TaskRevision) int {
		t.Helper()
		r.ProposedByRunID = m.CurrentIntegratorRunID
		revision, err := db.ProposeTaskRevision(context.Background(), task.ID, r, key)
		if err != nil {
			t.Fatalf("ProposeTaskRevision: %v", err)
		}
		return revision.Revision
	}

	t.Run("same scope is accepted", func(t *testing.T) {
		db, mission, member := planningFixture(t)
		task := mustApprovedMission(t, db, mission, member, approvedScope)
		revision := propose(t, db, mission, task, "propose-2", &domain.TaskRevision{Title: "sharper", Objective: "sharper", Scope: approvedScope})
		if err := db.AcceptTaskRevision(context.Background(), task.ID, revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-2"); err != nil {
			t.Fatalf("AcceptTaskRevision inside the approved scope: %v", err)
		}
		var acceptedByRun domain.RunID
		if scanErr := db.db.QueryRowContext(context.Background(), `SELECT accepted_by_run_id FROM mission_task_revisions WHERE task_id=? AND revision=?`, task.ID, revision).Scan(&acceptedByRun); scanErr != nil {
			t.Fatalf("read accepted_by_run_id: %v", scanErr)
		}
		if acceptedByRun != mission.CurrentIntegratorRunID {
			t.Fatalf("accepted_by_run_id = %q, want the integrator run %q", acceptedByRun, mission.CurrentIntegratorRunID)
		}
	})

	t.Run("material needs a human", func(t *testing.T) {
		db, mission, member := planningFixture(t)
		task := mustApprovedMission(t, db, mission, member, approvedScope)
		revision := propose(t, db, mission, task, "propose-2", &domain.TaskRevision{Title: "bigger", Objective: "bigger", Scope: approvedScope, Material: true})
		err := db.AcceptTaskRevision(context.Background(), task.ID, revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-2")
		if !errors.Is(err, ErrMissionAmendmentRequired) || !strings.Contains(err.Error(), "is material; submit it with mission plan submit") {
			t.Fatalf("accept a material revision = %v, want the amendment refusal", err)
		}
	})

	t.Run("widening the approved scope needs a human", func(t *testing.T) {
		db, mission, member := planningFixture(t)
		task := mustApprovedMission(t, db, mission, member, approvedScope)
		revision := propose(t, db, mission, task, "propose-2", &domain.TaskRevision{Title: "wider", Objective: "wider", Scope: domain.TaskScope{ExpectedPaths: []string{"internal/store", "internal/server"}, Exclusions: approvedScope.Exclusions}})
		err := db.AcceptTaskRevision(context.Background(), task.ID, revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-2")
		if !errors.Is(err, ErrMissionAmendmentRequired) || !strings.Contains(err.Error(), "widens the approved scope to [internal/server]") {
			t.Fatalf("accept a widening revision = %v, want the amendment refusal naming the path", err)
		}
	})

	t.Run("dropping an exclusion needs a human", func(t *testing.T) {
		db, mission, member := planningFixture(t)
		task := mustApprovedMission(t, db, mission, member, approvedScope)
		revision := propose(t, db, mission, task, "propose-2", &domain.TaskRevision{Title: "looser", Objective: "looser", Scope: domain.TaskScope{ExpectedPaths: approvedScope.ExpectedPaths}})
		err := db.AcceptTaskRevision(context.Background(), task.ID, revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-2")
		if !errors.Is(err, ErrMissionAmendmentRequired) || !strings.Contains(err.Error(), "drops exclusion internal/store/migrate.go from the approved scope") {
			t.Fatalf("accept a revision dropping an exclusion = %v, want the amendment refusal", err)
		}
	})

	t.Run("new work needs a human", func(t *testing.T) {
		db, mission, member := planningFixture(t)
		mustApprovedMission(t, db, mission, member, approvedScope)
		fresh := mustProposeScopedPlanTask(t, db, mission, "extra worker", "task-2", approvedScope)
		err := db.AcceptTaskRevision(context.Background(), fresh.ID, 1, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-fresh")
		if !errors.Is(err, ErrMissionAmendmentRequired) || !strings.Contains(err.Error(), "is new work; submit it with mission plan submit") {
			t.Fatalf("accept a new task inside the approved paths = %v, want the amendment refusal", err)
		}
	})

	t.Run("an abandoned planning draft cannot be revived", func(t *testing.T) {
		db, mission, member := planningFixture(t)
		ctx := context.Background()
		draft := mustProposeScopedPlanTask(t, db, mission, "dropped worker", "task-draft", approvedScope)
		// Revising in planning supersedes the first draft in place; that
		// status alone must not read as a human approval later.
		propose(t, db, mission, draft, "propose-draft-2", &domain.TaskRevision{Title: "dropped worker", Objective: "dropped worker", Scope: approvedScope})
		if err := db.AbandonTask(ctx, draft.ID, 0, mission.IntegratorGeneration, "abandon-draft"); err != nil {
			t.Fatalf("AbandonTask: %v", err)
		}
		mustApprovedMission(t, db, mission, member, approvedScope)
		_, reviveErr := db.ProposeTaskRevision(ctx, draft.ID, &domain.TaskRevision{Title: "revived", Objective: "revived", Scope: approvedScope, ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-draft-3")
		if !errors.Is(reviveErr, ErrConflict) || !strings.Contains(reviveErr.Error(), "is abandoned") {
			t.Fatalf("revise an abandoned task = %v, want the abandoned conflict", reviveErr)
		}
		// The abandoned revision itself is no longer proposed, so it cannot
		// be accepted either.
		if err := db.AcceptTaskRevision(ctx, draft.ID, 2, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-draft"); !errors.Is(err, ErrConflict) {
			t.Fatalf("accept an abandoned draft = %v, want ErrConflict", err)
		}
	})

	t.Run("a revision sent back needs another round", func(t *testing.T) {
		db, mission, member := planningFixture(t)
		ctx := context.Background()
		task := mustApprovedMission(t, db, mission, member, approvedScope)
		revision := propose(t, db, mission, task, "propose-2", &domain.TaskRevision{Title: "bigger", Objective: "bigger", Scope: approvedScope, Material: true})
		if _, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "one amendment", "submit-2"); err != nil {
			t.Fatalf("SubmitMissionPlan: %v", err)
		}
		if _, err := db.DecideMissionPlan(ctx, mission.ID, 2, domain.MissionPlanRevise, "too broad", member, "decide-2"); err != nil {
			t.Fatalf("DecideMissionPlan: %v", err)
		}
		// Stripping the material flag must not buy a way around the human.
		if _, err := db.db.ExecContext(ctx, `UPDATE mission_task_revisions SET material=0 WHERE task_id=? AND revision=?`, task.ID, revision); err != nil {
			t.Fatalf("clear material: %v", err)
		}
		err := db.AcceptTaskRevision(ctx, task.ID, revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-2")
		if !errors.Is(err, ErrMissionAmendmentRequired) || !strings.Contains(err.Error(), "was sent back in plan version 2") {
			t.Fatalf("accept a revision sent back = %v, want the amendment refusal naming the round", err)
		}
		// Re-proposing the declined change as a fresh, unmarked revision must
		// not get around the decision either.
		successor := propose(t, db, mission, task, "propose-3", &domain.TaskRevision{Title: "bigger", Objective: "bigger", Scope: approvedScope})
		err = db.AcceptTaskRevision(ctx, task.ID, successor, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-3")
		if !errors.Is(err, ErrMissionAmendmentRequired) || !strings.Contains(err.Error(), "was sent back in plan version 2") {
			t.Fatalf("accept a successor of a revision sent back = %v, want the amendment refusal", err)
		}
		// Another round that approves the task lifts the hold.
		if _, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "second try", "submit-3"); err != nil {
			t.Fatalf("SubmitMissionPlan: %v", err)
		}
		if _, err := db.DecideMissionPlan(ctx, mission.ID, 3, domain.MissionPlanApprove, "", member, "decide-3"); err != nil {
			t.Fatalf("DecideMissionPlan: %v", err)
		}
		tweak := propose(t, db, mission, task, "propose-4", &domain.TaskRevision{Title: "bigger, reworded", Objective: "bigger, reworded", Scope: approvedScope})
		if err := db.AcceptTaskRevision(ctx, task.ID, tweak, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-4"); err != nil {
			t.Fatalf("self-accept after the task was re-approved: %v", err)
		}
	})
}

// TestMissionAmendmentFreezesChangesWhileApprovedWorkContinues covers the
// whole amendment round: submit from active, what it freezes, what keeps
// dispatching, and what approval then accepts.
func TestMissionAmendmentFreezesChangesWhileApprovedWorkContinues(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	scope := domain.TaskScope{ExpectedPaths: []string{"internal/store"}}
	amended := mustApprovedMission(t, db, mission, member, scope)
	untouched := mustProposeScopedPlanTask(t, db, mission, "second worker", "task-2", scope)
	if err := db.AcceptTaskRevision(ctx, untouched.ID, 1, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-untouched"); !errors.Is(err, ErrMissionAmendmentRequired) {
		t.Fatalf("self-accepting new work = %v, want the amendment refusal", err)
	}
	pending, err := db.ProposeTaskRevision(ctx, amended.ID, &domain.TaskRevision{Title: "wider", Objective: "wider", Scope: domain.TaskScope{ExpectedPaths: []string{"internal/store", "internal/server"}}, ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}

	review, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "what changes and why", "submit-2")
	if err != nil {
		t.Fatalf("SubmitMissionPlan from active: %v", err)
	}
	if review.SubmittedPhase != domain.MissionPhaseActive || review.PlanVersion != 2 {
		t.Fatalf("amendment review = %+v, want plan version 2 submitted from active", review)
	}
	if phase, version := missionPhase(t, db, mission.ID); phase != domain.MissionPhaseAmendmentReview || version != 2 {
		t.Fatalf("mission = phase %s version %d, want amendment_review and 2", phase, version)
	}
	reviews, listErr := db.ListMissionPlanReviews(ctx, mission.ID)
	if listErr != nil || len(reviews) != 2 {
		t.Fatalf("ListMissionPlanReviews = %v, %v, want two rounds", reviews, listErr)
	}
	items := reviews[1].Items
	if len(items) != 2 {
		t.Fatalf("amendment items = %+v, want the pending revision and the new task", items)
	}
	byTask := map[domain.TaskID]domain.MissionPlanItem{}
	for _, item := range items {
		byTask[item.TaskID] = item
	}
	if got := byTask[amended.ID]; got.Revision != pending.Revision || got.NewTask || len(got.Widening) != 1 || got.Widening[0] != "internal/server" {
		t.Fatalf("amended item = %+v, want revision %d widening internal/server", got, pending.Revision)
	}
	if got := byTask[untouched.ID]; !got.NewTask || got.Revision != 1 {
		t.Fatalf("new-work item = %+v, want revision 1 marked new_task", got)
	}

	for name, refusal := range map[string]error{
		"task.propose": func() error {
			_, _, e := db.CreateTaskWithIdempotency(ctx, &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "third", Objective: "third"}}, "task-3")
			return e
		}(),
		"task.revise": func() error {
			_, e := db.ProposeTaskRevision(ctx, amended.ID, &domain.TaskRevision{Title: "later", Objective: "later"}, "propose-3")
			return e
		}(),
		"task.accept":  db.AcceptTaskRevision(ctx, amended.ID, pending.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-pending"),
		"task.abandon": db.AbandonTask(ctx, amended.ID, pending.Revision, mission.IntegratorGeneration, "abandon-pending"),
		"mission.question.ask": func() error {
			_, e := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "late question", "ask-late")
			return e
		}(),
	} {
		if !errors.Is(refusal, ErrMissionPhase) {
			t.Errorf("%s in amendment_review = %v, want ErrMissionPhase", name, refusal)
		}
	}

	// The amended task is held back; work the human already approved and did
	// not touch keeps dispatching.
	if _, _, reserveErr := reserveMissionAttempt(t, db, mission, amended, "dispatch-amended"); !errors.Is(reserveErr, ErrMissionNotReady) {
		t.Fatalf("reserve a task under review = %v, want ErrMissionNotReady", reserveErr)
	}
	approvedElsewhere := mustCreateMissionTask(t, db, mission.ID, "already approved")
	if _, _, reserveErr := reserveMissionAttempt(t, db, mission, approvedElsewhere, "dispatch-approved"); reserveErr != nil {
		t.Fatalf("reserve an untouched approved task during amendment_review: %v", reserveErr)
	}

	if _, rejectErr := db.DecideMissionPlan(ctx, mission.ID, 2, domain.MissionPlanReject, "no", member, "decide-reject"); !errors.Is(rejectErr, ErrMissionPhase) ||
		!strings.Contains(rejectErr.Error(), "an amendment is approved or sent back for changes; abandon its tasks or revisions to drop it") {
		t.Fatalf("reject an amendment = %v, want the amendment refusal", rejectErr)
	}

	decided, err := db.DecideMissionPlan(ctx, mission.ID, 2, domain.MissionPlanApprove, "", member, "decide-2")
	if err != nil {
		t.Fatalf("DecideMissionPlan on the amendment: %v", err)
	}
	if decided.Phase != domain.MissionPhaseActive {
		t.Fatalf("phase after approving an amendment = %s, want active", decided.Phase)
	}
	if got := revisionStatus(t, db, amended.ID, pending.Revision); got != string(domain.TaskRevisionAccepted) {
		t.Fatalf("amended revision status = %s, want accepted", got)
	}
	if got := revisionStatus(t, db, amended.ID, 1); got != string(domain.TaskRevisionSuperseded) {
		t.Fatalf("previous revision status = %s, want superseded", got)
	}
	projected, err := db.ProjectTask(ctx, amended.ID)
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	if projected.CurrentRevision != pending.Revision || projected.PendingRevision != nil {
		t.Fatalf("approved task = revision %d pending %+v, want revision %d and no pending revision", projected.CurrentRevision, projected.PendingRevision, pending.Revision)
	}
}

func TestMissionAmendmentApprovalAdvancesTheAcceptedSetVersion(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	task := mustApprovedMission(t, db, mission, member, domain.TaskScope{})
	submission := mustSubmitMissionAttempt(t, db, mission, task, "amendment-output")
	mustAcceptMissionSubmission(t, db, mission, submission, "accept-output")
	before := mission.AcceptedSetVersion

	if _, err := db.ProposeTaskRevision(ctx, task.ID, &domain.TaskRevision{Title: "replacement", Objective: "replacement", Material: true, ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2"); err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if _, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "replace the approved work", "submit-2"); err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}
	decided, err := db.DecideMissionPlan(ctx, mission.ID, 2, domain.MissionPlanApprove, "", member, "decide-2")
	if err != nil {
		t.Fatalf("DecideMissionPlan: %v", err)
	}
	if decided.AcceptedSetVersion != before+1 {
		t.Fatalf("accepted_set_version = %d, want %d: the superseded revision held an acceptance", decided.AcceptedSetVersion, before+1)
	}
	stored, err := db.GetMission(ctx, mission.ID)
	if err != nil {
		t.Fatalf("GetMission: %v", err)
	}
	if stored.AcceptedSetVersion != before+1 {
		t.Fatalf("stored accepted_set_version = %d, want %d", stored.AcceptedSetVersion, before+1)
	}
}

func TestMissionAmendmentReviseReturnsToActiveWithProposalsIntact(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	task := mustApprovedMission(t, db, mission, member, domain.TaskScope{})
	pending, err := db.ProposeTaskRevision(ctx, task.ID, &domain.TaskRevision{Title: "wider", Objective: "wider", Material: true, ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if _, submitErr := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "one amendment", "submit-2"); submitErr != nil {
		t.Fatalf("SubmitMissionPlan: %v", submitErr)
	}
	if _, decideErr := db.DecideMissionPlan(ctx, mission.ID, 2, domain.MissionPlanRevise, "narrow it", member, "decide-2"); decideErr != nil {
		t.Fatalf("DecideMissionPlan: %v", decideErr)
	}
	if phase, version := missionPhase(t, db, mission.ID); phase != domain.MissionPhaseActive || version != 2 {
		t.Fatalf("mission = phase %s version %d, want active and 2", phase, version)
	}
	if got := revisionStatus(t, db, task.ID, pending.Revision); got != string(domain.TaskRevisionProposed) {
		t.Fatalf("sent-back revision status = %s, want proposed", got)
	}
	if _, closedErr := db.DecideMissionPlan(ctx, mission.ID, 2, domain.MissionPlanApprove, "", member, "decide-again"); !errors.Is(closedErr, ErrMissionPhase) {
		t.Fatalf("decide a closed round = %v, want ErrMissionPhase", closedErr)
	}

	// Dropping the pending revision leaves the task and its approved revision
	// exactly as they were.
	if abandonErr := db.AbandonTask(ctx, task.ID, pending.Revision, mission.IntegratorGeneration, "abandon-pending"); abandonErr != nil {
		t.Fatalf("AbandonTask revision %d: %v", pending.Revision, abandonErr)
	}
	if got := revisionStatus(t, db, task.ID, pending.Revision); got != string(domain.TaskRevisionAbandoned) {
		t.Fatalf("dropped revision status = %s, want abandoned", got)
	}
	projected, projectErr := db.ProjectTask(ctx, task.ID)
	if projectErr != nil {
		t.Fatalf("ProjectTask: %v", projectErr)
	}
	if projected.AbandonedAt != nil || projected.CurrentRevision != 1 || projected.PendingRevision != nil {
		t.Fatalf("task after dropping a revision = %+v, want the approved revision 1 standing alone", projected)
	}
	if currentErr := db.AbandonTask(ctx, task.ID, 1, mission.IntegratorGeneration, "abandon-current"); !errors.Is(currentErr, ErrConflict) {
		t.Fatalf("drop the current revision = %v, want ErrConflict", currentErr)
	}
}

// TestMissionPreGateDatabaseMigratesToActive pins the upgrade path: a mission
// written before the plan gate existed is already approved work, so it must
// come back active with the new revision columns at their defaults.
func TestMissionPreGateDatabaseMigratesToActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 38)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, environment, created_at)
			VALUES ('w1', 'proj', '{"custom_image":"img","variables":{},"setup_policy":{"script":""}}', 1);
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, max_concurrent_attempts, max_total_attempts, current_integrator_run_id, integrator_generation, accepted_set_version, idempotency_key, created_at, updated_at)
			VALUES ('mi1', 'w1', 'ship it', 'm1', 'm1', 'claude', 'headless', '[]', 1, 2, 'r1', 1, 0, 'key-1', 1, 1);
		INSERT INTO mission_tasks (id, mission_id, current_revision, created_at, updated_at)
			VALUES ('t1', 'mi1', 1, 1, 1);
		INSERT INTO mission_task_revisions (task_id, revision, title, objective, status, created_at)
			VALUES ('t1', 1, 'legacy', 'legacy', 'accepted', 1);
	`, testKey(t, "ada@laptop")); err != nil {
		t.Fatalf("seed v38 rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	mission, err := db.GetMission(context.Background(), "mi1")
	if err != nil {
		t.Fatalf("GetMission: %v", err)
	}
	if mission.Phase != domain.MissionPhaseActive || mission.PlanVersion != 0 {
		t.Fatalf("migrated mission = phase %s plan version %d, want active and 0", mission.Phase, mission.PlanVersion)
	}
	task, err := db.ProjectTask(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	if task.Revision.Material || task.Revision.AcceptedByMemberID != "" || task.Revision.AcceptedByRunID != "" {
		t.Fatalf("migrated revision = %+v, want the new audit columns at their defaults", task.Revision)
	}
	var reviews int
	if err := db.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM mission_plan_items`).Scan(&reviews); err != nil {
		t.Fatalf("read mission_plan_items: %v", err)
	}
	if reviews != 0 {
		t.Fatalf("mission_plan_items = %d rows, want none", reviews)
	}
}

// TestMissionLaunchedMarkerBackfillsExistingIntegrators pins the upgrade: a
// current integrator whose row exists was launched, and one whose row is
// missing was not, so only the second is ever relaunched.
func TestMissionLaunchedMarkerBackfillsExistingIntegrators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, len(migrations)-1)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at, environment, base_branch, steer_others, origin)
			VALUES ('w1', 'proj', 1, '{}', 'main', '', '');
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r1', 'w1', 'm1', 'a', 'claude', 'tui', 'running', 'b', 'w', 1);
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, max_concurrent_attempts, max_total_attempts, current_integrator_run_id, integrator_generation, accepted_set_version, idempotency_key, created_at, updated_at)
			VALUES ('mi1', 'w1', 'launched', 'm1', 'm1', 'claude', 'tui', '[]', 1, 2, 'r1', 1, 0, 'key-1', 1, 1),
			       ('mi2', 'w1', 'reserved', 'm1', 'm1', 'claude', 'tui', '[]', 1, 2, 'r2', 1, 0, 'key-2', 1, 1);
	`, testKey(t, "ada@laptop")); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	for id, want := range map[domain.MissionID]bool{"mi1": true, "mi2": false} {
		m, getErr := db.GetMission(context.Background(), id)
		if getErr != nil {
			t.Fatalf("GetMission %s: %v", id, getErr)
		}
		if m.IntegratorRunLaunched != want {
			t.Fatalf("mission %s integrator_run_launched = %v, want %v", id, m.IntegratorRunLaunched, want)
		}
	}
}
