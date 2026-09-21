package store

import (
	"context"
	"errors"
	"fmt"
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
	task := &domain.Task{MissionID: m.ID, Revision: &domain.TaskRevision{Title: title, Objective: title, ProposedByRunID: m.CurrentIntegratorRunID}}
	created, _, err := db.CreateTaskWithIdempotency(context.Background(), task, key)
	if err != nil {
		t.Fatalf("CreateTaskWithIdempotency: %v", err)
	}
	return created
}

// mustAskAndAnswer satisfies the clarify gate: one question, one answer.
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

// mustSubmittedPlan drives a fresh mission all the way to plan_review.
func mustSubmittedPlan(t *testing.T, db *DB, m *domain.Mission, member domain.MemberID) *domain.MissionPlanReview {
	t.Helper()
	mustAskAndAnswer(t, db, m, member)
	mustProposePlanTask(t, db, m, "bounded worker", "task-1")
	review, err := db.SubmitMissionPlan(context.Background(), m.ID, m.CurrentIntegratorRunID, "what will be built and why", "submit-1")
	if err != nil {
		t.Fatalf("SubmitMissionPlan: %v", err)
	}
	return review
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
	if err := db.AcceptTaskRevision(ctx, task.ID, task.CurrentRevision, mission.IntegratorGeneration, "accept-1"); !errors.Is(err, ErrMissionPhase) {
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
	if err := db.AbandonTask(ctx, task.ID, mission.IntegratorGeneration, "abandon-1"); err != nil {
		t.Fatalf("AbandonTask in planning: %v", err)
	}
}

func TestMissionPhaseFreezesEveryTaskMutationDuringPlanReview(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	mustAskAndAnswer(t, db, mission, member)
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
		"task.accept":  db.AcceptTaskRevision(ctx, task.ID, task.CurrentRevision, mission.IntegratorGeneration, "accept-1"),
		"task.abandon": db.AbandonTask(ctx, task.ID, mission.IntegratorGeneration, "abandon-1"),
		"worker.start": func() error {
			_, _, e := reserveMissionAttempt(t, db, mission, task, "dispatch-a")
			return e
		}(),
		"mission.question.ask": func() error {
			_, e := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "late question", "ask-late")
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

func TestMissionPlanningReviseAdvancesCurrentRevisionAndSupersedes(t *testing.T) {
	db, mission, _ := planningFixture(t)
	ctx := context.Background()
	task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")

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
	if got := revisionStatus(t, db, task.ID, 1); got != string(domain.TaskRevisionSuperseded) {
		t.Fatalf("revision 1 status = %s, want superseded", got)
	}
	if got := revisionStatus(t, db, task.ID, 2); got != string(domain.TaskRevisionProposed) {
		t.Fatalf("revision 2 status = %s, want proposed", got)
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

func TestMissionPlanSubmitRequiresAConsultedHumanAndAProposedTask(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()

	_, err := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "ask at least one question") {
		t.Fatalf("submit with no question = %v, want the unconsulted-human refusal", err)
	}
	q, err := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "which checkout flow?", "ask-1")
	if err != nil {
		t.Fatalf("InsertMissionQuestion: %v", err)
	}
	_, err = db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "1 questions are unanswered") {
		t.Fatalf("submit with an open question = %v, want the unanswered-question refusal", err)
	}
	if _, answerErr := db.AnswerMissionQuestion(ctx, q.ID, member, "the guest flow", "answer-1"); answerErr != nil {
		t.Fatalf("AnswerMissionQuestion: %v", answerErr)
	}
	_, err = db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "plan one", "submit-1")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "propose at least one task") {
		t.Fatalf("submit with no task = %v, want the no-task refusal", err)
	}

	mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
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
	// A fresh key opens the next round from planning.
	next, nextErr := db.SubmitMissionPlan(ctx, mission.ID, mission.CurrentIntegratorRunID, "a narrower plan", "submit-2")
	if nextErr != nil || next.PlanVersion != 2 {
		t.Fatalf("second submit = %v, %v, want plan version 2", next, nextErr)
	}
}

func TestMissionPlanDecideApproveAcceptsExactlyTheProposedRevisions(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustAskAndAnswer(t, db, mission, member)
	kept := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	dropped := mustProposePlanTask(t, db, mission, "second worker", "task-2")
	if _, err := db.ProposeTaskRevision(ctx, kept.ID, &domain.TaskRevision{Title: "sharper", Objective: "sharper", ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2"); err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if err := db.AbandonTask(ctx, dropped.ID, mission.IntegratorGeneration, "abandon-1"); err != nil {
		t.Fatalf("AbandonTask: %v", err)
	}
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

func TestMissionPlanApproveRefusesAnEmptyPlan(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustSubmittedPlan(t, db, mission, member)
	// plan_review admits no task mutation, so an empty plan can only arrive
	// through a write this transaction did not make. The guard still has to
	// hold: approving nothing would activate a mission with no work.
	if _, err := db.db.ExecContext(ctx, `UPDATE mission_tasks SET abandoned_at=1 WHERE mission_id=?`, mission.ID); err != nil {
		t.Fatalf("abandon every task: %v", err)
	}
	_, err := db.DecideMissionPlan(ctx, mission.ID, 1, domain.MissionPlanApprove, "", member, "decide-1")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "the plan has no proposed task to accept; request changes to return the mission to planning") {
		t.Fatalf("approve with no proposed task = %v, want the empty-plan refusal", err)
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

	var wg sync.WaitGroup
	wg.Add(2)
	var abandonErr, submitErr error
	go func() {
		defer wg.Done()
		abandonErr = db.AbandonTask(ctx, task.ID, mission.IntegratorGeneration, "abandon-1")
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
	// The abandon won: the plan has no proposed task left to submit.
	if abandonErr != nil {
		t.Fatalf("neither side succeeded: abandon %v, submit %v", abandonErr, submitErr)
	}
	if !errors.Is(submitErr, ErrMissionPhase) || !strings.Contains(submitErr.Error(), "propose at least one task") {
		t.Fatalf("submit = %v, want the no-task refusal", submitErr)
	}
	if phase != domain.MissionPhasePlanning {
		t.Fatalf("phase = %s, want planning after a refused submit", phase)
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
