//go:build integration

package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/quota"
)

// stubClaudeUsage reports each account's used share of its Claude five-hour
// window; an account it has no figure for reads 0%.
type stubClaudeUsage struct {
	mu   sync.Mutex
	used map[domain.MemberID]float64
}

func (u *stubClaudeUsage) set(account domain.MemberID, used float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.used[account] = used
}

func (u *stubClaudeUsage) Read(_ context.Context, account domain.MemberID, _ bool) ([]quota.Provider, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	resets := time.Now().Add(time.Hour)
	return []quota.Provider{{Provider: "claude", Status: "ok", Windows: []quota.Window{
		{ID: "five_hour", UsedPercent: u.used[account], ResetsAt: &resets},
	}}}, nil
}

// TestIntegrationWorkerFallsBackFromExhaustedAccount: Bo shares his account
// with Ada, whose swarm declares a Claude worker on it. Bo's usage is
// exhausted, so the worker launches on Ada's own account; the attempt keeps
// Bo as its declared account and records Ada's as the one it launched on,
// and the run records Bo as the account Ada asked for.
func TestIntegrationWorkerFallsBackFromExhaustedAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	usage := &stubClaudeUsage{used: map[domain.MemberID]float64{}}
	e := &coordEnv{rt: newE2ERuntime(), image: "e2e/fake", usage: usage}
	srv := e.seed(ctx, t, false)
	if err := srv.srv.Store().ShareAccount(ctx, e.bo.id, e.ada.id); err != nil {
		t.Fatalf("share Bo's account with Ada: %v", err)
	}
	usage.set(e.bo.id, 100)
	usage.set(e.ada.id, 10)

	adaCtrl, adaClient := srv.control(t, e.ada.key)
	defer adaClient.Close()
	integrator := protocol.MissionExecutionChoice{AccountMemberID: string(e.ada.id), Harness: "fake", Mode: string(domain.LaunchTUI)}
	worker := protocol.MissionExecutionChoice{AccountMemberID: string(e.bo.id), Harness: "claude", Mode: string(domain.LaunchHeadless)}
	var created protocol.MissionCreateResult
	if err := adaCtrl.Call(protocol.MethodMissionCreate, protocol.MissionCreateParams{
		WorkspaceID: string(e.ws.ID), Objective: "fallback swarm integrator",
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: []protocol.MissionExecutionChoice{integrator, worker},
		IdempotencyKey:   "fallback-swarm",
	}, &created); err != nil {
		t.Fatalf("mission.create: %v", err)
	}
	missionID := created.Mission.ID
	socket := waitMissionSocket(t, e.coordDir(created.Mission.CurrentIntegratorRunID))

	var proposed protocol.TaskMutationResult
	if err := pacedCall(ctx, socket, protocol.MethodTaskPropose, protocol.TaskProposeParams{
		MissionID: missionID,
		Revision: protocol.TaskRevision{
			Title: "fallback worker", Objective: "fallback worker",
			EvidenceRequirements: []protocol.EvidenceRequirement{{Kind: "transcript"}},
		},
		IdempotencyKey: "propose-fallback",
	}, &proposed); err != nil {
		t.Fatalf("task.propose: %v", err)
	}
	if err := pacedCall(ctx, socket, protocol.MethodMissionStart, protocol.MissionStartParams{
		MissionID: missionID, IdempotencyKey: "start-fallback",
	}, nil); err != nil {
		t.Fatalf("mission.start: %v", err)
	}
	var started protocol.WorkerStartResult
	if err := pacedCall(ctx, socket, protocol.MethodWorkerStart, protocol.WorkerStartParams{
		MissionID: missionID, TaskID: proposed.Task.ID, TaskRevision: 1, DispatchKey: "fallback-worker",
		Harness: worker.Harness, Mode: worker.Mode, AccountOwnerID: worker.AccountMemberID,
	}, &started); err != nil {
		t.Fatalf("worker.start: %v", err)
	}
	if started.Attempt.AccountOwnerID != string(e.bo.id) || started.Attempt.LaunchAccountID != string(e.ada.id) {
		t.Fatalf("attempt accounts = declared %s, launched %s; want Bo %s, Ada %s",
			started.Attempt.AccountOwnerID, started.Attempt.LaunchAccountID, e.bo.id, e.ada.id)
	}
	var run protocol.RunResult
	if err := adaCtrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: started.Attempt.RunID}, &run); err != nil {
		t.Fatalf("run.get worker: %v", err)
	}
	if run.Run.AccountMemberID != string(e.ada.id) || run.Run.RequestedAccountMemberID != string(e.bo.id) {
		t.Fatalf("worker run account %s requested %s; want Ada %s requested Bo %s",
			run.Run.AccountMemberID, run.Run.RequestedAccountMemberID, e.ada.id, e.bo.id)
	}
}
