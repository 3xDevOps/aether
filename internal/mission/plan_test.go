package mission

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type missionFixture struct {
	db        *store.DB
	svc       *Service
	mission   *domain.Mission
	workspace *domain.Workspace
	member    *domain.Member
	canceller *recordingCanceller
	launcher  *recordingLauncher
	evidence  *mutableEvidenceReader
	// reads carries one signal per question read the service performs, so a
	// test that must land a change after mission.plan.show has taken its
	// snapshot can order the two instead of racing them.
	reads chan struct{}
}

// planShowProbe reports every ListMissionQuestions the mission service runs.
// mission.plan.show reads its snapshot once and then only reports differences,
// so a test answering before that read would wait out the whole deadline.
type planShowProbe struct {
	store.MissionStore
	reads chan struct{}
}

func (p *planShowProbe) ListMissionQuestions(ctx context.Context, missionID domain.MissionID) ([]*domain.MissionQuestion, error) {
	questions, err := p.MissionStore.ListMissionQuestions(ctx, missionID)
	select {
	case p.reads <- struct{}{}:
	default:
	}
	return questions, err
}

func newMissionFixture(t *testing.T) *missionFixture {
	t.Helper()
	return newMissionFixtureFor(t, "claude", domain.LaunchTUI)
}

// newMissionFixtureFor builds a planning mission around an integrator
// run of the given harness and mode.
func newMissionFixtureFor(t *testing.T, harnessName string, mode domain.LaunchMode) *missionFixture {
	t.Helper()
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	m := &domain.Mission{
		WorkspaceID: workspace.ID, Objective: "fixture mission", AccountableHumanID: member.ID,
		Integrator: domain.MissionIntegrator{AccountMemberID: member.ID, Harness: harnessName, Mode: mode},
		ExecutionChoices: []domain.MissionExecutionChoice{
			{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchHeadless},
			{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchTUI},
		},
		MaxConcurrentAttempts: 2, MaxTotalAttempts: 4, IdempotencyKey: "fixture-mission",
		IntegratorAuthorizingHumanID: member.ID, IntegratorRunOwnerID: member.ID,
	}
	if err := db.CreateMission(ctx, m); err != nil {
		t.Fatalf("create mission: %v", err)
	}
	if m.Phase != domain.MissionPhasePlanning {
		t.Fatalf("new mission phase = %q, want planning", m.Phase)
	}
	if err := db.CreateRunWithID(ctx, &domain.Run{
		ID: m.CurrentIntegratorRunID, WorkspaceID: workspace.ID, MemberID: member.ID, Task: "integrator",
		Harness: harnessName, Mode: mode, Status: domain.RunQueued,
	}); err != nil {
		t.Fatalf("create integrator run: %v", err)
	}
	canceller := &recordingCanceller{}
	launcher := &recordingLauncher{db: db}
	evidence := &mutableEvidenceReader{}
	reads := make(chan struct{}, 1)
	svc, err := New(Config{
		Store: db, Missions: &planShowProbe{MissionStore: db, reads: reads},
		Cancel: canceller, Runs: launcher, Evidence: evidence, AuthorizationMu: &sync.Mutex{},
		RequireCoordination: func() error { return nil },
	})
	if err != nil {
		t.Fatalf("new mission service: %v", err)
	}
	return &missionFixture{
		db: db, svc: svc, mission: m, workspace: workspace, member: member,
		canceller: canceller, launcher: launcher, evidence: evidence, reads: reads,
	}
}

func (f *missionFixture) call(t *testing.T, run domain.RunID, method string, params any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	return f.svc.HandleAgent(context.Background(), run, method, raw)
}

func (f *missionFixture) mustCall(t *testing.T, method string, params any) any {
	t.Helper()
	out, err := f.call(t, f.mission.CurrentIntegratorRunID, method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func (f *missionFixture) phaseRefusal(t *testing.T, method string, params any) {
	t.Helper()
	_, err := f.call(t, f.mission.CurrentIntegratorRunID, method, params)
	if !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("%s error = %v, want ErrMissionPhase", method, err)
	}
}

// answerAll answers every open question as the accountable human.
func (f *missionFixture) answerAll(t *testing.T, keyPrefix string) {
	t.Helper()
	questions, err := f.db.ListMissionQuestions(context.Background(), f.mission.ID)
	if err != nil {
		t.Fatalf("list questions: %v", err)
	}
	for _, question := range questions {
		if question.AnsweredAt != nil {
			continue
		}
		if _, answerErr := f.svc.AnswerQuestion(context.Background(), f.member.ID, protocol.MissionQuestionAnswerParams{
			QuestionID: string(question.ID), Answer: "use the existing flow", IdempotencyKey: keyPrefix + string(question.ID),
		}); answerErr != nil {
			t.Fatalf("answer question %s: %v", question.ID, answerErr)
		}
	}
}

// propose proposes one task while planning and returns its ID.
func (f *missionFixture) propose(t *testing.T, key string) string {
	t.Helper()
	out := f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "plan task " + key, Objective: "plan task " + key},
		IdempotencyKey: key,
	})
	return out.(protocol.TaskMutationResult).Task.ID
}

// start calls mission.start and returns the resulting plan state.
func (f *missionFixture) start(t *testing.T, key string) protocol.MissionPlanState {
	t.Helper()
	out := f.mustCall(t, protocol.MethodMissionStart, protocol.MissionStartParams{
		MissionID: string(f.mission.ID), IdempotencyKey: key,
	})
	started, ok := out.(protocol.MissionStartResult)
	if !ok {
		t.Fatalf("mission.start result = %#v, want MissionStartResult", out)
	}
	return started.Plan
}

func (f *missionFixture) workerStart(t *testing.T) error {
	t.Helper()
	_, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: "task-does-not-matter", TaskRevision: 1,
		DispatchKey: "dispatch-1", Harness: "claude", Mode: string(domain.LaunchHeadless),
		AccountOwnerID: string(f.member.ID),
	})
	return err
}

// TestPlanningRefusesDispatchAndAcceptanceUntilStart: no worker runs and
// nothing is accepted until the integrator starts the mission, and start
// needs no human decision.
func TestPlanningRefusesDispatchAndAcceptanceUntilStart(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)

	if err := f.workerStart(t); !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("worker.start in planning = %v, want ErrMissionPhase", err)
	}
	f.phaseRefusal(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: "task-1", Revision: 1, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-in-planning",
	})
	f.phaseRefusal(t, protocol.MethodTaskAcceptSubmission, protocol.TaskAcceptSubmissionParams{
		SubmissionID: "submission-1", ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-submission-in-planning",
	})

	f.mustCall(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "ask-1",
	})
	f.propose(t, "propose-1")
	f.phaseRefusal(t, protocol.MethodMissionStart, protocol.MissionStartParams{
		MissionID: string(f.mission.ID), IdempotencyKey: "start-1",
	})
	f.answerAll(t, "answer-1-")
	if plan := f.start(t, "start-1"); plan.Phase != string(domain.MissionPhaseActive) {
		t.Fatalf("plan after start = %+v, want active", plan)
	}
	tasks, err := f.db.ListTasks(ctx, f.mission.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Revision == nil || tasks[0].Revision.Status != domain.TaskRevisionAccepted ||
		tasks[0].Revision.AcceptedByRunID != f.mission.CurrentIntegratorRunID {
		t.Fatalf("tasks after start = %+v, want one revision accepted by the integrator", tasks)
	}
	// Questions are planning-only.
	f.phaseRefusal(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "one more?", IdempotencyKey: "ask-late",
	})
	// Dispatch now fails on the task identity it was given, not on the phase.
	if err := f.workerStart(t); errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("worker.start after start = %v, want a non-phase refusal", err)
	}
}

func TestStartRefusesAnotherMissionAndAnEmptyPlan(t *testing.T) {
	f := newMissionFixture(t)
	if _, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodMissionStart, protocol.MissionStartParams{
		MissionID: "mission-elsewhere", IdempotencyKey: "start-elsewhere",
	}); !errors.Is(err, store.ErrMissionStale) {
		t.Fatalf("start naming another mission = %v, want ErrMissionStale", err)
	}
	f.phaseRefusal(t, protocol.MethodMissionStart, protocol.MissionStartParams{
		MissionID: string(f.mission.ID), IdempotencyKey: "start-empty",
	})
	if got := f.reloadMission(t).Phase; got != domain.MissionPhasePlanning {
		t.Fatalf("phase after a refused start = %s, want planning", got)
	}
}

func TestQuestionAnswerRefusesANonAccountableCollaborator(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	other := regressionMember(t, f.db, "bystander")

	f.mustCall(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "ask-1",
	})
	questions, err := f.db.ListMissionQuestions(ctx, f.mission.ID)
	if err != nil || len(questions) != 1 {
		t.Fatalf("list questions = %v (err %v), want one", questions, err)
	}
	if _, answerErr := f.svc.AnswerQuestion(ctx, other.ID, protocol.MissionQuestionAnswerParams{
		QuestionID: string(questions[0].ID), Answer: "not yours to answer", IdempotencyKey: "answer-foreign",
	}); !errors.Is(answerErr, permissions.ErrDenied) {
		t.Fatalf("foreign answer = %v, want ErrDenied", answerErr)
	}
	// An admin who is not the accountable human answers for them.
	other.Role = domain.RoleAdmin
	if updateErr := f.db.UpdateMember(ctx, other); updateErr != nil {
		t.Fatalf("promote bystander: %v", updateErr)
	}
	if _, answerErr := f.svc.AnswerQuestion(ctx, other.ID, protocol.MissionQuestionAnswerParams{
		QuestionID: string(questions[0].ID), Answer: "the guest flow", IdempotencyKey: "answer-admin",
	}); answerErr != nil {
		t.Fatalf("admin answer: %v", answerErr)
	}
}

func TestPlanShowWaitReturnsOnAnswerAndRefusesAReplacedIntegrator(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	f.mustCall(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "ask-1",
	})

	type showResult struct {
		out protocol.MissionPlanShowResult
		err error
	}
	waited := make(chan showResult, 1)
	select {
	case <-f.reads:
	default:
	}
	go func() {
		out, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{WaitSeconds: 20})
		shown, _ := out.(protocol.MissionPlanShowResult)
		waited <- showResult{out: shown, err: err}
	}()
	<-f.reads
	f.answerAll(t, "answer-1-")
	select {
	case result := <-waited:
		if result.err != nil {
			t.Fatalf("waiting mission.plan.show: %v", result.err)
		}
		if result.out.Plan.OpenQuestions != 0 {
			t.Fatalf("open_questions after the answer landed = %d, want 0", result.out.Plan.OpenQuestions)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mission.plan.show did not return when the question was answered")
	}

	replacement := regressionMember(t, f.db, "replacement")
	replaced, err := f.db.ReplaceIntegrator(ctx, f.mission.ID, f.mission.IntegratorGeneration,
		domain.MissionIntegrator{AccountMemberID: replacement.ID, Harness: "claude", Mode: domain.LaunchTUI},
		replacement.ID, replacement.ID, "replace-during-planning")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	regressionRun(t, f.db, replaced.CurrentIntegratorRunID, f.workspace.ID, replacement.ID, "replacement integrator")
	if _, staleErr := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodMissionPlanShow,
		protocol.MissionPlanShowParams{WaitSeconds: 1}); !errors.Is(staleErr, store.ErrMissionStale) {
		t.Fatalf("mission.plan.show from the replaced integrator = %v, want ErrMissionStale", staleErr)
	}
}

func TestPlanShowRejectsAnOutOfRangeWait(t *testing.T) {
	f := newMissionFixture(t)
	_, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodMissionPlanShow,
		protocol.MissionPlanShowParams{WaitSeconds: protocol.CoordMaxInboxWaitSeconds + 1})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidParams {
		t.Fatalf("out-of-range wait_seconds = %v, want CodeInvalidParams", err)
	}
	want := "mission.plan.show: wait_seconds must be between 0 and 30"
	if rpcErr.Message != want {
		t.Fatalf("out-of-range wait_seconds message = %q, want %q", rpcErr.Message, want)
	}
}

// TestPlanMethodsRefuseAWorkerSocket: the planning agent methods belong to
// the integrator's own mission, so a worker run never resolves one.
func TestPlanMethodsRefuseAWorkerSocket(t *testing.T) {
	ctx := context.Background()
	db, mission, _, _, _, _ := setupSubmissionRegression(t)
	attempts, err := db.ListAttempts(ctx, mission.ID, "")
	if err != nil || len(attempts) == 0 {
		t.Fatalf("list attempts = %v (err %v), want one", attempts, err)
	}
	svc, err := New(Config{Store: db, Missions: db, AuthorizationMu: &sync.Mutex{}})
	if err != nil {
		t.Fatalf("new mission service: %v", err)
	}
	for _, method := range []string{
		protocol.MethodMissionQuestionAsk,
		protocol.MethodMissionPlanShow,
		protocol.MethodMissionStart,
	} {
		_, callErr := svc.HandleAgent(ctx, attempts[0].RunID, method, []byte(`{"body":"x","mission_id":"`+string(mission.ID)+`","idempotency_key":"worker-attempt"}`))
		if !errors.Is(callErr, store.ErrNotFound) && !errors.Is(callErr, store.ErrMissionStale) {
			t.Fatalf("%s from a worker socket = %v, want a closed refusal", method, callErr)
		}
	}
	questions, err := db.ListMissionQuestions(ctx, mission.ID)
	if err != nil || len(questions) != 1 {
		t.Fatalf("questions after the worker calls = %d (err %v), want only the fixture's own", len(questions), err)
	}
}

// TestStartRequiresTheAccountableHumansLaunchAdmission: start is what lets
// workers dispatch, so an accountable human who lost Launch cannot carry the
// mission forward, while cancel stays available to an admin.
func TestStartRequiresTheAccountableHumansLaunchAdmission(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	admin := regressionMember(t, f.db, "admin")
	admin.Role = domain.RoleAdmin
	if updateErr := f.db.UpdateMember(ctx, admin); updateErr != nil {
		t.Fatalf("promote admin: %v", updateErr)
	}
	f.propose(t, "propose-1")

	f.member.Role = domain.RoleViewer
	if updateErr := f.db.UpdateMember(ctx, f.member); updateErr != nil {
		t.Fatalf("demote accountable human: %v", updateErr)
	}
	if _, startErr := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodMissionStart, protocol.MissionStartParams{
		MissionID: string(f.mission.ID), IdempotencyKey: "start-1",
	}); !errors.Is(startErr, permissions.ErrDenied) {
		t.Fatalf("start without launch admission = %v, want ErrDenied", startErr)
	}
	if _, cancelErr := f.cancel(admin.ID, "cancel-1"); cancelErr != nil {
		t.Fatalf("cancel without launch admission: %v", cancelErr)
	}
	if got := f.reloadMission(t).Phase; got != domain.MissionPhaseCancelled {
		t.Fatalf("phase = %s, want cancelled", got)
	}
}
