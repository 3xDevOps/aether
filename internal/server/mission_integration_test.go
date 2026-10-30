//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// TestIntegrationMissionOrchestration drives the mission authority through the
// real human and run sockets. The workers are deliberately named shell
// fixtures, not vendor harness demonstrations: each fixture proactively
// exchanges a coordination message before it waits for the test to release it.
// The same scenario covers replay after a lost response, cancellation and retry, report reconciliation to Review,
// and an integrator generation change rejecting the disconnected old client.
func TestIntegrationMissionOrchestration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")

	e, srv := newCoordEnv(ctx, t, false)
	release := make(chan struct{})
	defer close(release)

	const (
		missionObjective = "mission shell integrator"
		workerAObjective = "mission shell worker A"
		workerBObjective = "mission shell worker B"
	)
	// These callbacks are intentionally ordinary shell-fixture behavior. They
	// use the same manual coordination path as a user harness, and never claim
	// to be a genuine mixed-vendor credentialed run.
	e.e2e(t).script(missionObjective, func(c *e2eContainer) {
		coordAgent{release: release}.run(ctx, c)
	})
	e.e2e(t).script(workerAObjective, func(c *e2eContainer) {
		coordAgent{peer: workerBObjective, body: "worker-A-before-overlap", release: release}.run(ctx, c)
	})
	e.e2e(t).script(workerBObjective, func(c *e2eContainer) {
		coordAgent{peer: workerAObjective, body: "worker-B-before-overlap", release: release}.run(ctx, c)
	})

	adaCtrl, adaClient := srv.control(t, e.ada.key)
	defer adaClient.Close()
	boCtrl, boClient := srv.control(t, e.bo.key)
	defer boClient.Close()

	if err := boCtrl.Call(protocol.MethodAccountShare, protocol.AccountMemberParams{MemberID: string(e.ada.id)}, nil); err != nil {
		t.Fatalf("account.share: %v", err)
	}
	choiceAda := protocol.MissionExecutionChoice{AccountMemberID: string(e.ada.id), Harness: "fake", Mode: string(domain.LaunchTUI)}
	choiceBo := protocol.MissionExecutionChoice{AccountMemberID: string(e.bo.id), Harness: "fake", Mode: string(domain.LaunchTUI)}
	var created protocol.MissionCreateResult
	if err := adaCtrl.Call(protocol.MethodMissionCreate, protocol.MissionCreateParams{
		WorkspaceID: string(e.ws.ID), Objective: missionObjective,
		AccountableHumanID: string(e.ada.id), Integrator: protocol.MissionIntegrator{
			AccountMemberID: string(e.ada.id), Harness: "fake", Mode: string(domain.LaunchTUI),
		}, ExecutionChoices: []protocol.MissionExecutionChoice{choiceAda, choiceBo},
		IdempotencyKey: "mission-create-1",
	}, &created); err != nil {
		t.Fatalf("mission.create: %v", err)
	}
	if created.Mission.ID == "" || created.Mission.CurrentIntegratorRunID == "" {
		t.Fatalf("mission.create returned incomplete mission: %+v", created.Mission)
	}
	missionID := created.Mission.ID
	integratorRun := created.Mission.CurrentIntegratorRunID
	integratorSocket := waitMissionSocket(t, e.coordDir(integratorRun))

	// A human retry after a lost response is idempotent and does not launch a
	// second integrator. The list/show surfaces expose the same server-issued ID.
	var replay protocol.MissionCreateResult
	if err := adaCtrl.Call(protocol.MethodMissionCreate, protocol.MissionCreateParams{
		WorkspaceID: string(e.ws.ID), Objective: missionObjective,
		AccountableHumanID: string(e.ada.id), Integrator: created.Mission.Integrator,
		ExecutionChoices: created.Mission.ExecutionChoices,
		IdempotencyKey:   "mission-create-1",
	}, &replay); err != nil {
		t.Fatalf("mission.create replay: %v", err)
	}
	if replay.Mission.ID != missionID || replay.Mission.CurrentIntegratorRunID != integratorRun {
		t.Fatalf("mission.create replay changed identity: first=%+v replay=%+v", created.Mission, replay.Mission)
	}
	var listed protocol.MissionListResult
	if err := adaCtrl.Call(protocol.MethodMissionList, protocol.MissionListParams{WorkspaceID: string(e.ws.ID), Limit: 10}, &listed); err != nil {
		t.Fatalf("mission.list: %v", err)
	}
	if !containsMission(listed.Missions, missionID) {
		t.Fatalf("mission.list omitted %s: %+v", missionID, listed.Missions)
	}
	var shown protocol.MissionShowResult
	if err := adaCtrl.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &shown); err != nil {
		t.Fatalf("mission.show before tasks: %v", err)
	}
	if shown.Mission.CurrentIntegratorRunID != integratorRun || shown.Mission.IntegratorGeneration != created.Mission.IntegratorGeneration {
		t.Fatalf("mission.show identity mismatch: %+v", shown.Mission)
	}

	// Task IDs are server-issued through the integrator socket. The proposal
	// path creates each row and leaves its first revision awaiting acceptance.
	propose := func(objective, title, key string) domain.TaskID {
		params := protocol.TaskProposeParams{
			MissionID: missionID,
			Revision: protocol.TaskRevision{
				Title: title, Objective: objective, Status: string(domain.TaskRevisionProposed),
				EvidenceRequirements: []protocol.EvidenceRequirement{{Kind: "test", Detail: "fixture evidence"}},
			},
			IdempotencyKey: key,
		}
		var result protocol.TaskMutationResult
		if err := pacedCall(ctx, integratorSocket, protocol.MethodTaskPropose, params, &result); err != nil {
			t.Fatalf("task.propose %s: %v", title, err)
		}
		if result.Task.ID == "" || result.Task.CurrentRevision != 1 {
			t.Fatalf("task.propose %s returned incomplete task: %+v", title, result.Task)
		}
		// A retry after a lost response reuses the exact JSON and key. It must
		// replay the same task rather than create a second server row.
		var replay protocol.TaskMutationResult
		if err := pacedCall(ctx, integratorSocket, protocol.MethodTaskPropose, params, &replay); err != nil {
			t.Fatalf("task.propose %s replay: %v", title, err)
		}
		if replay.Task.ID != result.Task.ID || replay.Task.CurrentRevision != result.Task.CurrentRevision {
			t.Fatalf("task.propose %s replay changed identity: first=%+v replay=%+v", title, result.Task, replay.Task)
		}
		var listedTasks protocol.TaskListResult
		if err := pacedCall(ctx, integratorSocket, protocol.MethodTaskList, protocol.TaskListParams{MissionID: missionID}, &listedTasks); err != nil {
			t.Fatalf("task.list after %s replay: %v", title, err)
		}
		count := 0
		for _, task := range listedTasks.Tasks {
			if task.ID == result.Task.ID {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("task.list has %d rows for replayed task %s: %+v", count, result.Task.ID, listedTasks.Tasks)
		}
		conflict := params
		conflict.Revision.Objective = objective + " changed"
		var conflictResult protocol.TaskMutationResult
		err := pacedCall(ctx, integratorSocket, protocol.MethodTaskPropose, conflict, &conflictResult)
		if err == nil || coordtransport.ErrorCode(err) != protocol.CodeConflict {
			t.Fatalf("task.propose reused key with changed objective error = %v, want CodeConflict", err)
		}
		return domain.TaskID(result.Task.ID)
	}
	taskA := propose(workerAObjective, "worker A", "propose-A")
	taskB := propose(workerBObjective, "worker B", "propose-B")

	// Planning: no worker runs and no revision is accepted until the
	// integrator starts the mission, and it cannot start while the question
	// it asked the accountable human is unanswered. No human approves a plan.
	if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: missionID, TaskID: string(taskA), TaskRevision: 1, DispatchKey: "dispatch-before-start",
		Harness: "fake", Mode: string(domain.LaunchTUI), AccountOwnerID: string(e.ada.id),
		RunOwnerID: string(e.ada.id), ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
	}, nil); err == nil || coordtransport.ErrorCode(err) != protocol.CodeInvalidState {
		t.Fatalf("worker.start before mission.start error = %v, want CodeInvalidState", err)
	}
	var asked protocol.MissionQuestionResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which checkout flow?", IdempotencyKey: "mission-ask-1",
	}, &asked); err != nil {
		t.Fatalf("mission.question.ask: %v", err)
	}
	var pending protocol.MissionPlanShowResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{}, &pending); err != nil {
		t.Fatalf("mission.plan.show: %v", err)
	}
	if pending.Plan.Phase != string(domain.MissionPhasePlanning) || pending.Plan.OpenQuestions != 1 {
		t.Fatalf("plan state before the answer = %+v, want planning with one open question", pending.Plan)
	}
	if err := pacedCall(ctx, integratorSocket, protocol.MethodMissionStart, protocol.MissionStartParams{
		MissionID: missionID, IdempotencyKey: "mission-start-early",
	}, nil); err == nil || coordtransport.ErrorCode(err) != protocol.CodeInvalidState {
		t.Fatalf("mission.start with an unanswered question = %v, want CodeInvalidState", err)
	}
	// Every seeded member is an admin, and an admin may answer for the
	// accountable human; the refusal is for a plain collaborator.
	_, cyKey := writeClientKey(t)
	cy := &domain.Member{
		DisplayName: "Cy", PublicKey: string(ssh.MarshalAuthorizedKey(cyKey.PublicKey())),
		Color: "#4363d8", Role: domain.RoleCollaborator,
	}
	if err := srv.srv.Store().CreateMember(ctx, cy); err != nil {
		t.Fatalf("seed collaborator: %v", err)
	}
	cyCtrl, cyClient := srv.control(t, cyKey)
	defer cyClient.Close()
	if err := cyCtrl.Call(protocol.MethodMissionQuestionAnswer, protocol.MissionQuestionAnswerParams{
		QuestionID: asked.Question.ID, Answer: "not mine to answer", IdempotencyKey: "mission-answer-cy",
	}, nil); controlErrorCode(err) != protocol.CodeDenied {
		t.Fatalf("mission.question.answer from a non-accountable collaborator = %v, want denied", err)
	}
	var answered protocol.MissionQuestionResult
	if err := adaCtrl.Call(protocol.MethodMissionQuestionAnswer, protocol.MissionQuestionAnswerParams{
		QuestionID: asked.Question.ID, Answer: "the existing checkout flow", IdempotencyKey: "mission-answer-1",
	}, &answered); err != nil {
		t.Fatalf("mission.question.answer: %v", err)
	}
	if answered.Question.AnsweredAt == nil || answered.Question.AnsweredByMemberID != string(e.ada.id) {
		t.Fatalf("answered question = %+v, want an answer attributed to the accountable human", answered.Question)
	}
	// The run reads the durable answer through its existing mission context.
	var answeredPlan protocol.MissionPlanShowResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{}, &answeredPlan); err != nil {
		t.Fatalf("mission.plan.show after answer: %v", err)
	}
	if len(answeredPlan.Questions) != 1 || answeredPlan.Questions[0].Answer != answered.Question.Answer ||
		answeredPlan.Questions[0].AnsweredByMemberID != string(e.ada.id) {
		t.Fatalf("integrator answer context = %+v, want the accountable human's answer", answeredPlan.Questions)
	}
	var started protocol.MissionStartResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodMissionStart, protocol.MissionStartParams{
		MissionID: missionID, IdempotencyKey: "mission-start-1",
	}, &started); err != nil {
		t.Fatalf("mission.start: %v", err)
	}
	if started.Plan.Phase != string(domain.MissionPhaseActive) || started.Plan.OpenQuestions != 0 {
		t.Fatalf("plan state after start = %+v, want active with no open question", started.Plan)
	}
	var startedPlan protocol.MissionPlanShowResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{}, &startedPlan); err != nil {
		t.Fatalf("mission.plan.show after start: %v", err)
	}
	if startedPlan.Plan.Phase != string(domain.MissionPhaseActive) {
		t.Fatalf("integrator plan phase = %q, want active", startedPlan.Plan.Phase)
	}
	// Start accepted both revisions in one step.
	var readyTasks protocol.TaskListResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodTaskList, protocol.TaskListParams{MissionID: missionID}, &readyTasks); err != nil {
		t.Fatalf("task.list after start: %v", err)
	}
	if len(readyTasks.Tasks) != 2 {
		t.Fatalf("task.list after start returned %d tasks, want 2", len(readyTasks.Tasks))
	}
	for _, task := range readyTasks.Tasks {
		if task.Status != string(domain.TaskReady) || task.Revision == nil || task.Revision.AcceptedByRunID != integratorRun {
			t.Fatalf("task %s after start = %+v, want ready and accepted by the integrator", task.ID, task)
		}
	}
	var reaccepted protocol.TaskMutationResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(taskA), Revision: 1, ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
		IdempotencyKey: "reaccept-started-A",
	}, &reaccepted); coordtransport.ErrorCode(err) != protocol.CodeConflict {
		t.Fatalf("task.accept already accepted revision = %v, want conflict", err)
	}

	start := func(task domain.TaskID, key string) protocol.WorkerStartResult {
		var out protocol.WorkerStartResult
		if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
			MissionID: missionID, TaskID: string(task), TaskRevision: 1, DispatchKey: key,
			Harness: "fake", Mode: string(domain.LaunchTUI), AccountOwnerID: string(e.ada.id),
			RunOwnerID: string(e.ada.id), ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
		}, &out); err != nil {
			t.Fatalf("worker.start %s: %v", key, err)
		}
		if out.Attempt.ID == "" || out.Attempt.RunID == "" {
			t.Fatalf("worker.start %s returned incomplete attempt: %+v", key, out)
		}
		return out
	}
	attemptA := start(taskA, "dispatch-A")
	attemptB := start(taskB, "dispatch-B")

	// Treat the first worker.start response as lost: replaying its dispatch key
	// returns one attempt and explicitly marks the replay.
	var replayed protocol.WorkerStartResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: missionID, TaskID: string(taskA), TaskRevision: 1, DispatchKey: "dispatch-A",
		Harness: "fake", Mode: string(domain.LaunchTUI), AccountOwnerID: string(e.ada.id),
		RunOwnerID: string(e.ada.id), ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
	}, &replayed); err != nil {
		t.Fatalf("worker.start lost-response replay: %v", err)
	}
	if !replayed.Replayed || replayed.Attempt.ID != attemptA.Attempt.ID || replayed.Attempt.RunID != attemptA.Attempt.RunID {
		t.Fatalf("worker.start replay = %+v, want attempt %s/%s", replayed, attemptA.Attempt.ID, attemptA.Attempt.RunID)
	}

	// The two fixtures are live together and each sends before the release,
	// proving this is a real socket/mailbox exchange rather than store-only
	// work. These attachments only mirror worker output and are deliberately
	// read-only so observing a worker cannot install a human takeover hold.
	attA := openMissionObserver(t, adaClient, attemptA.Attempt.RunID)
	attB := openMissionObserver(t, adaClient, attemptB.Attempt.RunID)
	attA.waitOutput(t, "inbox:worker-B-before-overlap")
	attB.waitOutput(t, "inbox:worker-A-before-overlap")
	workerSocket := waitMissionSocket(t, e.coordDir(attemptA.Attempt.RunID))
	var unauthorizedList protocol.WorkerListResult
	if err := pacedCall(ctx, workerSocket, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: missionID}, &unauthorizedList); coordtransport.ErrorCode(err) != protocol.CodeDenied {
		t.Fatalf("worker.list from worker = %v, want denied", err)
	}
	if len(unauthorizedList.Attempts) != 0 {
		t.Fatalf("denied worker.list exposed attempts: %+v", unauthorizedList)
	}
	if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerRetry, protocol.WorkerRetryParams{
		AttemptID: attemptA.Attempt.ID, DispatchKey: "retry-active-A", ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
	}, nil); coordtransport.ErrorCode(err) != protocol.CodeInvalidState {
		t.Fatalf("worker.retry active attempt = %v, want invalid state", err)
	}
	var activeAttempts protocol.WorkerListResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: missionID}, &activeAttempts); err != nil {
		t.Fatalf("worker.list after refused retry: %v", err)
	}
	if len(activeAttempts.Attempts) != 2 {
		t.Fatalf("refused retry changed attempt count: %+v", activeAttempts)
	}

	// A real human takeover places a durable hold on worker A. The integrator
	// cancellation request is denied before it can persist any cancellation
	// intent; releasing the hold must not later execute that denied request.
	_, takeoverGeneration := openMissionTakeover(t, boClient, attemptA.Attempt.RunID)
	var denied protocol.WorkerMutationResult
	err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerCancel, protocol.WorkerCancelParams{
		AttemptID: attemptA.Attempt.ID, ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration, IdempotencyKey: "cancel-held-A",
	}, &denied)
	if err == nil || coordtransport.ErrorCode(err) != protocol.CodeDenied {
		t.Fatalf("worker.cancel under human takeover error = %v, want denied", err)
	}
	var inspected protocol.WorkerInspectResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerInspect, protocol.WorkerInspectParams{AttemptID: attemptA.Attempt.ID}, &inspected); err != nil {
		t.Fatalf("worker.inspect under takeover: %v", err)
	}
	if inspected.Attempt.CancelRequestedAt != nil || !inspected.Attempt.TakeoverActive {
		t.Fatalf("takeover inspection recorded cancellation or lost hold: %+v", inspected.Attempt)
	}
	releaseMissionTakeover(t, boClient, attemptA.Attempt.RunID, "mission-takeover-"+attemptA.Attempt.RunID, takeoverGeneration)
	if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerInspect, protocol.WorkerInspectParams{AttemptID: attemptA.Attempt.ID}, &inspected); err != nil {
		t.Fatalf("worker.inspect after takeover release: %v", err)
	}
	if inspected.Attempt.CancelRequestedAt != nil || inspected.Attempt.State == string(domain.AttemptCancelled) {
		t.Fatalf("denied cancellation fired after explicit release: %+v", inspected.Attempt)
	}

	var cancelled protocol.WorkerMutationResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerCancel, protocol.WorkerCancelParams{
		AttemptID: attemptA.Attempt.ID, ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration, IdempotencyKey: "cancel-A",
	}, &cancelled); err != nil {
		t.Fatalf("worker.cancel A: %v", err)
	}
	cancellationDeadline := time.Now().Add(30 * time.Second)
	for {
		if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerInspect, protocol.WorkerInspectParams{AttemptID: attemptA.Attempt.ID}, &inspected); err != nil {
			t.Fatalf("worker.inspect while cancelling A: %v", err)
		}
		if inspected.Attempt.State == string(domain.AttemptCancelled) {
			break
		}
		if time.Now().After(cancellationDeadline) {
			t.Fatalf("worker A did not finish cancelling: %+v", inspected.Attempt)
		}
	}
	var retried protocol.WorkerMutationResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerRetry, protocol.WorkerRetryParams{
		AttemptID: attemptA.Attempt.ID, DispatchKey: "dispatch-A-retry", ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
	}, &retried); err != nil {
		t.Fatalf("worker.retry A: %v", err)
	}
	if retried.Attempt.ID == attemptA.Attempt.ID || retried.Attempt.RunID == "" {
		t.Fatalf("worker.retry A did not reserve a new attempt: %+v", retried)
	}

	// A report with no user evidence refs still captures the retained packet and
	// leaves the task in Review for human admission; it is never auto-accepted.
	retrySocket := waitMissionSocket(t, e.coordDir(retried.Attempt.RunID))
	retainedContainer := e.container(t, retried.Attempt.RunID)
	retainedID, err := e.rt.FindByCreationKey(ctx, retried.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var peerQuestion protocol.CoordAskResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodCoordAsk, protocol.CoordAskParams{
		ToRunID: retried.Attempt.RunID, Body: "which files does the retry touch?", IdempotencyKey: "ask-retry-A",
	}, &peerQuestion); err != nil {
		t.Fatalf("integrator coord.ask: %v", err)
	}
	var workerMail protocol.CoordInboxResult
	if err := pacedCall(ctx, retrySocket, protocol.MethodCoordInbox, protocol.CoordInboxParams{}, &workerMail); err != nil {
		t.Fatalf("worker inbox: %v", err)
	}
	if !slices.ContainsFunc(workerMail.Messages, func(m protocol.CoordMessage) bool { return m.ID == peerQuestion.QuestionID }) {
		t.Fatalf("worker inbox = %+v, want the integrator's question", workerMail.Messages)
	}
	if err := pacedCall(ctx, retrySocket, protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: workerMail.AckToken}, &workerMail); err != nil {
		t.Fatalf("worker acknowledges the question: %v", err)
	}
	var replied protocol.CoordReplyResult
	if err := pacedCall(ctx, retrySocket, protocol.MethodCoordReply, protocol.CoordReplyParams{
		QuestionID: peerQuestion.QuestionID, Body: "only the fixture file", IdempotencyKey: "reply-retry-A",
	}, &replied); err != nil {
		t.Fatalf("worker coord.reply: %v", err)
	}
	var report protocol.CoordReportResult
	if err := pacedCall(ctx, retrySocket, protocol.MethodCoordReport, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "fixture completed without required user evidence", IdempotencyKey: "report-retry-A",
	}, &report); err != nil {
		t.Fatalf("coord.report missing-evidence fixture: %v", err)
	}
	if report.ReportID == "" || !strings.Contains(strings.ToLower(report.NextAction), "review") {
		t.Fatalf("coord.report next action = %+v, want review", report)
	}
	// Earlier fixture mail may precede the report, so drain batch by batch.
	var reportMail protocol.CoordInboxResult
	for reported := false; !reported; {
		if err := pacedCall(ctx, integratorSocket, protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: reportMail.AckToken}, &reportMail); err != nil {
			t.Fatalf("integrator inbox after worker report: %v", err)
		}
		if len(reportMail.Messages) == 0 {
			t.Fatal("integrator inbox has no message for the worker's success report")
		}
		reported = slices.ContainsFunc(reportMail.Messages, func(m protocol.CoordMessage) bool {
			return m.FromRunID == string(retried.Attempt.RunID) && m.CorrelationID == report.ReportID && m.Body == "fixture completed without required user evidence"
		})
	}
	if err := pacedCall(ctx, integratorSocket, protocol.MethodCoordInbox, protocol.CoordInboxParams{AckToken: reportMail.AckToken}, &reportMail); err != nil {
		t.Fatalf("acknowledge worker report: %v", err)
	}
	// The worker's result is the integrator's to review, never its owner's.
	var workerRun protocol.RunResult
	if err := boCtrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: string(retried.Attempt.RunID)}, &workerRun); err != nil {
		t.Fatalf("run.get worker: %v", err)
	}
	if workerRun.Run.Review != "" {
		t.Fatalf("worker review after its success report = %q, want none", workerRun.Run.Review)
	}
	var history protocol.CoordMessagesListResult
	if err := boCtrl.Call(protocol.MethodCoordMessagesList, protocol.CoordMessagesListParams{
		WorkspaceID: string(e.ws.ID), MissionID: missionID, RunID: retried.Attempt.RunID,
	}, &history); err != nil {
		t.Fatalf("coord.messages.list: %v", err)
	}
	byID := make(map[string]protocol.RunMessage, len(history.Messages))
	for _, m := range history.Messages {
		if m.MissionID != missionID {
			t.Fatalf("listed message %+v is not stamped with mission %s", m, missionID)
		}
		byID[m.ID] = m
	}
	question, reply := byID[peerQuestion.QuestionID], byID[replied.MessageID]
	if question.Kind != protocol.CoordMessageKindQuestion || question.FromRunID != integratorRun ||
		question.ToRunID != retried.Attempt.RunID || question.AckedAt == nil {
		t.Fatalf("listed question = %+v, want the integrator's acknowledged question", question)
	}
	if reply.Kind != protocol.CoordMessageKindReply || reply.CorrelationID != peerQuestion.QuestionID || reply.Body != "only the fixture file" {
		t.Fatalf("listed reply = %+v, want the worker's reply on the question thread", reply)
	}
	reportRow := slices.IndexFunc(history.Messages, func(m protocol.RunMessage) bool { return m.CorrelationID == report.ReportID })
	if reportRow < 0 {
		t.Fatalf("history %+v has no row for report %s", history.Messages, report.ReportID)
	}
	if got := history.Messages[reportRow]; got.Kind != protocol.CoordMessageKindReport || got.Outcome != protocol.CoordOutcomeSuccess ||
		got.Summary != "fixture completed without required user evidence" || got.AckedAt == nil {
		t.Fatalf("listed report = %+v, want the acknowledged success report", got)
	}
	if err := adaCtrl.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &shown); err != nil {
		t.Fatalf("mission.show after report: %v", err)
	}
	if !containsTaskStatus(shown.Tasks, string(taskA), string(domain.TaskReview)) {
		t.Fatalf("task A did not remain Review after missing evidence: %+v", shown.Tasks)
	}
	retentionDeadline := time.Now().Add(30 * time.Second)
	for {
		if err := pacedCall(ctx, integratorSocket, protocol.MethodWorkerInspect, protocol.WorkerInspectParams{AttemptID: retried.Attempt.ID}, &inspected); err != nil {
			t.Fatal(err)
		}
		if inspected.Attempt.State == string(domain.AttemptCompleted) {
			break
		}
		if time.Now().After(retentionDeadline) {
			t.Fatalf("reported worker did not release capacity: %+v", inspected.Attempt)
		}
	}
	if domain.AttemptState(inspected.Attempt.State).HoldsConcurrency() {
		t.Fatal("completed worker still holds attempt capacity")
	}
	currentID, err := e.rt.FindByCreationKey(ctx, retried.Attempt.RunID)
	if err != nil || currentID != retainedID {
		t.Fatalf("report destroyed or replaced worker container: %s, %v", currentID, err)
	}
	retainedContainer.mu.Lock()
	retainedState := retainedContainer.state
	retainedContainer.mu.Unlock()
	if retainedState != "paused" {
		t.Fatalf("reported worker remains executable: %s", retainedState)
	}
	observation, err := srv.srv.sched.ObserveMissionRun(ctx, domain.RunID(retried.Attempt.RunID))
	if err != nil || observation.State != scheduler.MissionRunRetained || !observation.RetentionSettled {
		t.Fatalf("completed worker retention unsettled: %+v, %v", observation, err)
	}
	if !srv.srv.sched.RetainsContainer(ctx, domain.RunID(retried.Attempt.RunID)) {
		t.Fatal("completed worker lost its inspectable coordination assets")
	}

	// New work after start needs no human: the integrator accepts it alone.
	var addedTask protocol.TaskMutationResult
	if err := pacedCall(ctx, integratorSocket, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID:      missionID,
		Revision:       protocol.TaskRevision{Title: "worker C", Objective: "mission shell worker C"},
		IdempotencyKey: "propose-C",
	}, &addedTask); err != nil {
		t.Fatalf("task.propose during an active mission: %v", err)
	}
	if err := pacedCall(ctx, integratorSocket, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: addedTask.Task.ID, Revision: addedTask.Task.CurrentRevision,
		ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration, IdempotencyKey: "accept-C",
	}, nil); err != nil {
		t.Fatalf("task.accept of new work: %v", err)
	}
	if err := adaCtrl.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &shown); err != nil {
		t.Fatalf("mission.show after new work: %v", err)
	}
	if shown.Mission.Phase != string(domain.MissionPhaseActive) || !containsTaskStatus(shown.Tasks, addedTask.Task.ID, string(domain.TaskReady)) {
		t.Fatalf("mission after accepting new work = %s %+v, want active with task %s ready", shown.Mission.Phase, shown.Tasks, addedTask.Task.ID)
	}

	// Replacing the integrator bumps generation and invalidates the old socket.
	var replaced protocol.MissionReplaceIntegratorResult
	if err := boCtrl.Call(protocol.MethodMissionReplaceIntegrator, protocol.MissionReplaceIntegratorParams{
		MissionID: missionID, ExpectedGeneration: created.Mission.IntegratorGeneration,
		Integrator:     protocol.MissionIntegrator{AccountMemberID: string(e.bo.id), Harness: "fake", Mode: string(domain.LaunchTUI)},
		IdempotencyKey: "replace-integrator-1",
	}, &replaced); err != nil {
		t.Fatalf("mission.replace-integrator: %v", err)
	}
	if replaced.Mission.IntegratorGeneration <= created.Mission.IntegratorGeneration || replaced.RunID == "" {
		t.Fatalf("mission.replace-integrator did not advance generation: %+v", replaced)
	}
	var stale protocol.WorkerListResult
	err = pacedCall(ctx, integratorSocket, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: missionID}, &stale)
	if err == nil {
		t.Fatal("old integrator socket worker.list unexpectedly succeeded after integrator replacement")
	}

	// The current integrator's success report completes the mission with no
	// human decision, and the reconcile loop stops the worker still running.
	replacedSocket := waitMissionSocket(t, e.coordDir(replaced.RunID))
	if err := pacedCall(ctx, replacedSocket, protocol.MethodCoordReport, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "all tasks delivered", IdempotencyKey: "integrator-success",
	}, nil); err != nil {
		t.Fatalf("integrator coord.report success: %v", err)
	}
	if err := adaCtrl.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &shown); err != nil {
		t.Fatalf("mission.show after the integrator's report: %v", err)
	}
	if shown.Mission.Phase != string(domain.MissionPhaseCompleted) {
		t.Fatalf("mission phase after the integrator's success report = %q, want completed", shown.Mission.Phase)
	}
	teardownDeadline := time.Now().Add(time.Minute)
	for {
		if err := adaCtrl.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &shown); err != nil {
			t.Fatalf("mission.show while stopping leftover workers: %v", err)
		}
		live := false
		for _, attempt := range shown.Attempts {
			if domain.AttemptState(attempt.State).HoldsConcurrency() {
				live = true
			}
		}
		if !live {
			break
		}
		if time.Now().After(teardownDeadline) {
			t.Fatalf("completed mission still has live workers: %+v", shown.Attempts)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Server boot preserves the exact paused worker, then a boot beyond its
	// deadline reclaims that same runtime object and coordination ownership.
	srv.stop()
	restarted := e.start(ctx, t, false)
	currentID, err = e.rt.FindByCreationKey(ctx, retried.Attempt.RunID)
	if err != nil || currentID != retainedID {
		t.Fatalf("restart lost retained worker: %s, %v", currentID, err)
	}
	restarted.stop()
	sidecarPath := filepath.Join(e.dataDir, "scheduler", retried.Attempt.RunID+".json")
	sidecarBytes, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	var sidecar map[string]json.RawMessage
	if err := json.Unmarshal(sidecarBytes, &sidecar); err != nil {
		t.Fatal(err)
	}
	sidecar["retained_until"], err = json.Marshal(time.Now().UTC().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	sidecarBytes, err = json.Marshal(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecarPath, sidecarBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	expired := e.start(ctx, t, false)
	expiryDeadline := time.Now().Add(30 * time.Second)
	for {
		_, findErr := e.rt.FindByCreationKey(ctx, retried.Attempt.RunID)
		if errors.Is(findErr, runtime.ErrNotFound) &&
			!expired.srv.sched.RetainsContainer(ctx, domain.RunID(retried.Attempt.RunID)) {
			break
		}
		if time.Now().After(expiryDeadline) {
			t.Fatalf("expired worker still owns runtime or coordination: %v", findErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

}

type missionCLIEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func waitMissionSubmissions(ctx context.Context, t *testing.T, ctrl *protocol.Client, missionID string, want int, workers ...*attachConn) protocol.MissionShowResult {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var shown protocol.MissionShowResult
		if err := ctrl.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &shown); err == nil && len(shown.Submissions) >= want {
			return shown
		}
		outputs := make([]string, len(workers))
		for i, w := range workers {
			outputs[i] = w.output()
			if strings.Contains(outputs[i], "fixture-report-failed:") {
				t.Fatalf("worker report failed; output %q", outputs[i])
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d durable mission submissions; worker output %q", want, outputs)
		case <-ticker.C:
		}
	}
}

func runGitUpdateRef(t *testing.T, repo, ref, revision string) error {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repo, "update-ref", ref, revision)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref %s %s: %w (%s)", ref, revision, err, out)
	}
	return nil
}

func buildMissionAgentImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mission-agent"), `#!/bin/sh
set -eu

help=$(/usr/local/bin/aether-internal --help)
case "$help" in *"aether-internal"*) ;; *) echo "cli-help-missing" >&2; exit 1 ;; esac
skill=$(/usr/local/bin/aether-internal skill)
case "$skill" in *"Run:"*) ;; *) echo "cli-skill-missing" >&2; exit 1 ;; esac
status=$(/usr/local/bin/aether-internal status)
# Fixture task names are controlled ASCII; match the run's task before nested peers.
task=$(printf '%s\n' "$status" | sed -nE 's/.*"result":\{[^{}]*"task":"([^"]*)".*/\1/p')

case "$task" in
	*"integrator"*)
		echo "fixture-integrator-ready"
		while :; do sleep 60; done
		;;
esac

case "$task" in
	*"worker A"*) result="worker-a.txt"; body="worker-A-before-edit"; peer_task="mission Docker worker B" ;;
	*"worker B"*) result="worker-b.txt"; body="worker-B-before-edit"; peer_task="mission Docker worker A" ;;
	*) echo "fixture-peer-task-not-found" >&2; exit 1 ;;
esac
# Observers cannot write PTY input; the host releases this read-only gate
# after both output streams are attached.
while [ ! -f /run/aether/mission-fixture-start ]; do sleep 0.1; done


peer=
attempt=0
while [ "$attempt" -lt 120 ]; do
	status=$(/usr/local/bin/aether-internal status --json)
	peer=$(printf '%s\n' "$status" | sed -nE "s/.*\"run_id\":\"([^\"]*)\",\"member_id\":\"[^\"]*\",\"task\":\"$peer_task\".*/\1/p")
	if [ -n "$peer" ]; then break; fi
	attempt=$((attempt + 1))
	sleep 1
done
[ -n "$peer" ] || { echo "fixture-peer-not-found" >&2; exit 1; }
/usr/local/bin/aether-internal send --to "$peer" --body "$body" --idempotency-key "mission-send-$AETHER_RUN_ID" >/dev/null
echo "fixture-sent:$AETHER_RUN_ID"
# Peer discovery can take longer than the bounded acknowledgement polls.
attempt=0
ack=
while [ "$attempt" -lt 20 ]; do
	inbox=$(/usr/local/bin/aether-internal inbox --wait 2)
	ack=$(printf '%s\n' "$inbox" | sed -n 's/.*"ack_token":"\([^"]*\)".*/\1/p')
	if [ -n "$ack" ]; then
		/usr/local/bin/aether-internal inbox --ack "$ack" >/dev/null
		echo "fixture-acked:$AETHER_RUN_ID"
		break
	fi
	attempt=$((attempt + 1))
done
[ -n "$ack" ] || { echo "fixture-message-not-acked" >&2; exit 1; }
printf '%s\n' "$body" > "$result"
if ! reported=$(/usr/local/bin/aether-internal report --outcome success --summary "mission fixture retained result" --idempotency-key "mission-report-$AETHER_RUN_ID" 2>&1); then
	echo "fixture-report-failed:$reported"
	exit 1
fi
`)
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	writeFile(t, filepath.Join(dir, "Dockerfile"),
		"FROM busybox\n"+
			"COPY mission-agent /usr/local/bin/mission-agent\n"+
			"COPY mission-agent /usr/local/bin/claude\n"+
			"COPY mission-agent /usr/local/bin/pi\n"+
			"COPY mission-agent /usr/local/bin/omp\n"+
			"RUN chmod +x /usr/local/bin/mission-agent /usr/local/bin/claude /usr/local/bin/pi /usr/local/bin/omp\n"+
			fmt.Sprintf("USER %d:%d\n", uid, gid))
	image := fmt.Sprintf("aether-e2e-missionagent:%d", os.Getpid())
	if out, err := exec.Command("docker", "build", "-q", "-t", image, dir).CombinedOutput(); err != nil {
		t.Fatalf("build mission fixture image %s: %v (%s)", image, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rmi", "-f", image).CombinedOutput(); err != nil {
			t.Logf("remove mission fixture image %s: %v (%s)", image, err, out)
		}
	})
	return image
}

func containsMission(missions []protocol.Mission, id string) bool {
	for _, m := range missions {
		if m.ID == id {
			return true
		}
	}
	return false
}

func containsTaskStatus(tasks []protocol.Task, id, status string) bool {
	for _, task := range tasks {
		if task.ID == id && task.Status == status {
			return true
		}
	}
	return false
}

func waitMissionSocket(t *testing.T, dir string) string {
	t.Helper()
	socket := filepath.Join(dir, coordtransport.SocketName)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			return socket
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("coordination socket did not appear: %s", socket)
	return ""
}

// openMissionObserver attaches a read-only mirror. It must never acquire the
// human control lease: mission workers treat that lease as an explicit
// takeover and refuse integrator reconciliation while it is held.
func openMissionObserver(t *testing.T, client *ssh.Client, runID string) *attachConn {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("observer session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if err := sess.RequestPty("xterm-256color", 30, 120, ssh.TerminalModes{}); err != nil {
		t.Fatalf("observer pty-req: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemAttach); err != nil {
		t.Fatalf("observer subsystem: %v", err)
	}
	header, err := json.Marshal(protocol.AttachRequest{
		RunID: runID, ReadOnly: true, Cols: 120, Rows: 30,
		ControlSessionID: "integration-observer-" + runID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(append(header, '\n')); err != nil {
		t.Fatalf("write observer request: %v", err)
	}
	r := bufio.NewReader(stdout)
	line, err := protocol.ReadLine(r)
	if err != nil {
		t.Fatalf("read observer ack: %v", err)
	}
	var ack protocol.AttachResponse
	if err := json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("decode observer ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("observer attach denied: %+v", ack)
	}
	if ack.HasControl {
		t.Fatalf("observer acquired human control: %+v", ack)
	}
	a := &attachConn{sess: sess, stdin: stdin}
	go a.pump(r)
	return a
}

func openMissionTakeover(t *testing.T, client *ssh.Client, runID string) (*attachConn, uint64) {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("takeover session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if err := sess.RequestPty("xterm-256color", 30, 120, ssh.TerminalModes{}); err != nil {
		t.Fatalf("takeover pty-req: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("takeover stdin: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("takeover stdout: %v", err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemAttach); err != nil {
		t.Fatalf("takeover subsystem: %v", err)
	}
	req := protocol.AttachRequest{
		RunID: runID, Cols: 120, Rows: 30,
		ControlSessionID: "mission-takeover-" + runID, Takeover: true,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal takeover request: %v", err)
	}
	if _, err := stdin.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write takeover request: %v", err)
	}
	r := bufio.NewReader(stdout)
	line, err := protocol.ReadLine(r)
	if err != nil {
		t.Fatalf("read takeover ack: %v", err)
	}
	var ack protocol.AttachResponse
	if err := json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("decode takeover ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("takeover denied: %+v", ack)
	}
	if ack.ControlGeneration == 0 {
		t.Fatalf("takeover ack has no control generation: %+v", ack)
	}
	a := &attachConn{sess: sess, stdin: stdin}
	go a.pump(r)
	return a, ack.ControlGeneration
}

func releaseMissionTakeover(t *testing.T, client *ssh.Client, runID, sessionID string, generation uint64) {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("release session: %v", err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm-256color", 30, 120, ssh.TerminalModes{}); err != nil {
		t.Fatalf("release pty-req: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("release stdin: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("release stdout: %v", err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemAttach); err != nil {
		t.Fatalf("release subsystem: %v", err)
	}
	raw, err := json.Marshal(protocol.AttachRequest{
		RunID: runID, ReadOnly: true, Cols: 120, Rows: 30,
		ControlSessionID: sessionID, ControlGeneration: generation, ReleaseControl: true,
	})
	if err != nil {
		t.Fatalf("marshal release request: %v", err)
	}
	if _, err := stdin.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write release request: %v", err)
	}
	line, err := protocol.ReadLine(bufio.NewReader(stdout))
	if err != nil {
		t.Fatalf("read release ack: %v", err)
	}
	var ack protocol.AttachResponse
	if err := json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("decode release ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("release denied: %+v", ack)
	}
}

// controlErrorCode is the wire code of a control-channel error, or 0 for a
// nil or untyped error.
func controlErrorCode(err error) int {
	var rpcErr *protocol.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Code
	}
	return 0
}

// pacedCall spaces this scenario's sequential socket calls by at least the
// transport's one-second token refill interval, rather than consuming its
// burst allowance. The integrator fixture itself makes no socket calls.
// Every response, including a transport rate-limit error, reaches the caller
// unchanged; this helper never retries a rejected operation.
func pacedCall(ctx context.Context, socket, method string, params, result any) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return coordtransport.Call(ctx, socket, method, params, result)
	}
}
