package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func planningFixture(t *testing.T) (*DB, *domain.Mission, domain.MemberID) {
	t.Helper()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	return db, mustCreatePlanningMission(t, db, workspace.ID, member.ID, "plan-mission-1"), member.ID
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

// mustStartedMission proposes one task and starts the mission, returning the
// task as accepted work.
func mustStartedMission(t *testing.T, db *DB, m *domain.Mission, scope domain.TaskScope) *domain.Task {
	t.Helper()
	ctx := context.Background()
	task := mustProposeScopedPlanTask(t, db, m, "bounded worker", "task-1", scope)
	if _, err := db.StartMission(ctx, m.ID, m.CurrentIntegratorRunID, "start-1"); err != nil {
		t.Fatalf("StartMission: %v", err)
	}
	m.Phase = domain.MissionPhaseActive
	projected, err := db.ProjectTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	return projected
}

func missionPhase(t *testing.T, db *DB, id domain.MissionID) domain.MissionPhase {
	t.Helper()
	m, err := db.GetMission(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMission: %v", err)
	}
	return m.Phase
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

func TestMissionDraftReviseAdvancesCurrentRevisionAndSupersedes(t *testing.T) {
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
		t.Fatalf("current revision = %d, want 2; mission start would accept a stale draft", projected.CurrentRevision)
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
}

func TestMissionActiveReviseLeavesCurrentRevisionForAcceptance(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	mission := mustCreateMission(t, db, workspace.ID, member.ID)
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

func TestMissionStartAcceptsEveryProposalAndActivates(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	q, err := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "which checkout flow?", "ask-1")
	if err != nil {
		t.Fatalf("InsertMissionQuestion: %v", err)
	}
	kept := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	dropped := mustProposePlanTask(t, db, mission, "second worker", "task-2")
	if _, reviseErr := db.ProposeTaskRevision(ctx, kept.ID, &domain.TaskRevision{Title: "sharper", Objective: "sharper", ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2"); reviseErr != nil {
		t.Fatalf("ProposeTaskRevision: %v", reviseErr)
	}
	if abandonErr := db.AbandonTask(ctx, dropped.ID, 0, mission.IntegratorGeneration, "abandon-1"); abandonErr != nil {
		t.Fatalf("AbandonTask: %v", abandonErr)
	}

	_, err = db.StartMission(ctx, mission.ID, mission.CurrentIntegratorRunID, "start-1")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "1 questions are unanswered") {
		t.Fatalf("start with an open question = %v, want the unanswered-question refusal", err)
	}
	if _, answerErr := db.AnswerMissionQuestion(ctx, q.ID, member, "the guest flow", "answer-1"); answerErr != nil {
		t.Fatalf("AnswerMissionQuestion: %v", answerErr)
	}
	started, err := db.StartMission(ctx, mission.ID, mission.CurrentIntegratorRunID, "start-1")
	if err != nil {
		t.Fatalf("StartMission: %v", err)
	}
	if started.Phase != domain.MissionPhaseActive || missionPhase(t, db, mission.ID) != domain.MissionPhaseActive {
		t.Fatalf("started mission phase = %s, want active", started.Phase)
	}
	if got := revisionStatus(t, db, kept.ID, 2); got != string(domain.TaskRevisionAccepted) {
		t.Fatalf("kept task revision 2 status = %s, want accepted", got)
	}
	if got := revisionStatus(t, db, dropped.ID, 1); got != string(domain.TaskRevisionAbandoned) {
		t.Fatalf("abandoned task status = %s, want abandoned: start must not resurrect it", got)
	}
	var acceptedByRun domain.RunID
	if scanErr := db.db.QueryRowContext(ctx, `SELECT accepted_by_run_id FROM mission_task_revisions WHERE task_id=? AND revision=2`, kept.ID).Scan(&acceptedByRun); scanErr != nil {
		t.Fatalf("read accepted_by_run_id: %v", scanErr)
	}
	if acceptedByRun != mission.CurrentIntegratorRunID {
		t.Fatalf("accepted_by_run_id = %q, want the integrator run %q", acceptedByRun, mission.CurrentIntegratorRunID)
	}
	if replay, replayErr := db.StartMission(ctx, mission.ID, mission.CurrentIntegratorRunID, "start-1"); replayErr != nil || replay.Phase != domain.MissionPhaseActive {
		t.Fatalf("replayed start = %v, %v, want the active mission", replay, replayErr)
	}
	if _, againErr := db.StartMission(ctx, mission.ID, mission.CurrentIntegratorRunID, "start-2"); !errors.Is(againErr, ErrMissionPhase) {
		t.Fatalf("second start = %v, want ErrMissionPhase", againErr)
	}
	if _, askErr := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "late question", "ask-late"); !errors.Is(askErr, ErrMissionPhase) {
		t.Fatalf("ask after start = %v, want ErrMissionPhase: questions are planning-only", askErr)
	}
	current, err := db.ProjectTask(ctx, kept.ID)
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	if _, _, reserveErr := reserveMissionAttempt(t, db, started, current, "dispatch-a"); reserveErr != nil {
		t.Fatalf("ReserveAttempt after start: %v", reserveErr)
	}
}

func TestMissionStartRefusesAnEmptyPlan(t *testing.T) {
	db, mission, _ := planningFixture(t)
	ctx := context.Background()
	_, err := db.StartMission(ctx, mission.ID, mission.CurrentIntegratorRunID, "start-1")
	if !errors.Is(err, ErrMissionPhase) || !strings.Contains(err.Error(), "propose at least one task") {
		t.Fatalf("start with no task = %v, want the empty-plan refusal", err)
	}
	task := mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	if abandonErr := db.AbandonTask(ctx, task.ID, 0, mission.IntegratorGeneration, "abandon-1"); abandonErr != nil {
		t.Fatalf("AbandonTask: %v", abandonErr)
	}
	if _, err = db.StartMission(ctx, mission.ID, mission.CurrentIntegratorRunID, "start-1"); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("start with only an abandoned task = %v, want ErrMissionPhase", err)
	}
	if phase := missionPhase(t, db, mission.ID); phase != domain.MissionPhasePlanning {
		t.Fatalf("phase = %s, want planning after a refused start", phase)
	}
}

// TestMissionActiveAcceptsNewWorkAndChangedScope pins that an active mission
// needs no human round: the integrator accepts new tasks and revisions that
// change scope on its own.
func TestMissionActiveAcceptsNewWorkAndChangedScope(t *testing.T) {
	db, mission, _ := planningFixture(t)
	ctx := context.Background()
	scope := domain.TaskScope{ExpectedPaths: []string{"internal/store"}, Exclusions: []string{"internal/store/migrate.go"}}
	task := mustStartedMission(t, db, mission, scope)

	wider, err := db.ProposeTaskRevision(ctx, task.ID, &domain.TaskRevision{Title: "wider", Objective: "wider", Scope: domain.TaskScope{ExpectedPaths: []string{"internal/store", "internal/server"}}, ProposedByRunID: mission.CurrentIntegratorRunID}, "propose-2")
	if err != nil {
		t.Fatalf("ProposeTaskRevision: %v", err)
	}
	if acceptErr := db.AcceptTaskRevision(ctx, task.ID, wider.Revision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-2"); acceptErr != nil {
		t.Fatalf("accept a revision that widens scope and drops an exclusion: %v", acceptErr)
	}
	if got := revisionStatus(t, db, task.ID, 1); got != string(domain.TaskRevisionSuperseded) {
		t.Fatalf("previous revision status = %s, want superseded", got)
	}
	fresh := mustProposeScopedPlanTask(t, db, mission, "extra worker", "task-2", scope)
	if acceptErr := db.AcceptTaskRevision(ctx, fresh.ID, 1, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-fresh"); acceptErr != nil {
		t.Fatalf("accept a new task while active: %v", acceptErr)
	}
	projected, err := db.ProjectTask(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	if projected.Status != domain.TaskReady || projected.Revision.AcceptedByRunID != mission.CurrentIntegratorRunID {
		t.Fatalf("new task = status %s accepted by %q, want ready and accepted by the integrator", projected.Status, projected.Revision.AcceptedByRunID)
	}
}

func TestMissionCancelEndsAnActiveMission(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	task := mustStartedMission(t, db, mission, domain.TaskScope{})
	if _, _, err := reserveMissionAttempt(t, db, mission, task, "dispatch-a"); err != nil {
		t.Fatalf("ReserveAttempt: %v", err)
	}

	cancelled, err := db.CancelMission(ctx, mission.ID, member, "cancel-1")
	if err != nil {
		t.Fatalf("CancelMission in active: %v", err)
	}
	if cancelled.Phase != domain.MissionPhaseCancelled {
		t.Fatalf("cancelled mission phase = %s, want cancelled", cancelled.Phase)
	}
	if replay, replayErr := db.CancelMission(ctx, mission.ID, member, "cancel-1"); replayErr != nil || replay.Phase != domain.MissionPhaseCancelled {
		t.Fatalf("replayed cancel = %v, %v, want the cancelled mission", replay, replayErr)
	}
	for name, refusal := range map[string]error{
		"task.propose": func() error {
			_, _, e := db.CreateTaskWithIdempotency(ctx, &domain.Task{MissionID: mission.ID, Revision: &domain.TaskRevision{Title: "late", Objective: "late"}}, "task-late")
			return e
		}(),
		"task.accept":  db.AcceptTaskRevision(ctx, task.ID, task.CurrentRevision, mission.IntegratorGeneration, mission.CurrentIntegratorRunID, "accept-late"),
		"task.abandon": db.AbandonTask(ctx, task.ID, 0, mission.IntegratorGeneration, "abandon-late"),
		"worker.start": func() error {
			_, _, e := reserveMissionAttempt(t, db, mission, task, "dispatch-late")
			return e
		}(),
		"mission.replace-integrator": func() error {
			_, e := db.ReplaceIntegrator(ctx, mission.ID, mission.IntegratorGeneration, mission.Integrator, member, member, "replace-1")
			return e
		}(),
		"mission.cancel": func() error {
			_, e := db.CancelMission(ctx, mission.ID, member, "cancel-2")
			return e
		}(),
		"mission complete": func() error {
			_, e := db.CompleteMission(ctx, mission.ID, mission.CurrentIntegratorRunID)
			return e
		}(),
	} {
		if !errors.Is(refusal, ErrMissionPhase) {
			t.Errorf("%s after cancel = %v, want ErrMissionPhase", name, refusal)
		}
	}
}

func TestMissionCancelEndsAPlanningMission(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	mustProposePlanTask(t, db, mission, "bounded worker", "task-1")
	if _, err := db.CancelMission(ctx, mission.ID, member, "cancel-1"); err != nil {
		t.Fatalf("CancelMission in planning: %v", err)
	}
	if _, err := db.StartMission(ctx, mission.ID, mission.CurrentIntegratorRunID, "start-1"); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("start after cancel = %v, want ErrMissionPhase", err)
	}
	if _, err := db.InsertMissionQuestion(ctx, mission.ID, mission.CurrentIntegratorRunID, "late question", "ask-late"); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("ask after cancel = %v, want ErrMissionPhase", err)
	}
}

func TestMissionCompleteIsTheCurrentIntegratorsAndTerminal(t *testing.T) {
	db, mission, member := planningFixture(t)
	ctx := context.Background()
	if _, err := db.CompleteMission(ctx, mission.ID, mission.CurrentIntegratorRunID); !errors.Is(err, ErrMissionPhase) {
		t.Fatalf("complete a planning mission = %v, want ErrMissionPhase", err)
	}
	task := mustStartedMission(t, db, mission, domain.TaskScope{})

	if _, err := db.CompleteMission(ctx, mission.ID, "some-other-run"); !errors.Is(err, ErrMissionStale) {
		t.Fatalf("complete by a run that is not the integrator = %v, want ErrMissionStale", err)
	}
	completed, err := db.CompleteMission(ctx, mission.ID, mission.CurrentIntegratorRunID)
	if err != nil {
		t.Fatalf("CompleteMission: %v", err)
	}
	if completed.Phase != domain.MissionPhaseCompleted || missionPhase(t, db, mission.ID) != domain.MissionPhaseCompleted {
		t.Fatalf("completed mission phase = %s, want completed", completed.Phase)
	}
	if replay, replayErr := db.CompleteMission(ctx, mission.ID, mission.CurrentIntegratorRunID); replayErr != nil || replay.Phase != domain.MissionPhaseCompleted {
		t.Fatalf("completing again = %v, %v, want a no-op", replay, replayErr)
	}
	if _, cancelErr := db.CancelMission(ctx, mission.ID, member, "cancel-1"); !errors.Is(cancelErr, ErrMissionPhase) {
		t.Fatalf("cancel a completed mission = %v, want ErrMissionPhase", cancelErr)
	}
	if _, _, reserveErr := reserveMissionAttempt(t, db, mission, task, "dispatch-late"); !errors.Is(reserveErr, ErrMissionPhase) {
		t.Fatalf("dispatch after completion = %v, want ErrMissionPhase", reserveErr)
	}
}

func TestListMissionsPageReportsOpenQuestionsPerMission(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	first := mustCreatePlanningMission(t, db, workspace.ID, member.ID, "plan-mission-1")
	second := mustCreatePlanningMission(t, db, workspace.ID, member.ID, "plan-mission-2")

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

// TestMissionDatabaseMigratesToTheNewPhases pins the upgrade that
// drops the human plan gate: every old phase maps onto planning, active or
// cancelled, the plan round tables and the material column are gone, and a
// mission that waited on a plan decision can be started by its integrator.
func TestMissionDatabaseMigratesToTheNewPhases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 48)
	phases := map[string]domain.MissionPhase{
		"planning": domain.MissionPhasePlanning, "clarified": domain.MissionPhasePlanning,
		"plan_review": domain.MissionPhasePlanning, "active": domain.MissionPhaseActive,
		"amendment_review": domain.MissionPhaseActive, "rejected": domain.MissionPhaseCancelled,
	}
	seed := `INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at, environment, base_branch, steer_others, origin)
			VALUES ('w1', 'proj', 1, '{}', 'main', '', '');`
	for old := range phases {
		seed += fmt.Sprintf(`
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, max_concurrent_attempts, max_total_attempts, current_integrator_run_id, integrator_generation, accepted_set_version, idempotency_key, created_at, updated_at, phase, plan_version)
			VALUES ('%[1]s', 'w1', 'ship it', 'm1', 'm1', 'claude', 'tui', '[]', 1, 2, 'run-%[1]s', 1, 0, 'key-%[1]s', 1, 1, '%[1]s', 1);`, old)
	}
	seed += `
		INSERT INTO mission_create_receipts (workspace_id, idempotency_key, mission_id, objective, accountable_human_id, integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, max_concurrent_attempts, max_total_attempts, created_at)
			VALUES ('w1', 'key-active', 'active', 'ship it', 'm1', 'm1', 'claude', 'tui', '[]', 1, 2, 1);
		INSERT INTO mission_tasks (id, mission_id, current_revision, created_at, updated_at)
			VALUES ('t1', 'plan_review', 1, 1, 1);
		INSERT INTO mission_task_revisions (task_id, revision, title, objective, status, material, created_at)
			VALUES ('t1', 1, 'reviewed', 'reviewed', 'proposed', 1, 1);
		INSERT INTO mission_plan_reviews (mission_id, plan_version, summary, submitted_by_run_id, submitted_at, submitted_phase)
			VALUES ('plan_review', 1, 'the plan', 'run-plan_review', 1, 'clarified');
		INSERT INTO mission_plan_items (mission_id, plan_version, task_id, revision, new_task, material)
			VALUES ('plan_review', 1, 't1', 1, 1, 1);`
	if _, err := raw.Exec(seed, testKey(t, "ada@laptop")); err != nil {
		t.Fatalf("seed v48 rows: %v", err)
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
	ctx := context.Background()
	for old, want := range phases {
		if got := missionPhase(t, db, domain.MissionID(old)); got != want {
			t.Errorf("mission migrated from %s has phase %s, want %s", old, got, want)
		}
	}
	for _, table := range []string{"mission_plan_reviews", "mission_plan_items"} {
		var n int
		if scanErr := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); scanErr != nil || n != 0 {
			t.Fatalf("table %s after migration = %d, %v, want dropped", table, n, scanErr)
		}
	}
	for _, table := range []string{"missions", "mission_create_receipts"} {
		var limits, rows int
		if scanErr := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name IN ('max_concurrent_attempts','max_total_attempts')`, table).Scan(&limits); scanErr != nil || limits != 0 {
			t.Fatalf("limit columns on %s after migration = %d, %v, want dropped", table, limits, scanErr)
		}
		if scanErr := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); scanErr != nil || rows == 0 {
			t.Fatalf("rows in %s after migration = %d, %v, want kept", table, rows, scanErr)
		}
	}
	var material int
	if scanErr := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('mission_task_revisions') WHERE name='material'`).Scan(&material); scanErr != nil || material != 0 {
		t.Fatalf("material column after migration = %d, %v, want dropped", material, scanErr)
	}
	if _, writeErr := db.db.ExecContext(ctx, `UPDATE missions SET phase='plan_review' WHERE id='planning'`); writeErr == nil {
		t.Fatal("a retired phase was written, want the phase CHECK to refuse it")
	}
	started, err := db.StartMission(ctx, "plan_review", "run-plan_review", "start-1")
	if err != nil {
		t.Fatalf("StartMission on a mission migrated from plan_review: %v", err)
	}
	if started.Phase != domain.MissionPhaseActive || revisionStatus(t, db, "t1", 1) != string(domain.TaskRevisionAccepted) {
		t.Fatalf("started migrated mission = phase %s, task status %s, want active and accepted", started.Phase, revisionStatus(t, db, "t1", 1))
	}
}

// TestMissionPreGateDatabaseMigratesToActive pins the upgrade path: a mission
// written before mission phases existed is already started work, so it must
// come back active with the revision audit columns at their defaults.
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
	if mission.Phase != domain.MissionPhaseActive {
		t.Fatalf("migrated mission phase = %s, want active", mission.Phase)
	}
	task, err := db.ProjectTask(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ProjectTask: %v", err)
	}
	if task.Revision.AcceptedByMemberID != "" || task.Revision.AcceptedByRunID != "" {
		t.Fatalf("migrated revision = %+v, want the audit columns at their defaults", task.Revision)
	}
}

// TestMissionLaunchedMarkerBackfillsExistingIntegrators pins the upgrade: every
// existing integrator counts as launched, whether or not its row survives, so
// an upgrade relaunches nothing a human deleted.
func TestMissionLaunchedMarkerBackfillsExistingIntegrators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aether.db")
	raw := openLegacy(t, path, 41)
	if _, err := raw.Exec(`
		INSERT INTO members (id, display_name, public_key, color, role, created_at)
			VALUES ('m1', 'Ada', ?, '#e6194b', 'admin', 1);
		INSERT INTO workspaces (id, name, created_at, environment, base_branch, steer_others, origin)
			VALUES ('w1', 'proj', 1, '{}', 'main', '', '');
		INSERT INTO runs (id, workspace_id, member_id, task, harness, mode, status, branch, worktree, created_at)
			VALUES ('r1', 'w1', 'm1', 'a', 'claude', 'tui', 'running', 'b', 'w', 1);
		INSERT INTO missions (id, workspace_id, objective, accountable_human_id, integrator_account_member_id, integrator_harness, integrator_mode, execution_choices, max_concurrent_attempts, max_total_attempts, current_integrator_run_id, integrator_generation, accepted_set_version, idempotency_key, created_at, updated_at)
			VALUES ('mi1', 'w1', 'launched', 'm1', 'm1', 'claude', 'tui', '[]', 1, 2, 'r1', 1, 0, 'key-1', 1, 1),
			       ('mi2', 'w1', 'deleted', 'm1', 'm1', 'claude', 'tui', '[]', 1, 2, 'r2', 1, 0, 'key-2', 1, 1),
			       ('mi3', 'w1', 'no integrator', 'm1', 'm1', 'claude', 'tui', '[]', 1, 2, NULL, 1, 0, 'key-3', 1, 1);
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
	for id, want := range map[domain.MissionID]bool{"mi1": true, "mi2": true, "mi3": false} {
		m, getErr := db.GetMission(context.Background(), id)
		if getErr != nil {
			t.Fatalf("GetMission %s: %v", id, getErr)
		}
		if m.IntegratorRunLaunched != want {
			t.Fatalf("mission %s integrator_run_launched = %v, want %v", id, m.IntegratorRunLaunched, want)
		}
	}
}
