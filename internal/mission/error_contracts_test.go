package mission

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type integratorLookupFailure struct {
	store.MissionStore
	err error
}

func (s integratorLookupFailure) GetMissionByRun(context.Context, domain.RunID) (*domain.Mission, error) {
	return nil, s.err
}

func TestIntegratorOperationsDenyWorkersAndOrdinaryRuns(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "task", title: "bounded task"})
	if err := f.startWorker(t, tasks[0], "worker"); err != nil {
		t.Fatal(err)
	}
	attempts, err := f.db.ListAttempts(ctx, f.mission.ID, tasks[0].ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("initial attempts = %+v, error %v", attempts, err)
	}
	tasks[0], err = f.db.GetTask(ctx, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := domain.RunID("ordinary-run")
	regressionRun(t, f.db, ordinary, f.workspace.ID, f.member.ID, "ordinary")
	for _, run := range []domain.RunID{attempts[0].RunID, ordinary} {
		for method, params := range map[string]any{
			protocol.MethodWorkerList: protocol.WorkerListParams{MissionID: string(f.mission.ID)},
			protocol.MethodWorkerStart: protocol.WorkerStartParams{
				MissionID: string(f.mission.ID), TaskID: string(tasks[0].ID), TaskRevision: 1, DispatchKey: "unauthorized-start",
			},
			protocol.MethodWorkerRetry: protocol.WorkerRetryParams{
				AttemptID: string(attempts[0].ID), DispatchKey: "unauthorized-retry", ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
			},
			protocol.MethodWorkerCancel: protocol.WorkerCancelParams{
				AttemptID: string(attempts[0].ID), IdempotencyKey: "unauthorized-cancel", ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
			},
			protocol.MethodMissionPlanShow: protocol.MissionPlanShowParams{},
		} {
			out, callErr := f.call(t, run, method, params)
			if !errors.Is(callErr, permissions.ErrDenied) || out != nil {
				t.Fatalf("%s by %s = %#v, %v; want denied without data", method, run, out, callErr)
			}
		}
	}
	for method, params := range map[string]any{
		protocol.MethodTaskAccept: protocol.TaskAcceptParams{
			TaskID: string(tasks[0].ID), Revision: 1, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration, IdempotencyKey: "worker-accept",
		},
		protocol.MethodTaskAbandon: protocol.TaskAbandonParams{
			TaskID: string(tasks[0].ID), Revision: 1, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration, IdempotencyKey: "worker-abandon",
		},
		protocol.MethodTaskAcceptSubmission: protocol.TaskAcceptSubmissionParams{
			SubmissionID: "not-visible", ExpectedIntegratorGeneration: f.mission.IntegratorGeneration, IdempotencyKey: "worker-accept-submission",
		},
	} {
		out, callErr := f.call(t, attempts[0].RunID, method, params)
		if out != nil || !errors.Is(callErr, permissions.ErrDenied) {
			t.Fatalf("%s by worker = %#v, %v; want denied without data", method, out, callErr)
		}
	}
	current, err := f.db.GetTask(ctx, tasks[0].ID)
	if err != nil || !reflect.DeepEqual(tasks[0], current) {
		t.Fatalf("unauthorized task operations changed task: before=%+v after=%+v error=%v", tasks[0], current, err)
	}
	after, err := f.db.ListAttempts(ctx, f.mission.ID, tasks[0].ID)
	if err != nil || !reflect.DeepEqual(attempts, after) {
		t.Fatalf("unauthorized operations changed attempts: before=%+v after=%+v error=%v", attempts, after, err)
	}
}

func TestIntegratorLookupPreservesStorageFailureAndAuthority(t *testing.T) {
	f := newPlanGateFixture(t)
	cause := errors.New("mission index unavailable")
	original := f.svc.cfg.Missions
	f.svc.cfg.Missions = integratorLookupFailure{MissionStore: original, err: fmt.Errorf("read integrator: %w", cause)}
	out, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: string(f.mission.ID)})
	if out != nil || !errors.Is(err, cause) || errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("failed lookup = %#v, %v; want original storage failure", out, err)
	}
	f.svc.cfg.Missions = original
	out, err = f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: "another-mission"})
	if out != nil || !errors.Is(err, store.ErrMissionStale) {
		t.Fatalf("outside mission = %#v, %v; want stale authority without data", out, err)
	}
	retired := f.mission.CurrentIntegratorRunID
	if _, replaceErr := f.db.ReplaceIntegrator(context.Background(), f.mission.ID, f.mission.IntegratorGeneration,
		f.mission.Integrator, f.member.ID, f.member.ID, "replace"); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	out, err = f.call(t, retired, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: string(f.mission.ID)})
	if out != nil || !errors.Is(err, store.ErrMissionStale) {
		t.Fatalf("retired integrator = %#v, %v; want stale authority without data", out, err)
	}
}

func TestTaskAcceptanceReplayPreservesReceiptAfterConflict(t *testing.T) {
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "task", title: "bounded task"})
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(tasks[0].ID), Revision: protocol.TaskRevision{Title: "clarified task", Objective: "same bounded work"}, IdempotencyKey: "revise",
	})
	params := protocol.TaskAcceptParams{
		TaskID: string(tasks[0].ID), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration, IdempotencyKey: "accept",
	}
	accepted := f.mustCall(t, protocol.MethodTaskAccept, params)
	replayed := f.mustCall(t, protocol.MethodTaskAccept, params)
	if !reflect.DeepEqual(accepted, replayed) {
		t.Fatalf("same-key acceptance changed receipt: first=%+v replay=%+v", accepted, replayed)
	}
	params.IdempotencyKey = "reaccept"
	out, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodTaskAccept, params)
	if !errors.Is(err, store.ErrConflict) || out != nil {
		t.Fatalf("new-key reacceptance = %#v, %v; want conflict", out, err)
	}
	params.IdempotencyKey = "accept"
	if replayed := f.mustCall(t, protocol.MethodTaskAccept, params); !reflect.DeepEqual(accepted, replayed) {
		t.Fatalf("conflict changed original acceptance receipt: first=%+v replay=%+v", accepted, replayed)
	}
}

func TestWorkerRetryWaitsForCancellationSettlement(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "task", title: "bounded task"})
	if err := f.startWorker(t, tasks[0], "worker"); err != nil {
		t.Fatal(err)
	}
	attempts, err := f.db.ListAttempts(ctx, f.mission.ID, tasks[0].ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("initial attempts = %+v, error %v", attempts, err)
	}
	attempt := attempts[0]
	params := protocol.WorkerRetryParams{
		AttemptID: string(attempt.ID), DispatchKey: "retry", ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
	}
	for _, cancelling := range []bool{false, true} {
		if cancelling {
			if _, _, cancelErr := f.db.RequestAttemptCancellation(ctx, attempt.ID, f.mission.CurrentIntegratorRunID, f.mission.IntegratorGeneration, "cancel"); cancelErr != nil {
				t.Fatal(cancelErr)
			}
		}
		out, retryErr := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodWorkerRetry, params)
		var rpcErr *protocol.Error
		if out != nil || !errors.As(retryErr, &rpcErr) || rpcErr.Code != protocol.CodeInvalidState {
			t.Fatalf("retry while cancelling=%t = %#v, %v; want invalid state", cancelling, out, retryErr)
		}
		after, listErr := f.db.ListAttempts(ctx, f.mission.ID, tasks[0].ID)
		if listErr != nil || len(after) != 1 || after[0].ID != attempt.ID || !after[0].State.HoldsConcurrency() || (after[0].CancelRequestedAt != nil) != cancelling {
			t.Fatalf("refused retry altered active attempt: %+v, error %v", after, listErr)
		}
	}
	if settleErr := f.db.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCancelled, "settled"); settleErr != nil {
		t.Fatal(settleErr)
	}
	out := f.mustCall(t, protocol.MethodWorkerRetry, params).(protocol.WorkerStartResult)
	if out.Attempt.ID == string(attempt.ID) || out.Attempt.RunID == string(attempt.RunID) {
		t.Fatalf("settled retry reused cancelled attempt: %+v", out)
	}
	replay := f.mustCall(t, protocol.MethodWorkerRetry, params).(protocol.WorkerStartResult)
	if !replay.Replayed || replay.Attempt.ID != out.Attempt.ID || replay.Attempt.RunID != out.Attempt.RunID {
		t.Fatalf("retry replay duplicated attempt: first=%+v replay=%+v", out, replay)
	}
	after, err := f.db.ListAttempts(ctx, f.mission.ID, tasks[0].ID)
	if err != nil || len(after) != 2 {
		t.Fatalf("settled retry attempts = %+v, error %v; want exactly two", after, err)
	}
}
