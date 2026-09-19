package mission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

func (s *Service) workerStart(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	return s.workerStartInternal(ctx, run, raw, false)
}

func (s *Service) workerStartInternal(ctx context.Context, run domain.RunID, raw json.RawMessage, authorizationHeld bool) (any, error) {
	var p protocol.WorkerStartParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.TaskID == "" || p.TaskRevision <= 0 || p.DispatchKey == "" {
		return nil, errors.New("mission: worker.start requires task, revision, and dispatch_key")
	}
	if !authorizationHeld {
		s.cfg.AuthorizationMu.Lock()
		defer s.cfg.AuthorizationMu.Unlock()
	}
	// Resolve the assignment only after taking the shared authorization lock:
	// replacement cannot race the account/share decision below.
	m, err := s.integratorMission(ctx, run, p.MissionID)
	if err != nil {
		return nil, err
	}
	task, err := s.cfg.Missions.GetTask(ctx, domain.TaskID(p.TaskID))
	if err != nil {
		return nil, err
	}
	if task.MissionID != m.ID || task.Revision == nil || task.CurrentRevision != p.TaskRevision {
		return nil, errors.New("mission: task revision is not current")
	}
	var choice domain.MissionExecutionChoice
	found := false
	for _, c := range m.ExecutionChoices {
		if c.Harness == p.Harness && string(c.Mode) == p.Mode && string(c.AccountMemberID) == p.AccountOwnerID {
			choice, found = c, true
			break
		}
	}
	if !found {
		return nil, errors.New("mission: worker choice is not authorized")
	}
	if p.ExpectedIntegratorGeneration != 0 && p.ExpectedIntegratorGeneration != m.IntegratorGeneration {
		return nil, fmt.Errorf("%w: stale integrator authority", store.ErrMissionStale)
	}
	admission, err := sshd.AuthorizeLaunch(ctx, s.cfg.Store, m.AccountableHumanID, p.AccountOwnerID)
	if err != nil {
		return nil, err
	}
	if admission.Account.ID != choice.AccountMemberID {
		return nil, errors.New("mission: account attribution changed")
	}
	if p.RunOwnerID != "" && p.RunOwnerID != string(m.AccountableHumanID) {
		return nil, errors.New("mission: run_owner_id cannot impersonate another owner")
	}
	if coordinationErr := s.requireCoordination(); coordinationErr != nil {
		return nil, coordinationErr
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	attempt, replayed, err := s.cfg.Missions.ReserveAttempt(ctx, &domain.AttemptReservation{
		MissionID: m.ID, TaskID: task.ID, TaskRevision: p.TaskRevision,
		DispatchKey: p.DispatchKey, Harness: choice.Harness, Mode: choice.Mode,
		ActorRunID: run, AuthorizingHumanID: m.AccountableHumanID,
		RunOwnerID: m.AccountableHumanID, AccountOwnerID: choice.AccountMemberID,
		AuthorityGeneration: m.IntegratorGeneration, IntegratorGeneration: m.IntegratorGeneration,
	})
	if err != nil {
		return nil, err
	}
	if replayed {
		return protocol.WorkerStartResult{Attempt: attemptWire(attempt), Replayed: true}, nil
	}
	if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
		return nil, publishErr
	}
	if s.cfg.Cost != nil {
		if admitErr := s.cfg.Cost.Admit(ctx, m.WorkspaceID, m.AccountableHumanID); admitErr != nil {
			s.markAttemptUnknown(ctx, attempt, admitErr.Error())
			return nil, admitErr
		}
	}
	if s.cfg.Runs == nil {
		err = errors.New("mission: scheduler unavailable")
		s.markAttemptUnknown(ctx, attempt, err.Error())
		return nil, err
	}
	launched, err := s.cfg.Runs.LaunchMission(s.operationContext(ctx), MissionLaunchRequest{
		WorkspaceID: m.WorkspaceID, RunID: attempt.RunID, ActorRunID: run,
		RunOwnerID: attempt.RunOwnerID, AccountOwner: attempt.AccountOwnerID,
		Task: task.Revision.Objective, Harness: attempt.Harness, Mode: attempt.Mode,
	})
	if err != nil {
		s.markAttemptUnknown(ctx, attempt, err.Error())
		return nil, err
	}
	if bindErr := s.cfg.Missions.BindAttemptRun(ctx, attempt.ID, launched.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration); bindErr != nil {
		return nil, bindErr
	}
	attempt.RunID = launched.ID
	_ = s.publishMissionChanged(ctx, m.ID)
	return protocol.WorkerStartResult{Attempt: attemptWire(attempt)}, nil
}

func (s *Service) workerList(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.WorkerListParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	m, err := s.integratorMission(ctx, run, p.MissionID)
	if err != nil {
		return nil, err
	}
	attempts, err := s.cfg.Missions.ListAttempts(ctx, m.ID, domain.TaskID(p.TaskID))
	if err != nil {
		return nil, err
	}
	out := protocol.WorkerListResult{Attempts: make([]protocol.Attempt, 0, len(attempts))}
	for _, a := range attempts {
		out.Attempts = append(out.Attempts, attemptWire(a))
	}
	return out, nil
}

func (s *Service) workerInspect(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.WorkerInspectParams
	if err := json.Unmarshal(raw, &p); err != nil || p.AttemptID == "" {
		return nil, errors.New("mission: worker.inspect requires attempt_id")
	}
	m, assigned, err := s.resolveAssignment(ctx, run)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("%w: run has no mission assignment", store.ErrMissionStale)
	}
	a, err := s.cfg.Missions.GetAttempt(ctx, domain.AttemptID(p.AttemptID))
	if err != nil {
		return nil, err
	}
	if a.MissionID != m.ID {
		return nil, fmt.Errorf("%w: attempt is outside mission assignment", store.ErrMissionStale)
	}
	if assigned != nil && assigned.ID != a.ID {
		return nil, fmt.Errorf("%w: worker may inspect only its current attempt", store.ErrMissionStale)
	}
	var sub *domain.Submission
	submissions, err := s.cfg.Missions.ListSubmissions(ctx, a.MissionID, a.TaskID)
	if err != nil {
		return nil, err
	}
	for _, candidate := range submissions {
		if candidate != nil && candidate.AttemptID == a.ID {
			sub = candidate
			break
		}
	}
	out := protocol.WorkerInspectResult{Attempt: attemptWire(a)}
	if sub != nil {
		w := submissionWire(sub)
		out.Submission = &w
	}
	return out, nil
}

func (s *Service) workerCancel(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.WorkerCancelParams
	if err := json.Unmarshal(raw, &p); err != nil || p.AttemptID == "" || p.IdempotencyKey == "" {
		return nil, errors.New("mission: worker.cancel requires attempt_id and idempotency_key")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.integratorMission(ctx, run, "")
	if err != nil {
		return nil, err
	}
	a, err := s.cfg.Missions.GetAttempt(ctx, domain.AttemptID(p.AttemptID))
	if err != nil {
		return nil, err
	}
	if a.MissionID != m.ID || p.ExpectedIntegratorGeneration != m.IntegratorGeneration {
		return nil, fmt.Errorf("%w: stale worker authority", store.ErrMissionStale)
	}
	if !a.State.HoldsConcurrency() {
		return protocol.WorkerMutationResult{Attempt: attemptWire(a), Replayed: true}, nil
	}
	if authorizeErr := s.authorizeAttemptKill(ctx, m, a); authorizeErr != nil {
		return nil, authorizeErr
	}
	if s.cfg.MissionControl == nil {
		return nil, errors.New("mission: mission control is unavailable")
	}
	control, err := s.cfg.MissionControl()
	if err != nil {
		return nil, err
	}
	if control == nil {
		return nil, errors.New("mission: mission control is unavailable")
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	var requested *domain.Attempt
	var replayed bool
	admitErr := control.AdmitInput(s.operationContext(ctx), m.CurrentIntegratorRunID, a.RunID, m.IntegratorGeneration, func() error {
		var requestErr error
		requested, replayed, requestErr = s.cfg.Missions.RequestAttemptCancellation(s.operationContext(ctx), a.ID, run, m.IntegratorGeneration, p.IdempotencyKey)
		if requestErr != nil {
			return requestErr
		}
		if requested == nil {
			requested = a
		}
		if s.cfg.Cancel == nil {
			return errors.New("mission: scheduler cancel unavailable")
		}
		return s.cfg.Cancel.CancelMission(s.operationContext(ctx), a.RunID)
	})
	if requested != nil {
		if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
			return nil, publishErr
		}
	}
	if admitErr != nil {
		return nil, admitErr
	}
	return protocol.WorkerMutationResult{Attempt: attemptWire(requested), Replayed: replayed}, nil
}
func (s *Service) workerRetry(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.WorkerRetryParams
	if err := json.Unmarshal(raw, &p); err != nil || p.AttemptID == "" || p.DispatchKey == "" {
		return nil, errors.New("mission: worker.retry requires attempt_id and dispatch_key")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.integratorMission(ctx, run, "")
	if err != nil {
		return nil, err
	}
	a, err := s.cfg.Missions.GetAttempt(ctx, domain.AttemptID(p.AttemptID))
	if err != nil {
		return nil, err
	}
	if a.MissionID != m.ID || p.ExpectedIntegratorGeneration != m.IntegratorGeneration {
		return nil, fmt.Errorf("%w: stale worker authority", store.ErrMissionStale)
	}
	if authorizeErr := s.authorizeAttempt(ctx, m, a); authorizeErr != nil {
		return nil, authorizeErr
	}
	if a.State.HoldsConcurrency() {
		return nil, errors.New("mission: attempt is still active")
	}
	if a.TakeoverActive {
		return nil, store.ErrMissionTakeover
	}
	req := protocol.WorkerStartParams{
		MissionID: string(m.ID), TaskID: string(a.TaskID), TaskRevision: a.TaskRevision,
		DispatchKey: p.DispatchKey, Harness: a.Harness, Mode: string(a.Mode),
		AccountOwnerID: string(a.AccountOwnerID), RunOwnerID: string(a.RunOwnerID),
		ExpectedIntegratorGeneration: p.ExpectedIntegratorGeneration,
	}
	b, _ := json.Marshal(req)
	return s.workerStartInternal(ctx, run, b, true)
}

func attemptWire(a *domain.Attempt) protocol.Attempt {
	out := protocol.Attempt{
		ID: string(a.ID), MissionID: string(a.MissionID), TaskID: string(a.TaskID),
		TaskRevision: a.TaskRevision, Number: a.Number, DispatchKey: a.DispatchKey,
		Harness: a.Harness, Mode: string(a.Mode), State: string(a.State), RunID: string(a.RunID),
		ActorRunID: string(a.ActorRunID), AuthorizingHumanID: string(a.AuthorizingHumanID),
		RunOwnerID: string(a.RunOwnerID), AccountOwnerID: string(a.AccountOwnerID),
		AuthorityGeneration: a.AuthorityGeneration, IntegratorGeneration: a.IntegratorGeneration,
		TakeoverActive: a.TakeoverActive, TakeoverMemberID: string(a.TakeoverMemberID),
		TakeoverGeneration: a.TakeoverGeneration, CancellationActorRunID: string(a.CancellationActorRunID),
		CancellationGeneration: a.CancellationGeneration, LastError: a.LastError,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339), ReservedAt: a.ReservedAt.UTC().Format(time.RFC3339),
	}
	if a.StartedAt != nil {
		v := a.StartedAt.UTC().Format(time.RFC3339)
		out.StartedAt = &v
	}
	if a.FinishedAt != nil {
		v := a.FinishedAt.UTC().Format(time.RFC3339)
		out.FinishedAt = &v
	}
	if a.CancelRequestedAt != nil {
		v := a.CancelRequestedAt.UTC().Format(time.RFC3339)
		out.CancelRequestedAt = &v
	}
	return out
}
