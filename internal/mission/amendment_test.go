package mission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// recordingLauncher persists the run the attempt already reserved, which is
// what the scheduler seam must do: the durable RunID is never re-issued.
type recordingLauncher struct {
	db *store.DB
	mu sync.Mutex
}

func (l *recordingLauncher) LaunchMission(ctx context.Context, req MissionLaunchRequest) (*domain.Run, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := &domain.Run{
		ID: req.RunID, WorkspaceID: req.WorkspaceID, MemberID: req.RunOwnerID, Task: req.Task,
		Harness: req.Harness, Mode: req.Mode, Status: domain.RunQueued,
	}
	if err := l.db.CreateRunWithID(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

type taskSpec struct {
	key        string
	title      string
	paths      []string
	exclusions []string
}

// activate walks the initial gate - propose, clarify, submit, approve - and
// returns the created tasks in the order given, reloaded from the store so
// their current revision is the approved one.
func (f *planGateFixture) activate(t *testing.T, specs ...taskSpec) []*domain.Task {
	t.Helper()
	ctx := context.Background()
	ids := make([]domain.TaskID, 0, len(specs))
	for _, spec := range specs {
		out := f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
			MissionID: string(f.mission.ID),
			Revision: protocol.TaskRevision{Title: spec.title, Objective: spec.title, Scope: protocol.TaskScope{
				ExpectedPaths: spec.paths, Exclusions: spec.exclusions,
			}},
			IdempotencyKey: spec.key,
		})
		mutation, ok := out.(protocol.TaskMutationResult)
		if !ok || mutation.Task.ID == "" {
			t.Fatalf("task.propose result = %#v, want a task", out)
		}
		ids = append(ids, domain.TaskID(mutation.Task.ID))
	}
	f.clarify(t, "1")
	f.decide(t, f.submit(t, "1", domain.MissionPhasePlanReview), domain.MissionPlanApprove, "", "decide-approve-1")
	tasks := make([]*domain.Task, 0, len(ids))
	for _, id := range ids {
		task, err := f.db.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("reload approved task %s: %v", id, err)
		}
		if task.Revision == nil || task.Revision.Status != domain.TaskRevisionAccepted {
			t.Fatalf("task %s after approve = %+v, want an accepted revision", id, task.Revision)
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func (f *planGateFixture) decide(t *testing.T, version uint64, decision domain.MissionPlanDecision, feedback, key string) protocol.Mission {
	t.Helper()
	out, err := f.svc.DecidePlan(context.Background(), f.member.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version,
		Decision: string(decision), Feedback: feedback, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("mission.plan.decide %s: %v", decision, err)
	}
	return out.Mission
}

// amend proposes one revision of an approved task and submits it as the
// mission's first amendment, which is the only way a plan change reaches an
// active mission.
func (f *planGateFixture) amend(t *testing.T, task *domain.Task, revision protocol.TaskRevision) uint64 {
	t.Helper()
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(task.ID), Revision: revision, IdempotencyKey: "revise-2",
	})
	return f.submit(t, "2", domain.MissionPhaseAmendmentReview)
}

func (f *planGateFixture) startWorker(t *testing.T, task *domain.Task, dispatchKey string) error {
	t.Helper()
	current, err := f.db.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("reload task %s: %v", task.ID, err)
	}
	_, startErr := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: string(f.mission.ID), TaskID: string(current.ID), TaskRevision: current.CurrentRevision,
		DispatchKey: dispatchKey, Harness: "claude", Mode: string(domain.LaunchHeadless),
		AccountOwnerID: string(f.member.ID),
	})
	return startErr
}

func (f *planGateFixture) reloadMission(t *testing.T) *domain.Mission {
	t.Helper()
	m, err := f.db.GetMission(context.Background(), f.mission.ID)
	if err != nil {
		t.Fatalf("reload mission: %v", err)
	}
	return m
}

// TestClarificationCompleteOpensSubmissionWithoutAnyQuestion: asking is the
// integrator's judgement, declaring clarification finished is not.
func TestClarificationCompleteOpensSubmissionWithoutAnyQuestion(t *testing.T) {
	f := newPlanGateFixture(t)
	f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "unasked task", Objective: "unasked task"},
		IdempotencyKey: "propose-1",
	})
	// Submitting before the declaration is the one thing the phase forbids.
	f.phaseRefusal(t, protocol.MethodMissionPlanSubmit, protocol.MissionPlanSubmitParams{
		Summary: "too early", IdempotencyKey: "submit-early",
	})

	out := f.mustCall(t, protocol.MethodMissionClarificationComplete, protocol.MissionClarificationCompleteParams{
		IdempotencyKey: "clarify-1",
	})
	completed, ok := out.(protocol.MissionClarificationCompleteResult)
	if !ok || completed.Plan.Phase != string(domain.MissionPhaseClarified) {
		t.Fatalf("mission.clarification.complete result = %#v, want clarified", out)
	}
	if len(f.mustCall(t, protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{}).(protocol.MissionPlanShowResult).Questions) != 0 {
		t.Fatal("clarification completed with a question nobody asked")
	}

	// A task may still be shaped in clarified; only accepting is post-approval.
	f.mustCall(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "second task", Objective: "second task"},
		IdempotencyKey: "propose-2",
	})
	f.phaseRefusal(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: "task-1", Revision: 1, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-in-clarified",
	})
	f.decide(t, f.submit(t, "1", domain.MissionPhasePlanReview), domain.MissionPlanApprove, "", "decide-approve-1")
	if m := f.reloadMission(t); m.Phase != domain.MissionPhaseActive {
		t.Fatalf("phase after approve = %q, want active", m.Phase)
	}
}

// TestQuestionInClarifiedReturnsTheMissionToPlanning: clarified is not a
// one-way door, so a follow-up question reopens it.
func TestQuestionInClarifiedReturnsTheMissionToPlanning(t *testing.T) {
	f := newPlanGateFixture(t)
	f.clarify(t, "1")
	f.mustCall(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "ask-late",
	})
	if m := f.reloadMission(t); m.Phase != domain.MissionPhasePlanning {
		t.Fatalf("phase after a follow-up question = %q, want planning", m.Phase)
	}
	// The unanswered question now blocks the next declaration.
	_, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodMissionClarificationComplete,
		protocol.MissionClarificationCompleteParams{IdempotencyKey: "clarify-2"})
	if !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("clarification with an unanswered question = %v, want ErrMissionPhase", err)
	}
	f.answerAll(t, "answer-late-")
	f.clarify(t, "2")
	if m := f.reloadMission(t); m.Phase != domain.MissionPhaseClarified {
		t.Fatalf("phase after answering and re-declaring = %q, want clarified", m.Phase)
	}
}

// TestAmendmentFreezesPlanChangesAndHoldsBackOnlyTheAmendedTask is the whole
// point of the amendment round: approved work keeps running, the amended task
// waits for the human, and the plan itself cannot move under the reader.
func TestAmendmentFreezesPlanChangesAndHoldsBackOnlyTheAmendedTask(t *testing.T) {
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}},
		taskSpec{key: "propose-b", title: "task b", paths: []string{"internal/b"}})
	amended, untouched := tasks[0], tasks[1]

	version := f.amend(t, amended, protocol.TaskRevision{
		Title: "task a widened", Objective: "task a widened", Material: true,
		Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a", "internal/c"}},
	})

	f.phaseRefusal(t, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      string(f.mission.ID),
		Revision:       protocol.TaskRevision{Title: "late", Objective: "late"},
		IdempotencyKey: "propose-in-amendment",
	})
	f.phaseRefusal(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(untouched.ID), Revision: protocol.TaskRevision{Title: "late", Objective: "late"},
		IdempotencyKey: "revise-in-amendment",
	})
	f.phaseRefusal(t, protocol.MethodTaskAbandon, protocol.TaskAbandonParams{
		TaskID: string(untouched.ID), ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "abandon-in-amendment",
	})
	f.phaseRefusal(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(amended.ID), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-in-amendment",
	})
	f.phaseRefusal(t, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "may I?", IdempotencyKey: "ask-in-amendment",
	})
	f.phaseRefusal(t, protocol.MethodMissionClarificationComplete, protocol.MissionClarificationCompleteParams{
		IdempotencyKey: "clarify-in-amendment",
	})

	if err := f.startWorker(t, untouched, "dispatch-untouched"); err != nil {
		t.Fatalf("worker.start on an approved task during an amendment: %v", err)
	}
	if err := f.startWorker(t, amended, "dispatch-amended"); !errors.Is(err, store.ErrMissionNotReady) {
		t.Fatalf("worker.start on the amended task = %v, want ErrMissionNotReady", err)
	}

	approved := f.decide(t, version, domain.MissionPlanApprove, "", "decide-approve-2")
	if approved.Phase != string(domain.MissionPhaseActive) {
		t.Fatalf("phase after approving the amendment = %q, want active", approved.Phase)
	}
	current, err := f.db.GetTask(context.Background(), amended.ID)
	if err != nil {
		t.Fatalf("reload amended task: %v", err)
	}
	if current.CurrentRevision != 2 || current.Revision == nil || current.Revision.Status != domain.TaskRevisionAccepted {
		t.Fatalf("amended task after approve = %+v at revision %d, want accepted revision 2", current.Revision, current.CurrentRevision)
	}
	if current.PendingRevision != nil {
		t.Fatalf("amended task keeps pending revision %+v after approve", current.PendingRevision)
	}
	if current.Revision.AcceptedByMemberID != f.member.ID {
		t.Fatalf("accepted_by_member_id = %q, want the deciding human %q", current.Revision.AcceptedByMemberID, f.member.ID)
	}
}

// TestAmendmentRejectIsRefusedAndReviseKeepsTheMissionActive: an amendment
// leaves the approved plan standing, so there is nothing to reject.
func TestAmendmentRejectIsRefusedAndReviseKeepsTheMissionActive(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	version := f.amend(t, tasks[0], protocol.TaskRevision{
		Title: "task a again", Objective: "task a again", Material: true,
		Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a"}},
	})

	_, rejectErr := f.svc.DecidePlan(ctx, f.member.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version,
		Decision: string(domain.MissionPlanReject), IdempotencyKey: "decide-reject-2",
	})
	if !errors.Is(rejectErr, store.ErrMissionPhase) {
		t.Fatalf("reject on an amendment = %v, want ErrMissionPhase", rejectErr)
	}

	revised := f.decide(t, version, domain.MissionPlanRevise, "narrow it down", "decide-revise-2")
	if revised.Phase != string(domain.MissionPhaseActive) {
		t.Fatalf("phase after sending an amendment back = %q, want active", revised.Phase)
	}
	current, err := f.db.GetTask(ctx, tasks[0].ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if current.CurrentRevision != 1 || current.PendingRevision == nil || current.PendingRevision.Revision != 2 {
		t.Fatalf("task after a sent-back amendment = revision %d with pending %+v, want revision 1 with pending 2", current.CurrentRevision, current.PendingRevision)
	}
	// A revision a human declined is not the integrator's to accept alone.
	_, acceptErr := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(current.ID), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-sent-back",
	})
	if !errors.Is(acceptErr, store.ErrMissionAmendmentRequired) {
		t.Fatalf("self-accepting a sent-back revision = %v, want ErrMissionAmendmentRequired", acceptErr)
	}
	// Dropping it is the documented way out, and it leaves the task alone.
	f.mustCall(t, protocol.MethodTaskAbandon, protocol.TaskAbandonParams{
		TaskID: string(current.ID), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "abandon-revision-2",
	})
	dropped, err := f.db.GetTask(ctx, current.ID)
	if err != nil {
		t.Fatalf("reload task after dropping the revision: %v", err)
	}
	if dropped.PendingRevision != nil || dropped.CurrentRevision != 1 || dropped.Status == domain.TaskAbandoned {
		t.Fatalf("task after dropping one revision = %+v, want the approved task intact", dropped)
	}
}

// TestSelfAcceptanceRefusesAMaterialRevisionInActive: the integrator keeps
// accepting revisions that stay inside what the human approved, and nothing
// else.
func TestSelfAcceptanceRefusesAMaterialRevisionInActive(t *testing.T) {
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(tasks[0].ID), IdempotencyKey: "revise-material",
		Revision: protocol.TaskRevision{Title: "material", Objective: "material", Material: true,
			Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a"}}},
	})
	_, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(tasks[0].ID), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-material",
	})
	if !errors.Is(err, store.ErrMissionAmendmentRequired) {
		t.Fatalf("self-accepting a material revision = %v, want ErrMissionAmendmentRequired", err)
	}
	// A revision inside the approved scope that declares nothing material is
	// still the integrator's own call.
	f.mustCall(t, protocol.MethodTaskAbandon, protocol.TaskAbandonParams{
		TaskID: string(tasks[0].ID), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "abandon-material",
	})
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(tasks[0].ID), IdempotencyKey: "revise-inside",
		Revision: protocol.TaskRevision{Title: "inside", Objective: "inside",
			Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a"}}},
	})
	f.mustCall(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(tasks[0].ID), Revision: 3, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		IdempotencyKey: "accept-inside",
	})
	current, err := f.db.GetTask(context.Background(), tasks[0].ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if current.CurrentRevision != 3 || current.Revision.AcceptedByRunID != f.mission.CurrentIntegratorRunID {
		t.Fatalf("self-accepted task = revision %d by %q, want revision 3 by the integrator run", current.CurrentRevision, current.Revision.AcceptedByRunID)
	}
}

// TestSubmissionAcceptanceSurvivesAnAmendmentRound: finishing approved work
// is not a plan change. The service phase check is the only gate on
// task.submission.accept, so it is asserted from the wire.
func TestSubmissionAcceptanceSurvivesAnAmendmentRound(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}},
		taskSpec{key: "propose-b", title: "task b", paths: []string{"internal/b"}})
	worked, amended := tasks[0], tasks[1]

	if err := f.startWorker(t, worked, "dispatch-worked"); err != nil {
		t.Fatalf("worker.start: %v", err)
	}
	attempts, err := f.db.ListAttempts(ctx, f.mission.ID, worked.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %v (err %v), want one", attempts, err)
	}
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	f.evidence.packet = protocol.EvidencePacket{
		ID: "packet-1", WorkspaceID: string(f.workspace.ID), RunID: string(attempts[0].RunID),
		Availability: protocol.EvidenceAvailable, RetainedRevision: "revision-1", ExpiresAt: &expires,
	}
	submission, err := f.db.SubmitAttempt(ctx, attempts[0].ID, f.mission.IntegratorGeneration, f.mission.IntegratorGeneration,
		domain.SubmissionRef{WorkspaceID: f.workspace.ID, RunID: attempts[0].RunID, EvidenceRef: "packet-1", RetainedRevision: "revision-1"},
		[]domain.SubmissionEvidence{{Kind: "retained_packet", Ref: "packet-1", Available: true}}, nil)
	if err != nil {
		t.Fatalf("submit attempt: %v", err)
	}

	f.amend(t, amended, protocol.TaskRevision{
		Title: "task b widened", Objective: "task b widened", Material: true,
		Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/b", "internal/d"}},
	})

	m := f.reloadMission(t)
	f.mustCall(t, protocol.MethodTaskAcceptSubmission, protocol.TaskAcceptSubmissionParams{
		SubmissionID: string(submission.ID), ExpectedIntegratorGeneration: m.IntegratorGeneration,
		ExpectedAcceptedSetVersion: m.AcceptedSetVersion, IdempotencyKey: "accept-submission-in-amendment",
	})
	accepted, err := f.db.GetSubmission(ctx, submission.ID)
	if err != nil {
		t.Fatalf("reload submission: %v", err)
	}
	if accepted.State != domain.SubmissionAccepted {
		t.Fatalf("submission state during an amendment = %q, want accepted", accepted.State)
	}
	// The same task's revisions are still frozen.
	f.phaseRefusal(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(worked.ID), Revision: worked.CurrentRevision,
		ExpectedIntegratorGeneration: m.IntegratorGeneration, IdempotencyKey: "accept-during-amendment",
	})
}

// TestAmendmentDecisionRefusesANonAccountableCollaborator: an amendment is a
// human gate like any other round.
func TestAmendmentDecisionRefusesANonAccountableCollaborator(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	other := regressionMember(t, f.db, "bystander")
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	version := f.amend(t, tasks[0], protocol.TaskRevision{
		Title: "task a again", Objective: "task a again", Material: true,
		Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a"}},
	})
	if _, err := f.svc.DecidePlan(ctx, other.ID, protocol.MissionPlanDecideParams{
		MissionID: string(f.mission.ID), ExpectedPlanVersion: version,
		Decision: string(domain.MissionPlanApprove), IdempotencyKey: "decide-foreign",
	}); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("foreign amendment decision = %v, want ErrDenied", err)
	}
	if m := f.reloadMission(t); m.Phase != domain.MissionPhaseAmendmentReview {
		t.Fatalf("phase after a refused decision = %q, want amendment_review", m.Phase)
	}
}

// TestReplacedIntegratorLosesThePlanGateMethods: replacement retires the old
// run's authority immediately, in every phase.
func TestReplacedIntegratorLosesThePlanGateMethods(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(tasks[0].ID), IdempotencyKey: "revise-before-replacement",
		Revision: protocol.TaskRevision{Title: "inside", Objective: "inside",
			Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a"}}},
	})
	retired := f.mission.CurrentIntegratorRunID

	replaced, err := f.db.ReplaceIntegrator(ctx, f.mission.ID, f.mission.IntegratorGeneration,
		domain.MissionIntegrator{AccountMemberID: f.member.ID, Harness: "claude", Mode: domain.LaunchTUI},
		f.member.ID, f.member.ID, "replace-during-active")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	regressionRun(t, f.db, replaced.CurrentIntegratorRunID, f.workspace.ID, f.member.ID, "replacement integrator")

	for method, params := range map[string]any{
		protocol.MethodMissionClarificationComplete: protocol.MissionClarificationCompleteParams{IdempotencyKey: "clarify-retired"},
		protocol.MethodMissionPlanSubmit:            protocol.MissionPlanSubmitParams{Summary: "retired", IdempotencyKey: "submit-retired"},
		protocol.MethodTaskAccept: protocol.TaskAcceptParams{
			TaskID: string(tasks[0].ID), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
			IdempotencyKey: "accept-retired",
		},
	} {
		_, callErr := f.call(t, retired, method, params)
		if !errors.Is(callErr, store.ErrMissionStale) && !errors.Is(callErr, store.ErrNotFound) {
			t.Fatalf("%s from the retired integrator = %v, want a closed refusal", method, callErr)
		}
	}
}

// TestPlanShowWaitReturnsOnTheAmendmentDecision: the integrator waits for a
// human through the same call in every review phase.
func TestPlanShowWaitReturnsOnTheAmendmentDecision(t *testing.T) {
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	version := f.amend(t, tasks[0], protocol.TaskRevision{
		Title: "task a again", Objective: "task a again", Material: true,
		Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a"}},
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
	f.decide(t, version, domain.MissionPlanRevise, "narrow it down", "decide-revise-2")
	select {
	case result := <-waited:
		if result.err != nil {
			t.Fatalf("waiting mission.plan.show: %v", result.err)
		}
		if result.out.Plan.Phase != string(domain.MissionPhaseActive) || result.out.Plan.LatestFeedback != "narrow it down" {
			t.Fatalf("plan after the amendment decision = %+v, want active with the feedback", result.out.Plan)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mission.plan.show did not return when the amendment was decided")
	}
}

// TestPlanSubmitRacesAnApprovedWorkerStart: an amendment must not stall the
// work the human already approved, on either side of the submit.
func TestPlanSubmitRacesAnApprovedWorkerStart(t *testing.T) {
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}},
		taskSpec{key: "propose-b", title: "task b", paths: []string{"internal/b"}})
	approved, amended := tasks[0], tasks[1]
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(amended.ID), IdempotencyKey: "revise-2",
		Revision: protocol.TaskRevision{Title: "task b widened", Objective: "task b widened", Material: true,
			Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/b", "internal/d"}}},
	})

	starts := make(chan error, 1)
	go func() { starts <- f.startWorker(t, approved, "dispatch-racing") }()
	f.submit(t, "2", domain.MissionPhaseAmendmentReview)
	if err := <-starts; err != nil {
		t.Fatalf("worker.start racing mission.plan.submit: %v", err)
	}
	// And again after the submit landed: the approved task is untouched by
	// the round under review.
	if err := f.startWorker(t, approved, "dispatch-after-submit"); err != nil {
		t.Fatalf("worker.start after mission.plan.submit: %v", err)
	}
}

// TestAmendmentDiagnosticsDescribeTheProposedScope: the human reviewing an
// amendment needs the overlap the proposal would create, not the approved one.
func TestAmendmentDiagnosticsDescribeTheProposedScope(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}},
		taskSpec{key: "propose-b", title: "task b", paths: []string{"internal/b"}})
	before, err := f.svc.Show(ctx, protocol.MissionShowParams{MissionID: string(f.mission.ID)})
	if err != nil {
		t.Fatalf("mission.show: %v", err)
	}
	for _, d := range before.Diagnostics {
		if d.Kind == scopeDiagIntended {
			t.Fatalf("approved plan already reports an intended overlap: %+v", d)
		}
	}
	f.amend(t, tasks[0], protocol.TaskRevision{
		Title: "task a into b", Objective: "task a into b", Material: true,
		Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/b"}},
	})
	after, err := f.svc.Show(ctx, protocol.MissionShowParams{MissionID: string(f.mission.ID)})
	if err != nil {
		t.Fatalf("mission.show after the amendment: %v", err)
	}
	found := false
	for _, d := range after.Diagnostics {
		if d.Kind == scopeDiagIntended && d.TaskID == string(tasks[0].ID) && d.PeerTaskID == string(tasks[1].ID) &&
			len(d.Paths) == 1 && d.Paths[0] == "internal/b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics after the amendment = %+v, want an intended overlap on internal/b", after.Diagnostics)
	}
	// The proposed revision is what the dashboard renders beside the approved
	// one, so the wire must carry it.
	for _, task := range after.Tasks {
		if task.ID == string(tasks[0].ID) {
			if task.PendingRevision == nil || task.PendingRevision.Revision != 2 || !task.PendingRevision.Material {
				t.Fatalf("amended task on the wire = %+v, want a material pending revision 2", task.PendingRevision)
			}
		}
	}
}

// TestAssignmentCarriesFeedbackDuringAnAmendment: the integrator reads the
// phase and the last feedback off its own assignment.
func TestAssignmentCarriesFeedbackDuringAnAmendment(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	version := f.amend(t, tasks[0], protocol.TaskRevision{
		Title: "task a again", Objective: "task a again", Material: true,
		Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a"}},
	})
	f.decide(t, version, domain.MissionPlanRevise, "narrow it down", "decide-revise-2")
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(tasks[0].ID), IdempotencyKey: "revise-3",
		Revision: protocol.TaskRevision{Title: "task a narrowed", Objective: "task a narrowed", Material: true,
			Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a"}}},
	})
	f.submit(t, "3", domain.MissionPhaseAmendmentReview)

	assignment, err := f.svc.Assignment(ctx, f.mission.CurrentIntegratorRunID)
	if err != nil {
		t.Fatalf("assignment: %v", err)
	}
	if assignment.Phase != string(domain.MissionPhaseAmendmentReview) || assignment.LatestFeedback != "narrow it down" {
		t.Fatalf("assignment = %+v, want amendment_review carrying the last feedback", assignment)
	}
	if !slicesContain(assignment.Capabilities, protocol.MethodMissionClarificationComplete) {
		t.Fatalf("integrator capabilities = %v, want mission.clarification.complete", assignment.Capabilities)
	}
}

func slicesContain(in []string, want string) bool {
	for _, v := range in {
		if v == want {
			return true
		}
	}
	return false
}
