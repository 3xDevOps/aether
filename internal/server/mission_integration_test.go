//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
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
