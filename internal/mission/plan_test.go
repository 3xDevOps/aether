package mission

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

type planGateFixture struct {
	db        *store.DB
	svc       *Service
	mission   *domain.Mission
	workspace *domain.Workspace
	member    *domain.Member
	canceller *recordingCanceller
	launcher  *recordingLauncher
	evidence  *mutableEvidenceReader
	notices   *recordingInjector
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

func newPlanGateFixture(t *testing.T) *planGateFixture {
	t.Helper()
	return newPlanGateFixtureFor(t, "claude", domain.LaunchTUI)
}

// newPlanGateFixtureFor builds the plan gate fixture around an integrator
// run of the given harness and mode.
func newPlanGateFixtureFor(t *testing.T, harnessName string, mode domain.LaunchMode) *planGateFixture {
	t.Helper()
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "accountable")
	m := &domain.Mission{
		WorkspaceID: workspace.ID, Objective: "plan gate mission", AccountableHumanID: member.ID,
		Integrator:            domain.MissionIntegrator{AccountMemberID: member.ID, Harness: harnessName, Mode: mode},
		ExecutionChoices:      []domain.MissionExecutionChoice{{AccountMemberID: member.ID, Harness: "claude", Mode: domain.LaunchHeadless}},
		MaxConcurrentAttempts: 2, MaxTotalAttempts: 4, IdempotencyKey: "plan-gate-mission",
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
	notices := &recordingInjector{writes: make(chan injectedLine, 16)}
	svc, err := New(Config{
		Store: db, Missions: &planShowProbe{MissionStore: db, reads: reads},
		Cancel: canceller, Runs: launcher, Evidence: evidence, AuthorizationMu: &sync.Mutex{},
		RequireCoordination: func() error { return nil }, PTY: notices,
	})
	if err != nil {
		t.Fatalf("new mission service: %v", err)
	}
	return &planGateFixture{
		db: db, svc: svc, mission: m, workspace: workspace, member: member,
		canceller: canceller, launcher: launcher, evidence: evidence, notices: notices, reads: reads,
	}
}

// recordingInjector stands in for the PTY host: it hands every terminal
// write to the test and fails each with err. Notices are delivered in the
// background, so the test receives them rather than reading a slice.
type recordingInjector struct {
	mu     sync.Mutex
	err    error
	writes chan injectedLine
}

type injectedLine struct {
	key                     ptyhost.SessionKey
	actor, color, text, end string
}

func (r *recordingInjector) Inject(_ context.Context, key ptyhost.SessionKey, actor, color, text, submit string) error {
	r.writes <- injectedLine{key: key, actor: actor, color: color, text: text, end: submit}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *recordingInjector) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// expect waits for the next terminal write and requires it to be the
// integrator notice carrying text, ended with submit.
func (f *planGateFixture) expect(t *testing.T, step, text, submit string) {
	t.Helper()
	want := injectedLine{
		key: ptyhost.RunSession(f.mission.CurrentIntegratorRunID), actor: "aether",
		text: "[aether] " + text, end: submit,
	}
	select {
	case got := <-f.notices.writes:
		if got != want {
			t.Fatalf("%s wrote %+v to the integrator terminal, want %+v", step, got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s wrote nothing to the integrator terminal, want %+v", step, want)
	}
}

// expectNone requires that no terminal write arrives within a short window.
// The notice runs in the background, so absence can only be bounded, not
// proven; the window is far longer than a delivery against SQLite takes.
func (f *planGateFixture) expectNone(t *testing.T, step string) {
	t.Helper()
	select {
	case got := <-f.notices.writes:
		t.Fatalf("%s wrote %+v to the integrator terminal, want nothing", step, got)
	case <-time.After(250 * time.Millisecond):
	}
}

func (f *planGateFixture) call(t *testing.T, run domain.RunID, method string, params any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	return f.svc.HandleAgent(context.Background(), run, method, raw)
}

func (f *planGateFixture) mustCall(t *testing.T, method string, params any) any {
	t.Helper()
	out, err := f.call(t, f.mission.CurrentIntegratorRunID, method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func (f *planGateFixture) phaseRefusal(t *testing.T, method string, params any) {
	t.Helper()
	_, err := f.call(t, f.mission.CurrentIntegratorRunID, method, params)
	if !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("%s error = %v, want ErrMissionPhase", method, err)
	}
}

// answerAll answers every open question as the accountable human.
func (f *planGateFixture) answerAll(t *testing.T, keyPrefix string) {
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

// clarify declares clarification complete, which is what the initial plan
// submit needs. Questions are optional, so every round answers first and then
// says it has what it needs.
func (f *planGateFixture) clarify(t *testing.T, round string) {
	t.Helper()
	f.mustCall(t, protocol.MethodMissionClarificationComplete, protocol.MissionClarificationCompleteParams{
		IdempotencyKey: "clarify-" + round,
	})
}

// submit sends the plan and returns the version now awaiting a decision.
func (f *planGateFixture) submit(t *testing.T, round string, wantPhase domain.MissionPhase) uint64 {
	t.Helper()
	out := f.mustCall(t, protocol.MethodMissionPlanSubmit, protocol.MissionPlanSubmitParams{
		Summary: "what will be built and why", IdempotencyKey: "submit-" + round,
	})
	submitted, ok := out.(protocol.MissionPlanSubmitResult)
	if !ok || submitted.Plan.Phase != string(wantPhase) {
		t.Fatalf("mission.plan.submit result = %#v, want %s", out, wantPhase)
	}
	return submitted.Plan.PlanVersion
}

// proposeAndSubmit walks the whole gate up to the human decision and returns
// the submitted plan version.
func (f *planGateFixture) proposeAndSubmit(t *testing.T, round string) uint64 {
	t.Helper()
	f.mustCall(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "ask-" + round,
	})
	f.answerAll(t, "answer-"+round+"-")
	f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "plan task " + round, Objective: "plan task " + round},
		IdempotencyKey: "propose-" + round,
	})
	f.clarify(t, round)
	return f.submit(t, round, domain.MissionPhasePlanReview)
}

func (f *planGateFixture) workerStart(t *testing.T) error {
	t.Helper()
	_, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: "task-does-not-matter", TaskRevision: 1,
		DispatchKey: "dispatch-1", Harness: "claude", Mode: string(domain.LaunchHeadless),
		AccountOwnerID: string(f.member.ID),
	})
	return err
}

// TestPlanGateRefusesDispatchAndAcceptanceBeforeApproval is the whole point of
// the gate: no worker runs and nothing is accepted until a human decides.
func TestPlanGateRefusesDispatchAndAcceptanceBeforeApproval(t *testing.T) {
	f := newPlanGateFixture(t)

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

	f.proposeAndSubmit(t, "1")

	if err := f.workerStart(t); !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("worker.start in plan_review = %v, want ErrMissionPhase", err)
	}
	// The plan is frozen while a human reads it: every task mutation is
	// refused, not only the accepting ones.
	f.phaseRefusal(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "late", Objective: "late"},
		IdempotencyKey: "propose-in-review",
	})
	f.phaseRefusal(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: "task-1", Revision: protocol.TaskRevision{Title: "late", Objective: "late"},
		IdempotencyKey: "revise-in-review",
	})
	f.phaseRefusal(t, protocol.MethodTaskAbandon, protocol.TaskAbandonParams{
		TaskID: "task-1", ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "abandon-in-review",
	})
	f.phaseRefusal(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: "task-1", Revision: 1, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-in-review",
	})
}

func TestPlanApproveActivatesAndReviseReturnsToPlanningWithFeedback(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	version := f.proposeAndSubmit(t, "1")

	revised, err := f.svc.DecidePlan(ctx, f.member.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version,
		Decision: string(domain.MissionPlanRevise), Feedback: "split the migration out",
		IdempotencyKey: "decide-revise-1",
	})
	if err != nil {
		t.Fatalf("revise decision: %v", err)
	}
	if revised.Mission.Phase != string(domain.MissionPhasePlanning) {
		t.Fatalf("phase after revise = %q, want planning", revised.Mission.Phase)
	}
	shown, ok := f.mustCall(t, protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{}).(protocol.MissionPlanShowResult)
	if !ok {
		t.Fatal("mission.plan.show returned the wrong result type")
	}
	if shown.Plan.LatestFeedback != "split the migration out" {
		t.Fatalf("latest_feedback = %q, want the revise feedback", shown.Plan.LatestFeedback)
	}
	if len(shown.Questions) != 1 || len(shown.PlanReviews) != 1 {
		t.Fatalf("mission.plan.show = %d questions and %d reviews, want 1 and 1", len(shown.Questions), len(shown.PlanReviews))
	}

	// A revise round reuses the same tasks, so the second submission needs no
	// new task: the draft the integrator already proposed is still there. It
	// does need clarification again, because revise returned it to planning.
	f.clarify(t, "2")
	f.submit(t, "2", domain.MissionPhasePlanReview)
	approved, err := f.svc.DecidePlan(ctx, f.member.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version + 1,
		Decision: string(domain.MissionPlanApprove), IdempotencyKey: "decide-approve-1",
	})
	if err != nil {
		t.Fatalf("approve decision: %v", err)
	}
	if approved.Mission.Phase != string(domain.MissionPhaseActive) || approved.Mission.PlanVersion != version+1 {
		t.Fatalf("mission after approve = %+v, want active at version %d", approved.Mission, version+1)
	}
	tasks, err := f.db.ListTasks(ctx, f.mission.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Revision == nil || tasks[0].Revision.Status != domain.TaskRevisionAccepted {
		t.Fatalf("plan tasks after approve = %+v, want one accepted revision", tasks)
	}
	// Approval is the only thing the gate held back; dispatch now fails on the
	// task identity it was given, not on the phase.
	if err := f.workerStart(t); errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("worker.start after approve = %v, want a non-phase refusal", err)
	}
}

func TestPlanRejectCancelsIntegratorAndBlocksRelaunch(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	version := f.proposeAndSubmit(t, "1")
	if _, err := f.svc.DecidePlan(ctx, f.member.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version,
		Decision: string(domain.MissionPlanReject), IdempotencyKey: "decide-reject-1",
	}); err != nil {
		t.Fatalf("reject decision: %v", err)
	}
	rejected, err := f.db.GetMission(ctx, f.mission.ID)
	if err != nil {
		t.Fatalf("reload rejected mission: %v", err)
	}
	if rejected.Phase != domain.MissionPhaseRejected {
		t.Fatalf("phase after reject = %q, want rejected", rejected.Phase)
	}

	// Cancellation is the reconcile loop's job and is retried every pass until
	// the run is terminal.
	for range 2 {
		if reconcileErr := f.svc.reconcileMission(ctx, rejected); reconcileErr != nil {
			t.Fatalf("reconcile rejected mission: %v", reconcileErr)
		}
	}
	if len(f.canceller.runs) != 2 || f.canceller.runs[0] != rejected.CurrentIntegratorRunID {
		t.Fatalf("cancelled runs = %v, want the integrator run twice", f.canceller.runs)
	}

	relaunchErr := f.svc.launchRecovered(ctx, MissionLaunchRequest{
		WorkspaceID: rejected.WorkspaceID, MissionID: rejected.ID,
		IntegratorGeneration: rejected.IntegratorGeneration,
		RunID:                rejected.CurrentIntegratorRunID, ActorRunID: rejected.CurrentIntegratorRunID,
		RunOwnerID: rejected.IntegratorRunOwnerID, AccountOwner: rejected.Integrator.AccountMemberID,
		Task: rejected.Objective, Harness: rejected.Integrator.Harness, Mode: rejected.Integrator.Mode,
	})
	if !errors.Is(relaunchErr, store.ErrMissionStale) {
		t.Fatalf("relaunch of a rejected mission = %v, want ErrMissionStale", relaunchErr)
	}

	// Replacing the integrator is the recovery path in every other phase; a
	// rejected mission refuses it too.
	if _, replaceErr := f.svc.ReplaceIntegrator(ctx, f.member.ID, protocol.MissionReplaceIntegratorParams{
		MissionID: string(rejected.ID), ExpectedGeneration: rejected.IntegratorGeneration,
		Integrator:     protocol.MissionIntegrator{AccountMemberID: string(f.member.ID), Harness: "claude", Mode: string(domain.LaunchHeadless)},
		IdempotencyKey: "replace-after-reject",
	}); !errors.Is(replaceErr, store.ErrMissionPhase) {
		t.Fatalf("replace integrator after reject = %v, want ErrMissionPhase", replaceErr)
	}
}

func TestPlanHumanDecisionsRefuseANonAccountableCollaborator(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
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

	f.answerAll(t, "answer-1-")
	f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "plan task", Objective: "plan task"},
		IdempotencyKey: "propose-1",
	})
	f.clarify(t, "1")
	f.submit(t, "1", domain.MissionPhasePlanReview)
	if _, decideErr := f.svc.DecidePlan(ctx, other.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: 1,
		Decision: string(domain.MissionPlanApprove), IdempotencyKey: "decide-foreign",
	}); !errors.Is(decideErr, permissions.ErrDenied) {
		t.Fatalf("foreign decision = %v, want ErrDenied", decideErr)
	}

	// An admin who is not the accountable human decides for them.
	other.Role = domain.RoleAdmin
	if updateErr := f.db.UpdateMember(ctx, other); updateErr != nil {
		t.Fatalf("promote bystander: %v", updateErr)
	}
	if _, decideErr := f.svc.DecidePlan(ctx, other.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: 1,
		Decision: string(domain.MissionPlanApprove), IdempotencyKey: "decide-admin",
	}); decideErr != nil {
		t.Fatalf("admin decision: %v", decideErr)
	}
}

func TestPlanShowWaitReturnsOnAnswerAndRefusesAReplacedIntegrator(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
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
		domain.MissionIntegrator{AccountMemberID: replacement.ID, Harness: "claude", Mode: domain.LaunchHeadless},
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
	f := newPlanGateFixture(t)
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

// TestPlanGateMethodsRefuseAWorkerSocket: the three agent methods belong to
// the integrator's own mission, so a worker run never resolves one.
func TestPlanGateMethodsRefuseAWorkerSocket(t *testing.T) {
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
		protocol.MethodMissionClarificationComplete,
		protocol.MethodMissionPlanShow,
		protocol.MethodMissionPlanSubmit,
	} {
		_, callErr := svc.HandleAgent(ctx, attempts[0].RunID, method, []byte(`{"body":"x","summary":"x","idempotency_key":"worker-attempt"}`))
		if !errors.Is(callErr, store.ErrNotFound) && !errors.Is(callErr, store.ErrMissionStale) {
			t.Fatalf("%s from a worker socket = %v, want a closed refusal", method, callErr)
		}
	}
	questions, err := db.ListMissionQuestions(ctx, mission.ID)
	if err != nil || len(questions) != 1 {
		t.Fatalf("questions after the worker calls = %d (err %v), want only the fixture's own", len(questions), err)
	}
}

func TestPlanRejectStaysAvailableAfterAccountableHumanLosesLaunch(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	admin := regressionMember(t, f.db, "admin")
	admin.Role = domain.RoleAdmin
	if updateErr := f.db.UpdateMember(ctx, admin); updateErr != nil {
		t.Fatalf("promote admin: %v", updateErr)
	}

	f.mustCall(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "ask-1",
	})
	f.answerAll(t, "answer-1-")
	f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "plan task", Objective: "plan task"},
		IdempotencyKey: "propose-1",
	})
	f.clarify(t, "1")
	f.submit(t, "1", domain.MissionPhasePlanReview)

	// The accountable human is demoted while the plan sits in review.
	f.member.Role = domain.RoleViewer
	if updateErr := f.db.UpdateMember(ctx, f.member); updateErr != nil {
		t.Fatalf("demote accountable human: %v", updateErr)
	}
	if _, approveErr := f.svc.DecidePlan(ctx, admin.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: 1,
		Decision: string(domain.MissionPlanApprove), IdempotencyKey: "decide-approve",
	}); !errors.Is(approveErr, permissions.ErrDenied) {
		t.Fatalf("approve without launch admission = %v, want ErrDenied", approveErr)
	}
	if _, rejectErr := f.svc.DecidePlan(ctx, admin.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: 1,
		Decision: string(domain.MissionPlanReject), IdempotencyKey: "decide-reject",
	}); rejectErr != nil {
		t.Fatalf("reject without launch admission: %v", rejectErr)
	}
	m, err := f.db.GetMission(ctx, f.mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Phase != domain.MissionPhaseRejected {
		t.Fatalf("phase = %s, want rejected", m.Phase)
	}
}

// TestPlanHumanActionsNoticeTheIntegratorTerminal covers the wake-up an idle
// interactive integrator depends on: each answer and each plan decision types
// exactly one [aether] line into its terminal, and a replay types none.
func TestPlanHumanActionsNoticeTheIntegratorTerminal(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	submit := harness.SubmitSequence("claude")

	f.mustCall(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "ask-1",
	})
	f.answerAll(t, "answer-1-")
	f.expect(t, "answer", answerNotice, submit)
	questions, err := f.db.ListMissionQuestions(ctx, f.mission.ID)
	if err != nil || len(questions) != 1 {
		t.Fatalf("list questions = %d, %v; want one", len(questions), err)
	}
	if _, replayErr := f.svc.AnswerQuestion(ctx, f.member.ID, protocol.MissionQuestionAnswerParams{
		QuestionID: string(questions[0].ID), Answer: "use the existing flow", IdempotencyKey: "answer-1-" + string(questions[0].ID),
	}); replayErr != nil {
		t.Fatalf("replayed answer: %v", replayErr)
	}
	f.expectNone(t, "replayed answer")
	f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "plan task", Objective: "plan task"},
		IdempotencyKey: "propose-1",
	})
	f.clarify(t, "1")
	version := f.submit(t, "1", domain.MissionPhasePlanReview)

	revise := protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version,
		Decision: string(domain.MissionPlanRevise), Feedback: "split the migration out",
		IdempotencyKey: "decide-revise-1",
	}
	if _, reviseErr := f.svc.DecidePlan(ctx, f.member.ID, revise); reviseErr != nil {
		t.Fatalf("revise decision: %v", reviseErr)
	}
	f.expect(t, "revise", planDecisionNotice(domain.MissionPlanRevise), submit)
	// The retry of a revise whose response was lost lands while the next
	// version awaits review; telling the integrator its plan was sent back
	// again would be false.
	f.clarify(t, "2")
	f.submit(t, "2", domain.MissionPhasePlanReview)
	if _, replayErr := f.svc.DecidePlan(ctx, f.member.ID, revise); replayErr != nil {
		t.Fatalf("replayed revise decision: %v", replayErr)
	}
	f.expectNone(t, "replayed revise")
	if _, approveErr := f.svc.DecidePlan(ctx, f.member.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version + 1,
		Decision: string(domain.MissionPlanApprove), IdempotencyKey: "decide-approve-1",
	}); approveErr != nil {
		t.Fatalf("approve decision: %v", approveErr)
	}
	f.expect(t, "approve", planDecisionNotice(domain.MissionPlanApprove), submit)
	f.expectNone(t, "after approve")
}

// TestPlanNoticeToleratesAMissingTerminal proves a notice attempt that finds no
// live session fails nothing, and that the harness's own submit sequence ends
// the line.
func TestPlanNoticeToleratesAMissingTerminal(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixtureFor(t, "opencode", domain.LaunchTUI)
	f.notices.fail(ptyhost.ErrNoSession)
	version := f.proposeAndSubmit(t, "1")
	f.expect(t, "answer without a live terminal", answerNotice, "\r\r")
	if _, err := f.svc.DecidePlan(ctx, f.member.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version,
		Decision: string(domain.MissionPlanReject), IdempotencyKey: "decide-reject-1",
	}); err != nil {
		t.Fatalf("reject decision without a live terminal: %v", err)
	}
	f.expect(t, "reject without a live terminal", planDecisionNotice(domain.MissionPlanReject), "\r\r")
}

// TestPlanNoticeSkipsAHeadlessIntegrator: a headless harness never reads its
// terminal, so it is not written to at all.
func TestPlanNoticeSkipsAHeadlessIntegrator(t *testing.T) {
	f := newPlanGateFixtureFor(t, "claude", domain.LaunchHeadless)
	version := f.proposeAndSubmit(t, "headless")
	f.expectNone(t, "answer to a headless integrator")
	if _, err := f.svc.DecidePlan(context.Background(), f.member.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version,
		Decision: string(domain.MissionPlanApprove), IdempotencyKey: "decide-approve-1",
	}); err != nil {
		t.Fatalf("approve decision: %v", err)
	}
	f.expectNone(t, "approval to a headless integrator")
}
