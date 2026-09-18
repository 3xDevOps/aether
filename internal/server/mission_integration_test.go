//go:build integration

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestIntegrationMissionOrchestration drives the mission authority through the
// real human and run sockets. The workers are deliberately named shell
// fixtures, not vendor harness demonstrations: each fixture proactively
// exchanges a coordination message before it waits for the test to release it.
// The same scenario covers replay after a lost response, concurrency/total
// attempt bounds, cancellation and retry, report reconciliation to Review,
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

	choiceAda := protocol.MissionExecutionChoice{AccountMemberID: string(e.ada.id), Harness: "fake", Mode: string(domain.LaunchTUI)}
	choiceBo := protocol.MissionExecutionChoice{AccountMemberID: string(e.bo.id), Harness: "fake", Mode: string(domain.LaunchTUI)}
	var created protocol.MissionCreateResult
	if err := adaCtrl.Call(protocol.MethodMissionCreate, protocol.MissionCreateParams{
		WorkspaceID: string(e.ws.ID), Objective: missionObjective,
		AccountableHumanID: string(e.ada.id), Integrator: protocol.MissionIntegrator{
			AccountMemberID: string(e.ada.id), Harness: "fake", Mode: string(domain.LaunchTUI),
		}, ExecutionChoices: []protocol.MissionExecutionChoice{choiceAda, choiceBo},
		MaxConcurrentAttempts: 2, MaxTotalAttempts: 3, IdempotencyKey: "mission-create-1",
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
		ExecutionChoices:      created.Mission.ExecutionChoices,
		MaxConcurrentAttempts: 2, MaxTotalAttempts: 3, IdempotencyKey: "mission-create-1",
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
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodTaskPropose, params, &result); err != nil {
			t.Fatalf("task.propose %s: %v", title, err)
		}
		if result.Task.ID == "" || result.Task.CurrentRevision != 1 {
			t.Fatalf("task.propose %s returned incomplete task: %+v", title, result.Task)
		}
		// A retry after a lost response reuses the exact JSON and key. It must
		// replay the same task rather than create a second server row.
		var replay protocol.TaskMutationResult
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodTaskPropose, params, &replay); err != nil {
			t.Fatalf("task.propose %s replay: %v", title, err)
		}
		if replay.Task.ID != result.Task.ID || replay.Task.CurrentRevision != result.Task.CurrentRevision {
			t.Fatalf("task.propose %s replay changed identity: first=%+v replay=%+v", title, result.Task, replay.Task)
		}
		var listedTasks protocol.TaskListResult
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodTaskList, protocol.TaskListParams{MissionID: missionID}, &listedTasks); err != nil {
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
		err := coordtransport.Call(ctx, integratorSocket, protocol.MethodTaskPropose, conflict, &conflictResult)
		if err == nil || coordtransport.ErrorCode(err) != protocol.CodeConflict {
			t.Fatalf("task.propose reused key with changed objective error = %v, want CodeConflict", err)
		}
		return domain.TaskID(result.Task.ID)
	}
	taskA := propose(workerAObjective, "worker A", "propose-A")
	taskB := propose(workerBObjective, "worker B", "propose-B")
	for _, task := range []domain.TaskID{taskA, taskB} {
		var accepted protocol.TaskMutationResult
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
			TaskID: string(task), Revision: 1, ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
			IdempotencyKey: "accept-" + string(task),
		}, &accepted); err != nil {
			t.Fatalf("task.accept %s: %v", task, err)
		}
		if accepted.Task.Status != string(domain.TaskReady) {
			t.Fatalf("task %s status after accept = %q, want ready", task, accepted.Task.Status)
		}
	}

	start := func(task domain.TaskID, key string) protocol.WorkerStartResult {
		var out protocol.WorkerStartResult
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
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
	if attemptA.Attempt.State != string(domain.AttemptRunning) && attemptA.Attempt.State != string(domain.AttemptLaunching) {
		t.Fatalf("worker A state = %q, want active", attemptA.Attempt.State)
	}
	if attemptB.Attempt.State != string(domain.AttemptRunning) && attemptB.Attempt.State != string(domain.AttemptLaunching) {
		t.Fatalf("worker B state = %q, want active", attemptB.Attempt.State)
	}

	// Treat the first worker.start response as lost: replaying its dispatch key
	// returns one attempt and explicitly marks the replay.
	var replayed protocol.WorkerStartResult
	if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
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
	// proving this is a real socket/mailbox exchange rather than store-only work.
	attA := openAttach(t, adaClient, attemptA.Attempt.RunID)
	attB := openAttach(t, adaClient, attemptB.Attempt.RunID)
	attA.waitOutput(t, "inbox:worker-B-before-overlap")
	attB.waitOutput(t, "inbox:worker-A-before-overlap")

	// A real human takeover places a durable hold on worker A. The integrator
	// cancellation request is denied before it can persist any cancellation
	// intent; releasing the hold must not later execute that denied request.
	_, takeoverGeneration := openMissionTakeover(t, boClient, attemptA.Attempt.RunID)
	var denied protocol.WorkerMutationResult
	err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerCancel, protocol.WorkerCancelParams{
		AttemptID: attemptA.Attempt.ID, ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration, IdempotencyKey: "cancel-held-A",
	}, &denied)
	if err == nil || coordtransport.ErrorCode(err) != protocol.CodeDenied {
		t.Fatalf("worker.cancel under human takeover error = %v, want denied", err)
	}
	var inspected protocol.WorkerInspectResult
	if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerInspect, protocol.WorkerInspectParams{AttemptID: attemptA.Attempt.ID}, &inspected); err != nil {
		t.Fatalf("worker.inspect under takeover: %v", err)
	}
	if inspected.Attempt.CancelRequestedAt != nil || !inspected.Attempt.TakeoverActive {
		t.Fatalf("takeover inspection recorded cancellation or lost hold: %+v", inspected.Attempt)
	}
	releaseMissionTakeover(t, boClient, attemptA.Attempt.RunID, "mission-takeover-"+attemptA.Attempt.RunID, takeoverGeneration)
	if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerInspect, protocol.WorkerInspectParams{AttemptID: attemptA.Attempt.ID}, &inspected); err != nil {
		t.Fatalf("worker.inspect after takeover release: %v", err)
	}
	if inspected.Attempt.CancelRequestedAt != nil || inspected.Attempt.State == string(domain.AttemptCancelled) {
		t.Fatalf("denied cancellation fired after explicit release: %+v", inspected.Attempt)
	}

	// No third active attempt fits the concurrent bound. Cancel A, consume the
	// one remaining total-attempt slot with retry, then prove the total bound.
	var tooMany protocol.WorkerStartResult
	err = coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: missionID, TaskID: string(taskA), TaskRevision: 1, DispatchKey: "dispatch-C",
		Harness: "fake", Mode: string(domain.LaunchTUI), AccountOwnerID: string(e.ada.id), RunOwnerID: string(e.ada.id),
	}, &tooMany)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "limit") {
		t.Fatalf("worker.start over concurrency bound error = %v, want limit", err)
	}
	var cancelled protocol.WorkerMutationResult
	if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerCancel, protocol.WorkerCancelParams{
		AttemptID: attemptA.Attempt.ID, ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration, IdempotencyKey: "cancel-A",
	}, &cancelled); err != nil {
		t.Fatalf("worker.cancel A: %v", err)
	}
	if cancelled.Attempt.State != string(domain.AttemptCancelled) {
		t.Fatalf("worker.cancel A state = %q, want cancelled", cancelled.Attempt.State)
	}
	var retried protocol.WorkerMutationResult
	if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerRetry, protocol.WorkerRetryParams{
		AttemptID: attemptA.Attempt.ID, DispatchKey: "dispatch-A-retry", ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
	}, &retried); err != nil {
		t.Fatalf("worker.retry A: %v", err)
	}
	if retried.Attempt.ID == attemptA.Attempt.ID || retried.Attempt.RunID == "" {
		t.Fatalf("worker.retry A did not reserve a new attempt: %+v", retried)
	}
	var overTotal protocol.WorkerStartResult
	err = coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: missionID, TaskID: string(taskB), TaskRevision: 1, DispatchKey: "dispatch-D",
		Harness: "fake", Mode: string(domain.LaunchTUI), AccountOwnerID: string(e.ada.id), RunOwnerID: string(e.ada.id),
	}, &overTotal)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "limit") {
		t.Fatalf("worker.start over total bound error = %v, want limit", err)
	}

	// A report with no user evidence refs still captures the retained packet and
	// leaves the task in Review for human admission; it is never auto-accepted.
	retrySocket := waitMissionSocket(t, e.coordDir(retried.Attempt.RunID))
	var report protocol.CoordReportResult
	if err := coordtransport.Call(ctx, retrySocket, protocol.MethodCoordReport, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "fixture completed without required user evidence", IdempotencyKey: "report-retry-A",
	}, &report); err != nil {
		t.Fatalf("coord.report missing-evidence fixture: %v", err)
	}
	if report.ReportID == "" || !strings.Contains(strings.ToLower(report.NextAction), "review") {
		t.Fatalf("coord.report next action = %+v, want review", report)
	}
	if err := adaCtrl.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &shown); err != nil {
		t.Fatalf("mission.show after report: %v", err)
	}
	if !containsTaskStatus(shown.Tasks, string(taskA), string(domain.TaskReview)) {
		t.Fatalf("task A did not remain Review after missing evidence: %+v", shown.Tasks)
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
	err = coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: missionID}, &stale)
	if err == nil {
		t.Fatal("old integrator socket worker.list unexpectedly succeeded after integrator replacement")
	}

}

// TestIntegrationMissionCompositionInDocker drives the imported engine through
// the assembled server, real SSH control/run sockets, and the staged CLI in
// real Docker containers. The two worker programs are deterministic shell
// fixtures: they exchange durable coordination messages before writing their
// distinct result files. They are not a genuine mixed-harness vendor run.
func TestIntegrationMissionCompositionInDocker(t *testing.T) {
	requireBinary(t, "docker")
	if !dockerReachable(t) {
		t.Skip("mission composition needs a reachable Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	image := buildMissionAgentImage(t)
	rt, _, ok := dockerRuntime(t)
	if !ok {
		t.Fatal("the Docker daemon went away after the image was built")
	}
	e := &coordEnv{
		rt: rt, image: image, serverBinary: buildServerBinary(t),
		dataDir: filepath.Join(shortTempDir(t), "data"),
	}
	srv := e.seed(ctx, t, false)
	adaCtrl, adaClient := srv.control(t, e.ada.key)
	defer adaClient.Close()
	boCtrl, boClient := srv.control(t, e.bo.key)
	defer boClient.Close()

	const (
		integratorObjective = "mission Docker integrator fixture"
		workerAObjective    = "mission Docker worker A"
		workerBObjective    = "mission Docker worker B"
	)
	var created protocol.MissionCreateResult
	if err := adaCtrl.Call(protocol.MethodMissionCreate, protocol.MissionCreateParams{
		WorkspaceID: string(e.ws.ID), Objective: integratorObjective,
		AccountableHumanID: string(e.ada.id),
		Integrator: protocol.MissionIntegrator{
			AccountMemberID: string(e.ada.id), Harness: "claude", Mode: string(domain.LaunchTUI),
		},
		ExecutionChoices: []protocol.MissionExecutionChoice{
			{AccountMemberID: string(e.ada.id), Harness: "claude", Mode: string(domain.LaunchTUI)},
			{AccountMemberID: string(e.ada.id), Harness: "pi", Mode: string(domain.LaunchTUI)},
			{AccountMemberID: string(e.ada.id), Harness: "omp", Mode: string(domain.LaunchTUI)},
			{AccountMemberID: string(e.bo.id), Harness: "claude", Mode: string(domain.LaunchTUI)},
		},
		MaxConcurrentAttempts: 2, MaxTotalAttempts: 4,
		IdempotencyKey: "docker-mission-create",
	}, &created); err != nil {
		t.Fatalf("mission.create: %v", err)
	}
	missionID, integratorRun := created.Mission.ID, created.Mission.CurrentIntegratorRunID
	integratorSocket := waitMissionSocket(t, e.coordDir(integratorRun))

	propose := func(objective, title, key string) string {
		var out protocol.TaskMutationResult
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodTaskPropose, protocol.TaskProposeParams{
			MissionID: missionID,
			Revision: protocol.TaskRevision{
				Title: title, Objective: objective, Status: string(domain.TaskRevisionProposed),
			},
			IdempotencyKey: key,
		}, &out); err != nil {
			t.Fatalf("task.propose %s: %v", title, err)
		}
		if out.Task.ID == "" || out.Task.CurrentRevision != 1 {
			t.Fatalf("task.propose %s returned incomplete task: %+v", title, out.Task)
		}
		return out.Task.ID
	}
	taskA := propose(workerAObjective, "Docker worker A", "docker-propose-a")
	taskB := propose(workerBObjective, "Docker worker B", "docker-propose-b")
	for _, taskID := range []string{taskA, taskB} {
		var out protocol.TaskMutationResult
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
			TaskID: taskID, Revision: 1,
			ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
			IdempotencyKey:               "docker-accept-" + taskID,
		}, &out); err != nil {
			t.Fatalf("task.accept %s: %v", taskID, err)
		}
	}

	start := func(taskID, dispatch string) protocol.WorkerStartResult {
		var out protocol.WorkerStartResult
		harness := "pi"
		if taskID == taskB {
			harness = "omp"
		}
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
			MissionID: missionID, TaskID: taskID, TaskRevision: 1, DispatchKey: dispatch,
			Harness: harness, Mode: string(domain.LaunchTUI),
			AccountOwnerID: string(e.ada.id), RunOwnerID: string(e.ada.id),
			ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
		}, &out); err != nil {
			t.Fatalf("worker.start %s: %v", dispatch, err)
		}
		if out.Attempt.RunID == "" {
			t.Fatalf("worker.start %s returned no run: %+v", dispatch, out)
		}
		return out
	}
	attemptA := start(taskA, "docker-dispatch-a")
	attemptB := start(taskB, "docker-dispatch-b")
	attA := openAttach(t, adaClient, attemptA.Attempt.RunID)
	attB := openAttach(t, adaClient, attemptB.Attempt.RunID)
	attA.waitOutput(t, "fixture-sent:")
	attB.waitOutput(t, "fixture-sent:")
	attA.waitOutput(t, "fixture-acked:")
	attB.waitOutput(t, "fixture-acked:")
	// These lines are emitted by two different shell fixtures only after each
	// has observed a peer and completed the durable send/inbox exchange.
	attA.waitOutput(t, "fixture-reported:")
	attB.waitOutput(t, "fixture-reported:")

	shown := waitMissionSubmissions(ctx, t, adaCtrl, missionID, 2)
	if len(shown.Submissions) != 2 {
		t.Fatalf("mission submissions = %d, want two retained submissions", len(shown.Submissions))
	}
	// Acceptance is human-controlled by the current integrator run. The
	// accepted-set version is advanced and fed back for the next acceptance.
	acceptedOrder := make([]protocol.Submission, 0, len(shown.Submissions))
	for _, sub := range shown.Submissions {
		var accepted protocol.TaskMutationResult
		if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodTaskAcceptSubmission, protocol.TaskAcceptSubmissionParams{
			SubmissionID:                 sub.ID,
			ExpectedIntegratorGeneration: created.Mission.IntegratorGeneration,
			ExpectedAcceptedSetVersion:   shown.Mission.AcceptedSetVersion,
			IdempotencyKey:               "docker-accept-submission-" + sub.ID,
		}, &accepted); err != nil {
			t.Fatalf("task.accept-submission %s: %v", sub.ID, err)
		}
		if accepted.Acceptance == nil {
			t.Fatalf("task.accept-submission %s omitted durable acceptance", sub.ID)
		}
		shown.Mission.AcceptedSetVersion = accepted.Acceptance.AcceptedSetVersion
		acceptedOrder = append(acceptedOrder, sub)
	}

	base := runGit(t, filepath.Join(e.dataDir, "repos", string(e.ws.ID)+".git"), nil, "rev-parse", "refs/heads/main")
	prepareParams := map[string]any{
		// Workspace, mission, and submissions are intentionally omitted. The
		// server derives all three from this integrator's authenticated run.
		"target_ref": "refs/heads/main", "expected_target_revision": base,
		"idempotency_key": "docker-candidate-prepare",
	}
	candidate := runMissionIntegrationCLI(t, integratorRun, "prepare", prepareParams)
	if candidate.MissionID != missionID || candidate.WorkspaceID != string(e.ws.ID) ||
		candidate.MissionAcceptedSetVersion != shown.Mission.AcceptedSetVersion ||
		len(candidate.Submissions) != len(acceptedOrder) || candidate.State != protocol.CandidateFrozen {
		t.Fatalf("prepared candidate did not bind current mission refs/version: %+v", candidate)
	}
	for i, want := range acceptedOrder {
		if candidate.Submissions[i].RunID != want.Ref.RunID ||
			candidate.Submissions[i].EvidenceRef != want.Ref.EvidenceRef ||
			candidate.Submissions[i].RetainedRevision != want.Ref.RetainedRevision {
			t.Fatalf("candidate submission[%d] = %+v, want accepted ref %+v", i, candidate.Submissions[i], want.Ref)
		}
	}
	if replay := runMissionIntegrationCLI(t, integratorRun, "prepare", prepareParams); replay.CandidateID != candidate.CandidateID {
		t.Fatalf("prepare replay changed candidate identity: first=%s replay=%s", candidate.CandidateID, replay.CandidateID)
	}
	if _, err := runMissionIntegrationCLIExpectError(t, integratorRun, "prepare", map[string]any{
		"mission_id": "mission-not-current", "target_ref": "refs/heads/main",
		"expected_target_revision": base, "idempotency_key": "docker-conflicting-mission",
	}); err == nil {
		t.Fatal("prepare with conflicting mission_id unexpectedly succeeded")
	}
	shownCandidate := runMissionIntegrationCLI(t, integratorRun, "show", map[string]any{"candidate_id": candidate.CandidateID})
	if shownCandidate.CandidateID != candidate.CandidateID || shownCandidate.MissionID != missionID {
		t.Fatalf("integration.show did not preserve server-filled mission binding: %+v", shownCandidate)
	}

	failed := runMissionIntegrationCLI(t, integratorRun, "verify", map[string]any{
		"candidate_id": candidate.CandidateID, "candidate_revision": candidate.CandidateRevision,
		"argv":            []string{"sh", "-c", "printf 'fixture-failure\\n'; exit 17"},
		"timeout_seconds": 30, "idempotency_key": "docker-verify-failed",
	})
	if !hasVerificationStatus(failed, protocol.VerificationFailed) {
		t.Fatalf("failed verification was not durably recorded: %+v", failed.Verifications)
	}
	passed := runMissionIntegrationCLI(t, integratorRun, "verify", map[string]any{
		"candidate_id": candidate.CandidateID, "candidate_revision": candidate.CandidateRevision,
		// Verification also proves the canonical staged CLI exists in the
		// verification image and has no run identity/socket.
		"argv":            []string{"sh", "-c", "set -eu; /usr/local/bin/aether-internal --help >/tmp/help; /usr/local/bin/aether-internal skill >/tmp/skill; grep -q 'No coordination socket' /tmp/skill; test \"$(ls worker-*.txt | wc -l)\" = 2"},
		"timeout_seconds": 30, "idempotency_key": "docker-verify-passed",
	})
	if !hasVerificationStatus(passed, protocol.VerificationPassed) {
		t.Fatalf("successful verification was not durably recorded: %+v", passed.Verifications)
	}
	verificationIDs := make([]string, 0, len(passed.Verifications))
	for _, verification := range passed.Verifications {
		if verification.Status == protocol.VerificationPassed {
			verificationIDs = append(verificationIDs, verification.VerificationID)
		}
	}
	if len(verificationIDs) != 1 {
		t.Fatalf("passed verification IDs = %v, want one", verificationIDs)
	}

	if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodIntegrationDecide, protocol.IntegrationDecideParams{
		WorkspaceID: string(e.ws.ID), CandidateID: candidate.CandidateID,
	}, nil); err == nil {
		t.Fatal("integrator agent socket could decide delivery; approval is human-only")
	}
	requested := runMissionIntegrationCLI(t, integratorRun, "request-delivery", map[string]any{
		"candidate_id": candidate.CandidateID, "candidate_revision": candidate.CandidateRevision,
		"verification_ids": verificationIDs, "action": string(protocol.DeliveryActionUpdateRef),
		"idempotency_key": "docker-request-delivery",
	})
	if requested.DeliveryRequest == nil || requested.DeliveryRequest.State != protocol.DeliveryPending {
		t.Fatalf("request-delivery = %+v, want pending request", requested.DeliveryRequest)
	}
	// Disconnect the human SSH client before approval and reconnect it. The
	// durable candidate/request are the only state used after this boundary.
	_ = adaClient.Close()
	adaCtrl, adaClient = srv.control(t, e.ada.key)
	defer adaClient.Close()
	var decided protocol.IntegrationDecideResult
	if err := adaCtrl.Call(protocol.MethodIntegrationDecide, protocol.IntegrationDecideParams{
		WorkspaceID: string(e.ws.ID), CandidateID: candidate.CandidateID,
		RequestID:      requested.DeliveryRequest.RequestID,
		RequestVersion: requested.DeliveryRequest.RequestVersion, Approve: true,
	}, &decided); err != nil {
		t.Fatalf("human integration.decide after reconnect: %v", err)
	}
	if decided.Candidate.DeliveryRequest == nil || decided.Candidate.DeliveryRequest.State != protocol.DeliveryApproved {
		t.Fatalf("human approval result = %+v", decided.Candidate.DeliveryRequest)
	}

	// Move the target ref away from the reviewed revision: delivery must fail
	// closed rather than landing a candidate against an unexpected target.
	if err := runGitUpdateRef(t, filepath.Join(e.dataDir, "repos", string(e.ws.ID)+".git"), "refs/heads/main", candidate.Submissions[0].RetainedRevision); err != nil {
		t.Fatalf("advance stale target ref: %v", err)
	}
	if _, err := runMissionIntegrationCLIExpectError(t, integratorRun, "deliver", map[string]any{
		"candidate_id": candidate.CandidateID, "request_id": requested.DeliveryRequest.RequestID,
		"request_version": requested.DeliveryRequest.RequestVersion,
	}); err == nil {
		t.Fatal("delivery against stale target unexpectedly succeeded")
	}
	if err := runGitUpdateRef(t, filepath.Join(e.dataDir, "repos", string(e.ws.ID)+".git"), "refs/heads/main", base); err != nil {
		t.Fatalf("restore reviewed target ref: %v", err)
	}
	delivered := runMissionIntegrationCLI(t, integratorRun, "deliver", map[string]any{
		"candidate_id": candidate.CandidateID, "request_id": requested.DeliveryRequest.RequestID,
		"request_version": requested.DeliveryRequest.RequestVersion,
	})
	if delivered.DeliveryReceipt == nil ||
		delivered.DeliveryReceipt.Result != protocol.DeliveryResultLanded ||
		delivered.DeliveryReceipt.TargetRef != "refs/heads/main" ||
		delivered.DeliveryReceipt.PreviousRevision != base ||
		delivered.DeliveryReceipt.CandidateRevision != candidate.CandidateRevision {
		t.Fatalf("delivery receipt = %+v, want exact landed candidate", delivered.DeliveryReceipt)
	}
	if got := runGit(t, filepath.Join(e.dataDir, "repos", string(e.ws.ID)+".git"), nil, "rev-parse", "refs/heads/main"); got != candidate.CandidateRevision {
		t.Fatalf("main after exact delivery = %s, want candidate %s", got, candidate.CandidateRevision)
	}

	// Replacement starts a new integrator run and retires the disconnected
	// run's authority; its old socket cannot mutate or inspect this mission.
	var replaced protocol.MissionReplaceIntegratorResult
	if err := boCtrl.Call(protocol.MethodMissionReplaceIntegrator, protocol.MissionReplaceIntegratorParams{
		MissionID: missionID, ExpectedGeneration: created.Mission.IntegratorGeneration,
		Integrator:     protocol.MissionIntegrator{AccountMemberID: string(e.bo.id), Harness: "claude", Mode: string(domain.LaunchTUI)},
		IdempotencyKey: "docker-replace-integrator",
	}, &replaced); err != nil {
		t.Fatalf("mission.replace-integrator after delivery: %v", err)
	}
	if replaced.RunID == "" || replaced.RunID == integratorRun ||
		replaced.Mission.IntegratorGeneration <= created.Mission.IntegratorGeneration {
		t.Fatalf("integrator replacement = %+v, want new generation/run", replaced)
	}
	// The replacement has a fresh, restarted integrator socket; the retired
	// socket remains present only as a durable stale identity.
	_ = waitMissionSocket(t, e.coordDir(replaced.RunID))
	var staleList protocol.WorkerListResult
	if err := coordtransport.Call(ctx, integratorSocket, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: missionID}, &staleList); err == nil {
		t.Fatal("replaced integrator socket remained authorized")
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

func runMissionIntegrationCLI(t *testing.T, runID, operation string, params any) protocol.Candidate {
	t.Helper()
	candidate, err := runMissionIntegrationCLIResult(t, runID, operation, params)
	if err != nil {
		t.Fatalf("integration %s: %v", operation, err)
	}
	return candidate
}

func runMissionIntegrationCLIExpectError(t *testing.T, runID, operation string, params any) (protocol.Candidate, error) {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		return protocol.Candidate{}, err
	}
	cmd := exec.CommandContext(t.Context(), "docker", "exec", "-i", "aether-run-"+runID,
		"/usr/local/bin/aether-internal", "integration", operation,
		"--params-file", "-", "--json")
	cmd.Stdin = bytes.NewReader(data)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return protocol.Candidate{}, fmt.Errorf("cli exit: %w: %s", runErr, strings.TrimSpace(string(out)))
	}
	var envelope missionCLIEnvelope
	if err := json.Unmarshal(out, &envelope); err != nil {
		return protocol.Candidate{}, fmt.Errorf("decode CLI response %q: %w", out, err)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return protocol.Candidate{}, fmt.Errorf("integration %s failed without an error", operation)
		}
		return protocol.Candidate{}, fmt.Errorf("integration %s: %s", operation, envelope.Error.Message)
	}
	var result protocol.IntegrationPrepareResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return protocol.Candidate{}, fmt.Errorf("decode integration %s result: %w", operation, err)
	}
	return result.Candidate, nil
}

func runMissionIntegrationCLIResult(t *testing.T, runID, operation string, params any) (protocol.Candidate, error) {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		return protocol.Candidate{}, err
	}
	cmd := exec.CommandContext(t.Context(), "docker", "exec", "-i", "aether-run-"+runID,
		"/usr/local/bin/aether-internal", "integration", operation,
		"--params-file", "-", "--json")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		return protocol.Candidate{}, fmt.Errorf("cli exit: %w", err)
	}
	var envelope missionCLIEnvelope
	if err := json.Unmarshal(out, &envelope); err != nil {
		return protocol.Candidate{}, fmt.Errorf("decode CLI response %q: %w", out, err)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return protocol.Candidate{}, fmt.Errorf("integration %s failed without an error", operation)
		}
		return protocol.Candidate{}, fmt.Errorf("integration %s: %s", operation, envelope.Error.Message)
	}
	var result struct {
		Candidate protocol.Candidate `json:"candidate"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return protocol.Candidate{}, fmt.Errorf("decode integration %s result: %w", operation, err)
	}
	return result.Candidate, nil
}

func waitMissionSubmissions(ctx context.Context, t *testing.T, ctrl *protocol.Client, missionID string, want int) protocol.MissionShowResult {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var shown protocol.MissionShowResult
		if err := ctrl.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &shown); err == nil && len(shown.Submissions) >= want {
			return shown
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d durable mission submissions", want)
		case <-ticker.C:
		}
	}
}

func hasVerificationStatus(candidate protocol.Candidate, want protocol.VerificationStatus) bool {
	for _, verification := range candidate.Verifications {
		if verification.Status == want {
			return true
		}
	}
	return false
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
task=mission
for arg in "$@"; do task=$arg; done

help=$(/usr/local/bin/aether-internal --help)
case "$help" in *"aether-internal"*) ;; *) echo "cli-help-missing" >&2; exit 1 ;; esac
skill=$(/usr/local/bin/aether-internal skill)
case "$skill" in *"Run:"*) ;; *) echo "cli-skill-missing" >&2; exit 1 ;; esac

case "$task" in
	*"integrator"*)
		echo "fixture-integrator-ready"
		while :; do sleep 60; done
		;;
esac

case "$task" in
	*"worker A"*) result="worker-a.txt"; body="worker-A-before-edit" ;;
	*"worker B"*) result="worker-b.txt"; body="worker-B-before-edit" ;;
	*) result="worker-other.txt"; body="worker-before-edit" ;;
esac

# The marker is only to enter the radar. The substantive result is written
# after both fixtures have exchanged messages, and each fixture removes the
# marker before reporting so candidate assembly has no shared-file conflict.
printf '%s:marker\n' "$AETHER_RUN_ID" > shared.txt
peer=
attempt=0
while [ "$attempt" -lt 120 ]; do
	status=$(/usr/local/bin/aether-internal status --json)
	peer=$(printf '%s\n' "$status" | sed -nE 's/.*"peers":\[\{"run_id":"([^"]*)".*/\1/p')
	if [ -n "$peer" ]; then break; fi
	attempt=$((attempt + 1))
	sleep 1
done
[ -n "$peer" ] || { echo "fixture-peer-not-found" >&2; exit 1; }
/usr/local/bin/aether-internal send --to "$peer" --body "$body" --idempotency-key "mission-send-$AETHER_RUN_ID" >/dev/null
echo "fixture-sent:$AETHER_RUN_ID"
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
rm -f shared.txt
printf '%s\n' "$body" > "$result"
/usr/local/bin/aether-internal report --outcome success --summary "mission fixture retained result" --idempotency-key "mission-report-$AETHER_RUN_ID" >/dev/null
echo "fixture-reported:$AETHER_RUN_ID"
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
