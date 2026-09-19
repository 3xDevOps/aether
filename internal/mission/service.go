package mission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// Launcher is the scheduler seam mission dispatch uses. LaunchMission must
// honor the supplied run ID; a lost response must never create a second run.
type Launcher interface {
	LaunchMission(context.Context, MissionLaunchRequest) (*domain.Run, error)
}

type Canceller interface {
	CancelMission(context.Context, domain.RunID) error
}

type MissionLaunchRequest struct {
	WorkspaceID          domain.WorkspaceID
	MissionID            domain.MissionID
	AttemptID            domain.AttemptID
	IntegratorGeneration uint64
	RunID                domain.RunID
	ActorRunID           domain.RunID
	RunOwnerID           domain.MemberID
	AccountOwner         domain.MemberID
	Task                 string
	Harness              string
	Mode                 domain.LaunchMode
}

type Budget interface {
	Admit(context.Context, domain.WorkspaceID, domain.MemberID) error
}

// MissionRunObservation is the scheduler's authoritative lifecycle view. A
// terminal run row alone is never enough to settle a mission attempt.
type MissionRunState string

const (
	MissionRunActive         MissionRunState = "active"
	MissionRunPending        MissionRunState = "pending"
	MissionRunDestroyPending MissionRunState = "destroy_pending"
	MissionRunRetained       MissionRunState = "retained"
	MissionRunUnknown        MissionRunState = "unknown"
	MissionRunStopped        MissionRunState = "stopped"
)

type MissionRunObservation struct {
	State            MissionRunState
	RetentionSettled bool
}

type MissionRunObserver func(context.Context, domain.RunID) (MissionRunObservation, error)
type MissionControlResolver func() (sshd.MissionControl, error)

func (o MissionRunObservation) Settled() bool {
	return o.State == MissionRunStopped && o.RetentionSettled
}

func (o MissionRunObservation) HoldsExecution() bool {
	switch o.State {
	case MissionRunActive, MissionRunPending, MissionRunDestroyPending, MissionRunRetained, MissionRunUnknown:
		return true
	default:
		return false
	}
}

type Config struct {
	Store               store.Store
	Missions            store.MissionStore
	Runs                Launcher
	Cancel              Canceller
	AuthorizationMu     *sync.Mutex
	Cost                Budget
	Evidence            EvidenceReader
	ScopeSnapshot       func(context.Context, domain.RunID) ([]string, bool, error)
	ObserveMissionRun   MissionRunObserver
	MissionControl      MissionControlResolver
	RequireCoordination func() error
	Bus                 events.Bus
	Now                 func() time.Time
	// Integration resolves the underlying candidate engine lazily. Mission
	// policy owns the adapter while the engine remains the implementation.
	Integration func() (sshd.IntegrationService, error)
}
type Service struct {
	cfg             Config
	mu              sync.Mutex
	integrationMu   sync.RWMutex
	integrationBase sshd.IntegrationService
	dispatchMu      sync.Mutex
	reconcileCursor map[domain.WorkspaceID]string
	cancel          context.CancelFunc
	runCtx          context.Context
	done            chan struct{}
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("mission: store is required")
	}
	if cfg.Missions == nil {
		var ok bool
		cfg.Missions, ok = cfg.Store.(store.MissionStore)
		if !ok {
			return nil, errors.New("mission: store does not implement MissionStore")
		}
	}
	if cfg.AuthorizationMu == nil {
		cfg.AuthorizationMu = &sync.Mutex{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ScopeSnapshot == nil {
		cfg.ScopeSnapshot = func(context.Context, domain.RunID) ([]string, bool, error) {
			return nil, false, errors.New("mission: scope snapshot unavailable")
		}
	}
	return &Service{cfg: cfg, reconcileCursor: make(map[domain.WorkspaceID]string)}, nil
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel, s.runCtx, s.done = cancel, runCtx, done
	s.mu.Unlock()
	if err := s.reconcile(runCtx); err != nil {
		cancel()
		s.mu.Lock()
		if s.done == done {
			s.cancel, s.runCtx, s.done = nil, nil, nil
		}
		close(done)
		s.mu.Unlock()
		return fmt.Errorf("mission: recover durable state: %w", err)
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := s.reconcile(runCtx); err != nil {
					slog.Warn("mission: reconciliation pass failed", "error", err)
				}
			}
		}
	}()
	return nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.runCtx, s.done = nil, nil, nil
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	if done != nil {
		<-done
	}
	return nil
}

func (s *Service) operationContext(request context.Context) context.Context {
	s.mu.Lock()
	runCtx := s.runCtx
	s.mu.Unlock()
	if runCtx != nil {
		return runCtx
	}
	return context.WithoutCancel(request)
}

// reconcile repairs durable reservations that lost their in-memory scheduler
// owner during a process restart. Every launch keeps its persisted RunID, so
// retries cannot create a second process for one mission attempt.
func (s *Service) reconcile(ctx context.Context) error {
	if s.cfg.Runs == nil {
		return nil
	}
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		cursor := s.reconcileCursor[workspace.ID]
		missions, next, err := s.cfg.Missions.ListMissionsPage(ctx, workspace.ID, 64, cursor)
		if err != nil {
			return err
		}
		for _, m := range missions {
			if missionErr := s.reconcileMission(ctx, m); missionErr != nil {
				slog.Warn("mission: reconcile mission", "mission", m.ID, "error", missionErr)
			}
			if pendingErr := s.reconcilePendingMissionChange(ctx, m.ID); pendingErr != nil {
				slog.Warn("mission: reconcile mission notification", "mission", m.ID, "error", pendingErr)
			}
		}
		if next == "" {
			delete(s.reconcileCursor, workspace.ID)
		} else {
			s.reconcileCursor[workspace.ID] = next
		}
	}
	return nil
}

func (s *Service) observeMissionRun(ctx context.Context, run domain.RunID) (MissionRunObservation, error) {
	if s.cfg.ObserveMissionRun == nil {
		return MissionRunObservation{State: MissionRunUnknown}, nil
	}
	return s.cfg.ObserveMissionRun(ctx, run)
}

func (s *Service) reconcilePendingMissionChange(ctx context.Context, missionID domain.MissionID) error {
	version, err := s.cfg.Missions.PendingMissionControlChange(ctx, missionID)
	if err != nil {
		return fmt.Errorf("mission: pending mission notification %s: %w", missionID, err)
	}
	if version == 0 {
		return nil
	}
	if s.cfg.Bus == nil {
		return errors.New("mission: mission notification bus is unavailable")
	}
	if publishErr := s.publishMissionChanged(ctx, missionID); publishErr != nil {
		return fmt.Errorf("mission: publish pending notification %s: %w", missionID, publishErr)
	}
	if ackErr := s.cfg.Missions.AckMissionControlChange(ctx, missionID, version); ackErr != nil {
		return fmt.Errorf("mission: acknowledge pending notification %s at %d: %w", missionID, version, ackErr)
	}
	return nil
}

func (s *Service) requireCoordination() error {
	if s.cfg.RequireCoordination == nil {
		return errors.New("mission: coordination is unavailable")
	}
	return s.cfg.RequireCoordination()
}
func (s *Service) authorizeAttempt(ctx context.Context, mission *domain.Mission, attempt *domain.Attempt) error {
	if mission == nil || attempt == nil || mission.AccountableHumanID == "" || attempt.AccountOwnerID == "" {
		return errors.New("mission: attempt authorization is incomplete")
	}
	admission, err := sshd.AuthorizeLaunch(ctx, s.cfg.Store, mission.AccountableHumanID, string(attempt.AccountOwnerID))
	if err != nil {
		return err
	}
	if admission.Account.ID != attempt.AccountOwnerID {
		return errors.New("mission: attempt account attribution changed")
	}
	return nil
}
func (s *Service) authorizeAttemptKill(ctx context.Context, mission *domain.Mission, attempt *domain.Attempt) error {
	if err := s.authorizeAttempt(ctx, mission, attempt); err != nil {
		return err
	}
	run, err := s.cfg.Store.GetRun(ctx, attempt.RunID)
	if err != nil {
		return err
	}
	ws, err := s.cfg.Store.GetWorkspace(ctx, run.WorkspaceID)
	if err != nil {
		return err
	}
	member, err := s.cfg.Store.GetMember(ctx, mission.AccountableHumanID)
	if err != nil {
		return err
	}
	return permissions.Check(permissions.Kill,
		permissions.Actor{ID: member.ID, Role: member.Role},
		permissions.Target{Workspace: run.WorkspaceID, Owner: run.MemberID, Protected: run.Protected, SteerOthers: ws.SteerOthers})
}

func (s *Service) launchRecovered(ctx context.Context, req MissionLaunchRequest) error {
	if err := s.requireCoordination(); err != nil {
		return err
	}
	if req.RunID == "" || req.RunOwnerID == "" || req.AccountOwner == "" {
		return errors.New("mission: recovered launch has incomplete durable attribution")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	admission, err := sshd.AuthorizeLaunch(ctx, s.cfg.Store, req.RunOwnerID, string(req.AccountOwner))
	if err != nil {
		return err
	}
	if admission.Account.ID != req.AccountOwner {
		return errors.New("mission: recovered account attribution changed")
	}
	if req.MissionID != "" {
		current, missionErr := s.cfg.Missions.GetMission(ctx, req.MissionID)
		if missionErr != nil {
			return missionErr
		}
		if req.AttemptID == "" {
			if current.CurrentIntegratorRunID != req.RunID || current.IntegratorGeneration != req.IntegratorGeneration ||
				current.Integrator.AccountMemberID != req.AccountOwner || current.Integrator.Harness != req.Harness ||
				current.Integrator.Mode != req.Mode || current.IntegratorRunOwnerID != req.RunOwnerID {
				return fmt.Errorf("%w: recovered integrator assignment changed", store.ErrMissionStale)
			}
		} else {
			attempt, attemptErr := s.cfg.Missions.GetAttempt(ctx, req.AttemptID)
			if attemptErr != nil {
				return attemptErr
			}
			if attempt.MissionID != req.MissionID || attempt.RunID != req.RunID ||
				attempt.IntegratorGeneration != req.IntegratorGeneration ||
				!attempt.State.HoldsConcurrency() || attempt.CancelRequestedAt != nil ||
				attempt.RunOwnerID != req.RunOwnerID ||
				attempt.AccountOwnerID != req.AccountOwner || attempt.Harness != req.Harness ||
				attempt.Mode != req.Mode {
				return fmt.Errorf("%w: recovered worker assignment changed", store.ErrMissionStale)
			}
		}
	}
	if s.cfg.Cost != nil {
		if admitErr := s.cfg.Cost.Admit(ctx, req.WorkspaceID, req.RunOwnerID); admitErr != nil {
			return admitErr
		}
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	_, err = s.cfg.Runs.LaunchMission(s.operationContext(ctx), req)
	return err
}

func (s *Service) settleObservedAttempt(ctx context.Context, attempt *domain.Attempt, run *domain.Run, obs MissionRunObservation) error {
	if attempt == nil || run == nil || !run.Status.Terminal() || !obs.Settled() || !attempt.State.HoldsConcurrency() {
		return nil
	}
	target := domain.AttemptFailed
	if run.Reason == "killed" || attempt.CancelRequestedAt != nil {
		target = domain.AttemptCancelled
	}
	if err := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, target, run.Reason); err != nil {
		return err
	}
	return s.publishMissionChanged(ctx, attempt.MissionID)
}

func (s *Service) markAttemptUnknown(ctx context.Context, attempt *domain.Attempt, detail string) {
	if attempt == nil {
		return
	}
	if err := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptUnknown, detail); err != nil && !errors.Is(err, store.ErrMissionStale) {
		slog.Warn("mission: record attempt policy failure", "attempt", attempt.ID, "error", err)
		return
	}
	_ = s.publishMissionChanged(ctx, attempt.MissionID)
}

func (s *Service) reconcileMission(ctx context.Context, mission *domain.Mission) error {
	if mission == nil {
		return nil
	}
	if mission.CurrentIntegratorRunID != "" {
		_, runErr := s.cfg.Store.GetRun(ctx, mission.CurrentIntegratorRunID)
		if errors.Is(runErr, store.ErrNotFound) {
			choice := mission.Integrator
			launchErr := s.launchRecovered(ctx, MissionLaunchRequest{
				WorkspaceID: mission.WorkspaceID, MissionID: mission.ID,
				IntegratorGeneration: mission.IntegratorGeneration,
				RunID:                mission.CurrentIntegratorRunID, ActorRunID: mission.CurrentIntegratorRunID,
				RunOwnerID: mission.IntegratorRunOwnerID, AccountOwner: choice.AccountMemberID,
				Task: mission.Objective, Harness: choice.Harness, Mode: choice.Mode,
			})
			if launchErr != nil {
				slog.Warn("mission: recover integrator", "mission", mission.ID, "error", launchErr)
			}
		} else if runErr != nil {
			return runErr
		}
	}
	attempts, err := s.cfg.Missions.ListAttempts(ctx, mission.ID, "")
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		if attempt == nil {
			continue
		}
		if attempt.State == domain.AttemptSubmitted {
			if attempt.CancelRequestedAt != nil {
				if cancellationErr := s.reconcileCancellation(ctx, mission, attempt); cancellationErr != nil {
					slog.Warn("mission: reconcile cancellation", "mission", mission.ID, "attempt", attempt.ID, "error", cancellationErr)
				}
			} else if submittedErr := s.reconcileSubmitted(ctx, mission, attempt); submittedErr != nil {
				slog.Warn("mission: reconcile submitted attempt", "mission", mission.ID, "attempt", attempt.ID, "error", submittedErr)
			}
			continue
		}
		if !attempt.State.HoldsConcurrency() || attempt.RunID == "" {
			continue
		}
		current, runErr := s.cfg.Store.GetRun(ctx, attempt.RunID)
		if runErr == nil {
			obs, observeErr := s.observeMissionRun(ctx, attempt.RunID)
			if observeErr != nil {
				slog.Warn("mission: observe attempt", "attempt", attempt.ID, "error", observeErr)
				continue
			}
			if settleErr := s.settleObservedAttempt(ctx, attempt, current, obs); settleErr != nil {
				slog.Warn("mission: settle attempt", "attempt", attempt.ID, "error", settleErr)
			}
			if !obs.Settled() && attempt.CancelRequestedAt != nil {
				if cancellationErr := s.reconcileCancellation(ctx, mission, attempt); cancellationErr != nil {
					slog.Warn("mission: reconcile cancellation", "mission", mission.ID, "attempt", attempt.ID, "error", cancellationErr)
				}
			}
			continue
		}
		if !errors.Is(runErr, store.ErrNotFound) {
			return runErr
		}
		if attempt.CancelRequestedAt != nil {
			s.dispatchMu.Lock()
			obs, observeErr := s.observeMissionRun(ctx, attempt.RunID)
			if errors.Is(observeErr, store.ErrNotFound) || (observeErr == nil && obs.Settled()) {
				stateErr := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCancelled, "cancelled before launch")
				s.dispatchMu.Unlock()
				if stateErr != nil && !errors.Is(stateErr, store.ErrMissionStale) {
					return stateErr
				}
				_ = s.publishMissionChanged(ctx, mission.ID)
				continue
			}
			s.dispatchMu.Unlock()
			if observeErr != nil {
				slog.Warn("mission: observe cancelled reservation", "attempt", attempt.ID, "error", observeErr)
			}
			continue
		}
		task, taskErr := s.cfg.Missions.GetTask(ctx, attempt.TaskID)
		if taskErr != nil || task == nil || task.Revision == nil {
			if taskErr != nil && !errors.Is(taskErr, store.ErrNotFound) {
				slog.Warn("mission: recover attempt task", "attempt", attempt.ID, "error", taskErr)
			}
			continue
		}
		if task.Revision.Revision != attempt.TaskRevision {
			s.markAttemptUnknown(ctx, attempt, fmt.Sprintf("task revision changed from %d to %d", attempt.TaskRevision, task.Revision.Revision))
			continue
		}
		launchErr := s.launchRecovered(ctx, MissionLaunchRequest{
			WorkspaceID: mission.WorkspaceID, MissionID: mission.ID, AttemptID: attempt.ID,
			IntegratorGeneration: attempt.IntegratorGeneration,
			RunID:                attempt.RunID, ActorRunID: attempt.ActorRunID,
			RunOwnerID: attempt.RunOwnerID, AccountOwner: attempt.AccountOwnerID,
			Task: task.Revision.Objective, Harness: attempt.Harness, Mode: attempt.Mode,
		})
		if launchErr != nil {
			slog.Warn("mission: recover worker", "mission", mission.ID, "attempt", attempt.ID, "error", launchErr)
			if stateErr := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptUnknown, launchErr.Error()); stateErr != nil && !errors.Is(stateErr, store.ErrMissionStale) {
				slog.Warn("mission: record worker recovery failure", "attempt", attempt.ID, "error", stateErr)
			} else {
				_ = s.publishMissionChanged(ctx, mission.ID)
			}
			continue
		}
		_ = s.publishMissionChanged(ctx, mission.ID)
	}
	return nil
}

func (s *Service) reconcileCancellation(ctx context.Context, mission *domain.Mission, attempt *domain.Attempt) error {
	if attempt == nil || attempt.RunID == "" || attempt.CancelRequestedAt == nil {
		return nil
	}
	obs, observeErr := s.observeMissionRun(ctx, attempt.RunID)
	if observeErr == nil {
		if currentRun, runErr := s.cfg.Store.GetRun(ctx, attempt.RunID); runErr == nil && obs.Settled() && currentRun.Status.Terminal() {
			if updateErr := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCancelled, currentRun.Reason); updateErr != nil {
				return updateErr
			}
			return s.publishMissionChanged(ctx, mission.ID)
		}
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	current, err := s.cfg.Missions.GetMission(ctx, mission.ID)
	if err != nil {
		return err
	}
	if authorizeErr := s.authorizeAttemptKill(ctx, current, attempt); authorizeErr != nil {
		return authorizeErr
	}
	control, err := s.cfg.MissionControl()
	if err != nil {
		return err
	}
	if control == nil {
		return errors.New("mission: mission control is unavailable")
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	return control.AdmitInput(s.operationContext(ctx), current.CurrentIntegratorRunID, attempt.RunID, current.IntegratorGeneration, func() error {
		if s.cfg.Cancel == nil {
			return errors.New("mission: scheduler cancel unavailable")
		}
		return s.cfg.Cancel.CancelMission(s.operationContext(ctx), attempt.RunID)
	})
}

func (s *Service) reconcileSubmitted(ctx context.Context, mission *domain.Mission, attempt *domain.Attempt) error {
	if s.cfg.Evidence == nil || attempt.RunID == "" {
		return nil
	}
	submissions, err := s.cfg.Missions.ListSubmissions(ctx, mission.ID, attempt.TaskID)
	if err != nil {
		return err
	}
	retained := false
	for _, submission := range submissions {
		if submission == nil || submission.AttemptID != attempt.ID || submission.Ref.EvidenceRef == "" {
			continue
		}
		packet, getErr := s.cfg.Evidence.Get(ctx, mission.WorkspaceID, submission.Ref.EvidenceRef)
		if getErr == nil && packet.ID != "" && packet.Availability == protocol.EvidenceAvailable &&
			packet.RetainedRevision != "" && packet.RunID == string(attempt.RunID) {
			retained = true
			break
		}
	}
	if !retained {
		return nil
	}
	obs, err := s.observeMissionRun(ctx, attempt.RunID)
	if err != nil {
		return err
	}
	if obs.Settled() {
		if updateErr := s.cfg.Missions.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration, attempt.IntegratorGeneration, domain.AttemptCompleted, "retained"); updateErr != nil {
			return updateErr
		}
		return s.publishMissionChanged(ctx, mission.ID)
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	current, err := s.cfg.Missions.GetMission(ctx, mission.ID)
	if err != nil {
		return err
	}
	if authorizeErr := s.authorizeAttemptKill(ctx, current, attempt); authorizeErr != nil {
		return authorizeErr
	}
	control, err := s.cfg.MissionControl()
	if err != nil {
		return err
	}
	if control == nil {
		return errors.New("mission: mission control is unavailable")
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	return control.AdmitInput(s.operationContext(ctx), current.CurrentIntegratorRunID, attempt.RunID, current.IntegratorGeneration, func() error {
		if s.cfg.Cancel == nil {
			return errors.New("mission: scheduler cancel unavailable")
		}
		return s.cfg.Cancel.CancelMission(s.operationContext(ctx), attempt.RunID)
	})
}

// Create persists the bounded human authorization and launches its
// integrator through the reserved-run scheduler seam. The actor is always the
// authenticated member; request fields cannot impersonate a different owner.
func (s *Service) Create(ctx context.Context, actor domain.MemberID, p protocol.MissionCreateParams) (protocol.MissionCreateResult, error) {
	if p.WorkspaceID == "" || strings.TrimSpace(p.Objective) == "" || p.IdempotencyKey == "" {
		return protocol.MissionCreateResult{}, errors.New("workspace_id, objective, and idempotency_key are required")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	admission, err := sshd.AuthorizeLaunch(ctx, s.cfg.Store, actor, p.Integrator.AccountMemberID)
	if err != nil {
		return protocol.MissionCreateResult{}, err
	}
	if p.AccountableHumanID == "" {
		p.AccountableHumanID = string(actor)
	} else if p.AccountableHumanID != string(actor) {
		return protocol.MissionCreateResult{}, fmt.Errorf("%w: accountable_human_id must match authenticated member", permissions.ErrDenied)
	}
	accountable, err := s.cfg.Store.GetMember(ctx, domain.MemberID(p.AccountableHumanID))
	if err != nil {
		return protocol.MissionCreateResult{}, err
	}
	if s.cfg.Cost != nil {
		if admitErr := s.cfg.Cost.Admit(ctx, domain.WorkspaceID(p.WorkspaceID), actor); admitErr != nil {
			return protocol.MissionCreateResult{}, admitErr
		}
	}
	if accountable.Pending {
		return protocol.MissionCreateResult{}, fmt.Errorf("%w: accountable human is pending admin approval", permissions.ErrDenied)
	}
	choice, err := executionChoice(p.Integrator, p.ExecutionChoices)
	if err != nil {
		return protocol.MissionCreateResult{}, err
	}
	choices := make([]domain.MissionExecutionChoice, 0, len(p.ExecutionChoices))
	for _, c := range p.ExecutionChoices {
		mode := domain.LaunchMode(c.Mode)
		if c.AccountMemberID == "" || c.Harness == "" || !mode.Valid() {
			return protocol.MissionCreateResult{}, errors.New("execution choices must name account_member_id, harness, and valid mode")
		}
		choices = append(choices, domain.MissionExecutionChoice{AccountMemberID: domain.MemberID(c.AccountMemberID), Harness: c.Harness, Mode: mode})
	}
	m := &domain.Mission{
		WorkspaceID: domain.WorkspaceID(p.WorkspaceID), Objective: p.Objective,
		AccountableHumanID: domain.MemberID(p.AccountableHumanID),
		Integrator:         choice, ExecutionChoices: choices,
		MaxConcurrentAttempts: p.MaxConcurrentAttempts, MaxTotalAttempts: p.MaxTotalAttempts,
		IdempotencyKey:               p.IdempotencyKey,
		IntegratorAuthorizingHumanID: actor, IntegratorRunOwnerID: actor,
	}
	if coordinationErr := s.requireCoordination(); coordinationErr != nil {
		return protocol.MissionCreateResult{}, coordinationErr
	}
	if createErr := s.cfg.Missions.CreateMission(ctx, m); createErr != nil {
		return protocol.MissionCreateResult{}, createErr
	}
	if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
		return protocol.MissionCreateResult{Mission: protocol.MissionFromDomain(m)}, publishErr
	}
	if s.cfg.Runs == nil || m.CurrentIntegratorRunID == "" {
		return protocol.MissionCreateResult{Mission: protocol.MissionFromDomain(m)}, errors.New("mission: integrator run reservation is unavailable")
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if existing, getErr := s.cfg.Store.GetRun(ctx, m.CurrentIntegratorRunID); getErr == nil && existing != nil {
		return protocol.MissionCreateResult{Mission: protocol.MissionFromDomain(m)}, nil
	}
	_, err = s.cfg.Runs.LaunchMission(s.operationContext(ctx), MissionLaunchRequest{
		WorkspaceID: m.WorkspaceID, RunID: m.CurrentIntegratorRunID,
		ActorRunID: m.CurrentIntegratorRunID, RunOwnerID: m.IntegratorRunOwnerID,
		AccountOwner: admission.Account.ID, Task: m.Objective, Harness: choice.Harness, Mode: choice.Mode,
	})
	if err != nil {
		return protocol.MissionCreateResult{Mission: protocol.MissionFromDomain(m)}, err
	}
	return protocol.MissionCreateResult{Mission: protocol.MissionFromDomain(m)}, nil
}

func (s *Service) ReplaceIntegrator(ctx context.Context, actor domain.MemberID, p protocol.MissionReplaceIntegratorParams) (protocol.MissionReplaceIntegratorResult, error) {
	if p.MissionID == "" || p.IdempotencyKey == "" {
		return protocol.MissionReplaceIntegratorResult{}, errors.New("mission_id and idempotency_key are required")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.cfg.Missions.GetMission(ctx, domain.MissionID(p.MissionID))
	if err != nil {
		return protocol.MissionReplaceIntegratorResult{}, err
	}
	requesting, err := s.cfg.Store.GetMember(ctx, actor)
	if err != nil {
		return protocol.MissionReplaceIntegratorResult{}, err
	}
	if actor != m.AccountableHumanID && requesting.Role != domain.RoleAdmin {
		return protocol.MissionReplaceIntegratorResult{}, fmt.Errorf("%w: only the accountable human or an admin may replace the integrator", permissions.ErrDenied)
	}
	choice, err := executionChoice(p.Integrator, choicesFromMission(m))
	if err != nil {
		return protocol.MissionReplaceIntegratorResult{}, err
	}
	admission, err := sshd.AuthorizeLaunch(ctx, s.cfg.Store, actor, p.Integrator.AccountMemberID)
	if err != nil {
		return protocol.MissionReplaceIntegratorResult{}, err
	}
	if s.cfg.Cost != nil {
		if admitErr := s.cfg.Cost.Admit(ctx, m.WorkspaceID, actor); admitErr != nil {
			return protocol.MissionReplaceIntegratorResult{}, admitErr
		}
	}
	if coordinationErr := s.requireCoordination(); coordinationErr != nil {
		return protocol.MissionReplaceIntegratorResult{}, coordinationErr
	}
	replaced, err := s.cfg.Missions.ReplaceIntegrator(ctx, m.ID, p.ExpectedGeneration, choice, actor, actor, p.IdempotencyKey)
	if err != nil {
		return protocol.MissionReplaceIntegratorResult{}, err
	}
	if publishErr := s.publishMissionChanged(ctx, replaced.ID); publishErr != nil {
		return protocol.MissionReplaceIntegratorResult{Mission: protocol.MissionFromDomain(replaced)}, publishErr
	}
	if s.cfg.Runs == nil || replaced.CurrentIntegratorRunID == "" {
		return protocol.MissionReplaceIntegratorResult{Mission: protocol.MissionFromDomain(replaced)}, errors.New("mission: integrator run reservation is unavailable")
	}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	launched, err := s.cfg.Runs.LaunchMission(s.operationContext(ctx), MissionLaunchRequest{
		WorkspaceID: replaced.WorkspaceID, RunID: replaced.CurrentIntegratorRunID,
		ActorRunID: replaced.CurrentIntegratorRunID, RunOwnerID: replaced.IntegratorRunOwnerID,
		AccountOwner: admission.Account.ID, Task: replaced.Objective, Harness: choice.Harness, Mode: choice.Mode,
	})
	if err != nil {
		return protocol.MissionReplaceIntegratorResult{Mission: protocol.MissionFromDomain(replaced)}, err
	}
	return protocol.MissionReplaceIntegratorResult{Mission: protocol.MissionFromDomain(replaced), RunID: string(launched.ID)}, nil
}

func choicesFromMission(m *domain.Mission) []protocol.MissionExecutionChoice {
	out := make([]protocol.MissionExecutionChoice, 0, len(m.ExecutionChoices))
	for _, c := range m.ExecutionChoices {
		out = append(out, protocol.MissionExecutionChoice{AccountMemberID: string(c.AccountMemberID), Harness: c.Harness, Mode: string(c.Mode)})
	}
	return out
}

func executionChoice(in protocol.MissionIntegrator, choices []protocol.MissionExecutionChoice) (domain.MissionIntegrator, error) {
	mode := domain.LaunchMode(in.Mode)
	if in.AccountMemberID == "" || in.Harness == "" || !mode.Valid() {
		return domain.MissionIntegrator{}, errors.New("integrator account_member_id, harness, and valid mode are required")
	}
	for _, c := range choices {
		if c.AccountMemberID == in.AccountMemberID && c.Harness == in.Harness && c.Mode == in.Mode {
			return domain.MissionIntegrator{AccountMemberID: domain.MemberID(in.AccountMemberID), Harness: in.Harness, Mode: mode}, nil
		}
	}
	return domain.MissionIntegrator{}, errors.New("integrator choice must be one of execution_choices")
}

func (s *Service) List(ctx context.Context, p protocol.MissionListParams) (protocol.MissionListResult, error) {
	if p.WorkspaceID == "" {
		return protocol.MissionListResult{}, errors.New("workspace_id is required")
	}
	limit := p.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, next, err := s.cfg.Missions.ListMissionsPage(ctx, domain.WorkspaceID(p.WorkspaceID), limit, p.Before)
	if err != nil {
		return protocol.MissionListResult{}, err
	}
	out := protocol.MissionListResult{Missions: make([]protocol.Mission, 0, len(rows)), NextCursor: next}
	for _, m := range rows {
		out.Missions = append(out.Missions, protocol.MissionFromDomain(m))
	}
	return out, nil
}

func (s *Service) Show(ctx context.Context, p protocol.MissionShowParams) (protocol.MissionShowResult, error) {
	m, err := s.cfg.Missions.GetMission(ctx, domain.MissionID(p.MissionID))
	if err != nil {
		return protocol.MissionShowResult{}, err
	}
	tasks, err := s.cfg.Missions.ListTasks(ctx, m.ID)
	if err != nil {
		return protocol.MissionShowResult{}, err
	}
	attempts, err := s.cfg.Missions.ListAttempts(ctx, m.ID, "")
	if err != nil {
		return protocol.MissionShowResult{}, err
	}
	submissions, err := s.cfg.Missions.ListSubmissions(ctx, m.ID, "")
	if err != nil {
		return protocol.MissionShowResult{}, err
	}
	out := protocol.MissionShowResult{Mission: protocol.MissionFromDomain(m), Tasks: make([]protocol.Task, 0, len(tasks)), Attempts: make([]protocol.Attempt, 0, len(attempts)), Submissions: make([]protocol.Submission, 0, len(submissions))}
	for _, task := range tasks {
		out.Tasks = append(out.Tasks, taskWire(task))
	}
	for _, attempt := range attempts {
		out.Attempts = append(out.Attempts, attemptWire(attempt))
	}
	for _, submission := range submissions {
		out.Submissions = append(out.Submissions, submissionWire(submission))
	}
	diagnostics, diagErr := s.scopeDiagnostics(ctx, tasks, attempts, submissions)
	if diagErr != nil {
		return protocol.MissionShowResult{}, diagErr
	}
	out.Diagnostics = diagnostics
	return out, nil
}

// HandleAgent is a closed mission whitelist. The socket caller is already a
// run identity; no method accepts a human/member selector or generic RPC.
func (s *Service) HandleAgent(ctx context.Context, run domain.RunID, method string, raw json.RawMessage) (any, error) {
	switch method {
	case protocol.MethodIntegrationPrepare, protocol.MethodIntegrationShow,
		protocol.MethodIntegrationVerify, protocol.MethodIntegrationRequestDelivery,
		protocol.MethodIntegrationDeliver:
		return s.handleIntegrationAgent(ctx, run, method, raw)
	case protocol.MethodTaskShow, protocol.MethodTaskList:
		return s.handleTaskRead(ctx, run, method, raw)
	case protocol.MethodTaskPropose, protocol.MethodTaskRevise, protocol.MethodTaskAccept, protocol.MethodTaskAcceptSubmission, protocol.MethodTaskAbandon:
		return s.handleTaskMutation(ctx, run, method, raw)
	case protocol.MethodWorkerStart:
		return s.workerStart(ctx, run, raw)
	case protocol.MethodWorkerList:
		return s.workerList(ctx, run, raw)
	case protocol.MethodWorkerInspect:
		return s.workerInspect(ctx, run, raw)
	case protocol.MethodWorkerCancel:
		return s.workerCancel(ctx, run, raw)
	case protocol.MethodWorkerRetry:
		return s.workerRetry(ctx, run, raw)
	default:
		return nil, errors.New("mission: method not found")
	}
}

func (s *Service) integratorMission(ctx context.Context, run domain.RunID, requested string) (*domain.Mission, error) {
	m, err := s.cfg.Missions.GetMissionByRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if m.CurrentIntegratorRunID != run {
		return nil, fmt.Errorf("%w: stale integrator assignment", store.ErrMissionStale)
	}
	if requested != "" && m.ID != domain.MissionID(requested) {
		return nil, fmt.Errorf("%w: mission is outside assignment", store.ErrMissionStale)
	}
	return m, nil
}
