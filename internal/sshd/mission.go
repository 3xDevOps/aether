package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// LaunchAdmission is the fresh identity and account decision used by both
// human run.launch and mission dispatch. Account sharing is deliberately
// resolved at admission time; a run owner never becomes an account owner.
type LaunchAdmission struct {
	Actor   *domain.Member
	Account *domain.Member
}

// MissionService serves authenticated human mission reads and creation. Agent
// coordination uses the narrower MissionCoordService seam owned by coord.
type MissionService interface {
	Create(context.Context, domain.MemberID, protocol.MissionCreateParams) (protocol.MissionCreateResult, error)
	List(context.Context, protocol.MissionListParams) (protocol.MissionListResult, error)
	Show(context.Context, protocol.MissionShowParams) (protocol.MissionShowResult, error)
	ReplaceIntegrator(context.Context, domain.MemberID, protocol.MissionReplaceIntegratorParams) (protocol.MissionReplaceIntegratorResult, error)
	AnswerQuestion(context.Context, domain.MemberID, protocol.MissionQuestionAnswerParams) (protocol.MissionQuestionResult, error)
	DecidePlan(context.Context, domain.MemberID, protocol.MissionPlanDecideParams) (protocol.MissionPlanDecideResult, error)
}

// AuthorizeLaunch resolves and checks every mutable fact required before a
// run can be admitted. Callers that launch a run must hold the shared
// authorization mutex across this check and the scheduler's durable launch
// boundary.
func AuthorizeLaunch(ctx context.Context, st store.Store, actorID domain.MemberID, requestedAccount string) (LaunchAdmission, error) {
	if st == nil {
		return LaunchAdmission{}, fmt.Errorf("sshd: launch admission requires a store")
	}
	actor, err := st.GetMember(ctx, actorID)
	if err != nil {
		return LaunchAdmission{}, err
	}
	if actor.Pending {
		return LaunchAdmission{}, fmt.Errorf("%w: membership pending admin approval", permissions.ErrDenied)
	}
	if err = permissions.Check(permissions.Launch, permissions.Actor{ID: actor.ID, Role: actor.Role}, permissions.Target{}); err != nil {
		return LaunchAdmission{}, err
	}
	accountID, err := ResolveLaunchAccount(ctx, st, actor.ID, requestedAccount)
	if err != nil {
		return LaunchAdmission{}, err
	}
	account, err := st.GetMember(ctx, accountID)
	if err != nil {
		return LaunchAdmission{}, err
	}
	if account.Pending {
		return LaunchAdmission{}, fmt.Errorf("%w: account owner is pending admin approval", permissions.ErrDenied)
	}
	return LaunchAdmission{Actor: actor, Account: account}, nil
}

// ResolveLaunchAccount implements the existing directional whole-home share
// rule. It is shared by human and mission launches so administrators do not
// acquire implicit credential-use authority.
func ResolveLaunchAccount(ctx context.Context, st store.Store, actor domain.MemberID, requested string) (domain.MemberID, error) {
	account := domain.MemberID(requested)
	if account == "" || account == actor {
		return actor, nil
	}
	owner, err := st.GetMember(ctx, account)
	if err != nil {
		return "", err
	}
	if owner.Pending {
		return "", fmt.Errorf("%w: account owner is pending admin approval", permissions.ErrDenied)
	}
	shared, err := st.AccountSharedWith(ctx, account, actor)
	if err != nil {
		return "", err
	}
	if !shared {
		return "", fmt.Errorf("%w: member %s has not shared their account with you", permissions.ErrDenied, account)
	}
	return account, nil
}

// lockAuthorization runs the admission callback under the process-wide
// credential authorization mutex. The pointer is allocated by sshd.New and
// shared with the mission service builder.
func (s *Server) lockAuthorization(fn func() error) error {
	if fn == nil {
		return fmt.Errorf("sshd: nil launch admission callback")
	}
	if s.authorizationMu == nil {
		s.authorizationMu = &sync.Mutex{}
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	return fn()
}
func init() {
	registerMethod(protocol.MethodMissionCreate, (*Server).missionCreate)
	registerMethod(protocol.MethodMissionList, (*Server).missionList)
	registerMethod(protocol.MethodMissionShow, (*Server).missionShow)
	registerGuarded(protocol.MethodMissionWorkerRelease, permissions.Steer, runTarget, (*Server).missionWorkerRelease)
	registerMethod(protocol.MethodMissionReplaceIntegrator, (*Server).missionReplaceIntegrator)
	// Launch is the coarse role screen only. The accountable-human-or-admin
	// rule lives in the mission service, which holds the same authorization
	// mutex these handlers must not take.
	registerGuarded(protocol.MethodMissionQuestionAnswer, permissions.Launch, nil, (*Server).missionQuestionAnswer)
	registerGuarded(protocol.MethodMissionPlanDecide, permissions.Launch, nil, (*Server).missionPlanDecide)
}

func (s *Server) missions() (MissionService, *protocol.Error) {
	if s.cfg.Services.Missions == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "mission service is unavailable"}
	}
	return s.cfg.Services.Missions, nil
}

func (s *Server) missionWorkerRelease(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.MissionWorkerReleaseParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" {
		return nil, invalidParams("run_id is required")
	}
	if p.ExpectedTakeoverGeneration == 0 {
		return nil, invalidParams("expected_takeover_generation is required")
	}
	mission := s.cfg.Services.MissionControl
	if mission == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "mission control is unavailable"}
	}
	if s.cfg.Control == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "run control is unavailable"}
	}
	var assignment *domain.MissionWorkerAssignment
	err := s.lockAuthorization(func() error {
		// registerGuarded performs the first check before entering this
		// handler. Re-check under AuthorizationMu so role, ownership, and
		// workspace policy cannot be revoked between that check and the
		// worker control admission boundary.
		if err := checkSteer(ctx, s.cfg.Store, member, domain.RunID(p.RunID)); err != nil {
			return err
		}
		release := func() error {
			var err error
			assignment, err = mission.ReleaseHold(ctx, domain.RunID(p.RunID), member, p.ExpectedTakeoverGeneration)
			return err
		}
		// Status and ReleaseAdmitted are both fenced by the control service.
		// If a lease appears between an initially empty status and the
		// no-lease admission, retry so a foreign writer can never be evicted.
		for range 2 {
			lease, hadLease := s.cfg.Control.Status(p.RunID)
			if hadLease {
				if lease.MemberID != member {
					return control.ErrOccupied
				}
				if err := s.cfg.Control.ReleaseAdmitted(p.RunID, member, lease.SessionID, lease.Generation, release); err != nil {
					if errors.Is(err, control.ErrStale) {
						continue
					}
					return err
				}
				s.cancelControlAttach(p.RunID, lease.SessionID, lease.Generation, errAttachControlRevoked)
				return nil
			}
			retryLease := false
			err := s.cfg.Control.AdmitSnapshot(p.RunID, func(current control.Snapshot, present bool) error {
				if present {
					if current.MemberID != member {
						return control.ErrOccupied
					}
					retryLease = true
					return control.ErrStale
				}
				return release()
			})
			if retryLease && errors.Is(err, control.ErrStale) {
				continue
			}
			return err
		}
		return control.ErrStale
	})
	if err != nil {
		switch {
		case errors.Is(err, control.ErrOccupied), errors.Is(err, control.ErrStale), errors.Is(err, store.ErrMissionStale):
			return nil, &protocol.Error{Code: protocol.CodeConflict, Message: err.Error()}
		default:
			return nil, rpcError(err)
		}
	}
	if assignment == nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "mission control returned no assignment"}
	}
	return protocol.MissionWorkerReleaseResult{
		RunID:              string(assignment.WorkerRunID),
		TakeoverActive:     assignment.Active,
		TakeoverGeneration: assignment.Generation,
	}, nil
}

func (s *Server) missionCreate(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, err := s.missions()
	if err != nil {
		return nil, err
	}

	p, perr := decodeParams[protocol.MissionCreateParams](raw)
	if perr != nil {
		return nil, perr
	}
	out, callErr := svc.Create(ctx, member, p)
	if callErr != nil {
		return nil, rpcError(callErr)
	}
	return out, nil
}

func (s *Server) missionList(ctx context.Context, _ domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, err := s.missions()
	if err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.MissionListParams](raw)
	if perr != nil {
		return nil, perr
	}
	out, callErr := svc.List(ctx, p)
	if callErr != nil {
		return nil, rpcError(callErr)
	}
	return out, nil
}

func (s *Server) missionShow(ctx context.Context, _ domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, err := s.missions()
	if err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.MissionShowParams](raw)
	if perr != nil {
		return nil, perr
	}
	out, callErr := svc.Show(ctx, p)
	if callErr != nil {
		return nil, rpcError(callErr)
	}
	return out, nil
}

func (s *Server) missionReplaceIntegrator(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, err := s.missions()
	if err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.MissionReplaceIntegratorParams](raw)
	if perr != nil {
		return nil, perr
	}
	out, callErr := svc.ReplaceIntegrator(ctx, member, p)
	if callErr != nil {
		return nil, rpcError(callErr)
	}
	return out, nil
}

func (s *Server) missionQuestionAnswer(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, err := s.missions()
	if err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.MissionQuestionAnswerParams](raw)
	if perr != nil {
		return nil, perr
	}
	out, callErr := svc.AnswerQuestion(ctx, member, p)
	if callErr != nil {
		return nil, rpcError(callErr)
	}
	return out, nil
}

func (s *Server) missionPlanDecide(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, err := s.missions()
	if err != nil {
		return nil, err
	}
	p, perr := decodeParams[protocol.MissionPlanDecideParams](raw)
	if perr != nil {
		return nil, perr
	}
	out, callErr := svc.DecidePlan(ctx, member, p)
	if callErr != nil {
		return nil, rpcError(callErr)
	}
	return out, nil
}
