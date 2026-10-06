//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestIntegrationEnhancedMissionWake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env := startEnhancedServer(ctx, t)
	defer env.stop(t)
	ada, err := env.srv.Store().GetMemberByTailnetLogin(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}

	enhanced := protocol.MissionExecutionChoice{AccountMemberID: string(ada.ID), Harness: "fake", Mode: string(domain.LaunchACP)}
	params, _ := json.Marshal(protocol.MissionCreateParams{
		WorkspaceID: string(env.ws.ID), Objective: "enhanced swarm", AccountableHumanID: string(ada.ID),
		Integrator:       protocol.MissionIntegrator(enhanced),
		ExecutionChoices: []protocol.MissionExecutionChoice{enhanced},
		IdempotencyKey:   "enhanced-swarm",
	})
	var created protocol.MissionCreateResult
	if status := postJSON(t, env.web+"/api/v1/mission.create", string(params), &created); status != http.StatusOK {
		t.Fatalf("mission.create status %d", status)
	}
	missionID, generation := created.Mission.ID, created.Mission.IntegratorGeneration
	integratorRun := domain.RunID(created.Mission.CurrentIntegratorRunID)
	integrator := waitMissionSocket(t, filepath.Join(env.data, "coord", string(integratorRun)))
	waitTurns(ctx, t, env.srv, integratorRun, 1)

	var asked protocol.MissionQuestionResult
	if err := pacedCall(ctx, integrator, protocol.MethodMissionQuestionAsk, protocol.MissionQuestionAskParams{
		Body: "which login flow?", IdempotencyKey: "ask",
	}, &asked); err != nil {
		t.Fatalf("mission.question.ask: %v", err)
	}
	answer, _ := json.Marshal(protocol.MissionQuestionAnswerParams{QuestionID: asked.Question.ID, Answer: "the SSO one", IdempotencyKey: "answer"})
	var answered protocol.MissionQuestionResult
	if status := postJSON(t, env.web+"/api/v1/"+protocol.MethodMissionQuestionAnswer, string(answer), &answered); status != http.StatusOK {
		t.Fatalf("mission.question.answer status %d", status)
	}
	waitTurns(ctx, t, env.srv, integratorRun, 2)
	waitPrompted(ctx, t, env.srv, integratorRun, protocol.CoordMissionUpdateContext(missionID, []domain.MissionChange{domain.MissionQuestionAnswered}))

	var proposed protocol.TaskMutationResult
	if err := pacedCall(ctx, integrator, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID: missionID, IdempotencyKey: "propose",
		Revision: protocol.TaskRevision{
			Title: "worker", Objective: "say pong", Status: string(domain.TaskRevisionProposed),
			EvidenceRequirements: []protocol.EvidenceRequirement{{Kind: "test", Detail: "none"}},
		},
	}, &proposed); err != nil {
		t.Fatalf("task.propose: %v", err)
	}
	if err := pacedCall(ctx, integrator, protocol.MethodMissionStart, protocol.MissionStartParams{MissionID: missionID, IdempotencyKey: "start"}, nil); err != nil {
		t.Fatalf("mission.start: %v", err)
	}
	var started protocol.WorkerStartResult
	if err := pacedCall(ctx, integrator, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: missionID, TaskID: proposed.Task.ID, TaskRevision: 1, DispatchKey: "worker",
		Harness: "fake", Mode: string(domain.LaunchACP), AccountOwnerID: string(ada.ID), RunOwnerID: string(ada.ID),
		ExpectedIntegratorGeneration: generation,
	}, &started); err != nil {
		t.Fatalf("worker.start: %v", err)
	}
	worker := domain.RunID(started.Attempt.RunID)
	waitTurns(ctx, t, env.srv, worker, 1)

	if err := pacedCall(ctx, integrator, protocol.MethodCoordSend, protocol.CoordSendParams{
		ToRunID: string(worker), Body: "check the login test", IdempotencyKey: "nudge",
	}, nil); err != nil {
		t.Fatalf("coord.send: %v", err)
	}
	waitTurns(ctx, t, env.srv, worker, 2)
	waitPrompted(ctx, t, env.srv, worker, protocol.CoordInboxContext(1))

	workerSocket := waitMissionSocket(t, filepath.Join(env.data, "coord", string(worker)))
	if err := pacedCall(ctx, workerSocket, protocol.MethodCoordReport, protocol.CoordReportParams{
		Outcome: protocol.CoordOutcomeSuccess, Summary: "said pong", IdempotencyKey: "worker-report",
	}, nil); err != nil {
		t.Fatalf("worker coord.report: %v", err)
	}
	waitPrompted(ctx, t, env.srv, integratorRun, protocol.CoordInboxContext(1))

	for _, run := range []domain.RunID{worker, integratorRun} {
		if err := env.srv.sched.Kill(ctx, run, ada.ID); err != nil {
			t.Fatalf("kill %s: %v", run, err)
		}
	}
}

func waitTurns(ctx context.Context, t *testing.T, srv *Server, run domain.RunID, n int) []acphost.Item {
	t.Helper()
	for {
		items, err := srv.sched.ACPHistory(run, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		ended := 0
		for _, it := range items {
			if it.Kind == acphost.KindTurnEnd && it.StopReason == "end_turn" {
				ended++
			}
		}
		if ended >= n {
			return items
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run %s ended %d of %d turns: %+v", run, ended, n, items)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitPrompted(ctx context.Context, t *testing.T, srv *Server, run domain.RunID, instruction string) {
	t.Helper()
	for {
		items, err := srv.sched.ACPHistory(run, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.Kind == acphost.KindMessage && it.Message.Role == "user" && strings.Contains(it.Message.Text, strings.TrimSpace(instruction)) {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run %s was never prompted with %q: %+v", run, instruction, items)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
