package mission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// maxMissionTextBytes bounds every free-text body a mission question carries.
// internal/store does not import internal/protocol, so the wire cap is
// applied here, once, for both the agent socket and the control channel.
const maxMissionTextBytes = protocol.CoordMaxBodyBytes

// planShowPoll is the re-read interval of mission.plan.show's bounded wait.
// The plan changes through human answers, so a poll is cheaper than a waiter
// registry and cannot leak a channel when an integrator is replaced mid-wait.
const planShowPoll = 500 * time.Millisecond

// planSnapshot is the tuple mission.plan.show waits on. Comparing it is the
// whole change test: anything an integrator must react to moves one field.
type planSnapshot struct {
	phase                domain.MissionPhase
	integratorGeneration uint64
	acceptedSetVersion   uint64
	answered             int
}

// missionPhaseRefusal repeats the store's phase check at the service boundary so an
// agent is refused before any work is done. It wraps the store's sentinel:
// both RPC classifiers must see the same error from either layer.
func missionPhaseRefusal(m *domain.Mission, operation string) error {
	return fmt.Errorf("%w: %s: mission is in phase %s", store.ErrMissionPhase, operation, m.Phase)
}

func (s *Service) handlePlanAgent(ctx context.Context, run domain.RunID, method string, raw json.RawMessage) (any, error) {
	switch method {
	case protocol.MethodMissionQuestionAsk:
		return s.questionAsk(ctx, run, raw)
	case protocol.MethodMissionPlanShow:
		return s.planShow(ctx, run, raw)
	case protocol.MethodMissionStart:
		return s.start(ctx, run, raw)
	default:
		return nil, errors.New("mission: method not found")
	}
}

func (s *Service) questionAsk(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.MissionQuestionAskParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Body) == "" || !validTaskKey(p.IdempotencyKey) {
		return nil, errors.New("mission: mission.question.ask requires body and idempotency_key")
	}
	if len(p.Body) > maxMissionTextBytes {
		return nil, fmt.Errorf("mission: mission.question.ask body is %d bytes, limit is %d", len(p.Body), maxMissionTextBytes)
	}
	// The authorization lock is what keeps mission.replace-integrator from
	// landing between resolving the integrator and the store write.
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.integratorMission(ctx, run, "")
	if err != nil {
		return nil, err
	}
	question, err := s.cfg.Missions.InsertMissionQuestion(ctx, m.ID, run, p.Body, p.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if publishErr := s.missionChanged(ctx, m.ID, domain.MissionQuestionAsked, run); publishErr != nil {
		return nil, publishErr
	}
	return protocol.MissionQuestionResult{Question: protocol.MissionQuestionFromDomain(question)}, nil
}

// start is the integrator's mission.start: it accepts every proposed task
// and moves the mission to active. The mission's launch admission is
// re-resolved first, since starting is what lets workers dispatch: an
// accountable human who lost Launch or the account share cannot carry the
// mission forward.
func (s *Service) start(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.MissionStartParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.MissionID == "" || !validTaskKey(p.IdempotencyKey) {
		return nil, errors.New("mission: mission.start requires mission_id and idempotency_key")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.integratorMission(ctx, run, p.MissionID)
	if err != nil {
		return nil, err
	}
	if _, admissionErr := sshd.AuthorizeLaunch(ctx, s.cfg.Store, m.AccountableHumanID, string(m.Integrator.AccountMemberID)); admissionErr != nil {
		return nil, admissionErr
	}
	if _, startErr := s.cfg.Missions.StartMission(ctx, m.ID, run, p.IdempotencyKey); startErr != nil {
		return nil, startErr
	}
	if publishErr := s.missionChanged(ctx, m.ID, domain.MissionPhaseChanged, run); publishErr != nil {
		return nil, publishErr
	}
	state, _, err := s.planState(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	return protocol.MissionStartResult{Plan: state}, nil
}

func (s *Service) planShow(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.MissionPlanShowParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	if p.WaitSeconds < 0 || p.WaitSeconds > protocol.CoordMaxInboxWaitSeconds {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf(
			"%s: wait_seconds must be between 0 and %d", protocol.MethodMissionPlanShow, protocol.CoordMaxInboxWaitSeconds)}
	}
	m, err := s.integratorMission(ctx, run, "")
	if err != nil {
		return nil, err
	}
	result, snapshot, err := s.planShowResult(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	if p.WaitSeconds == 0 {
		return result, nil
	}
	deadline := time.NewTimer(time.Duration(p.WaitSeconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(planShowPoll)
	defer ticker.Stop()
	// The service run context ends the wait on shutdown; a cancelled request
	// returns the state already read rather than an error, so a disconnecting
	// agent still gets a well-formed answer from its last read.
	service := s.operationContext(ctx)
	for {
		select {
		case <-ctx.Done():
			return result, nil
		case <-service.Done():
			return result, nil
		case <-deadline.C:
			return result, nil
		case <-ticker.C:
			if _, staleErr := s.integratorMission(ctx, run, ""); staleErr != nil {
				return nil, staleErr
			}
			next, nextSnapshot, readErr := s.planShowResult(ctx, m.ID)
			if readErr != nil {
				return nil, readErr
			}
			if nextSnapshot != snapshot {
				return next, nil
			}
			result = next
		}
	}
}

func (s *Service) planShowResult(ctx context.Context, missionID domain.MissionID) (protocol.MissionPlanShowResult, planSnapshot, error) {
	state, questions, err := s.planState(ctx, missionID)
	if err != nil {
		return protocol.MissionPlanShowResult{}, planSnapshot{}, err
	}
	out := protocol.MissionPlanShowResult{
		Plan:      state,
		Questions: make([]protocol.MissionQuestion, 0, len(questions)),
	}
	answered := 0
	for _, question := range questions {
		if question.AnsweredAt != nil {
			answered++
		}
		out.Questions = append(out.Questions, protocol.MissionQuestionFromDomain(question))
	}
	snapshot := planSnapshot{
		phase: domain.MissionPhase(state.Phase), integratorGeneration: state.IntegratorGeneration,
		answered: answered, acceptedSetVersion: state.AcceptedSetVersion,
	}
	return out, snapshot, nil
}

// planState composes what the store deliberately does not: OpenQuestions comes
// from GetMission.
func (s *Service) planState(ctx context.Context, missionID domain.MissionID) (protocol.MissionPlanState, []*domain.MissionQuestion, error) {
	m, err := s.cfg.Missions.GetMission(ctx, missionID)
	if err != nil {
		return protocol.MissionPlanState{}, nil, err
	}
	questions, err := s.cfg.Missions.ListMissionQuestions(ctx, missionID)
	if err != nil {
		return protocol.MissionPlanState{}, nil, err
	}
	return protocol.MissionPlanState{
		MissionID: string(m.ID), Phase: string(m.Phase),
		IntegratorGeneration: m.IntegratorGeneration, OpenQuestions: m.OpenQuestions,
		AcceptedSetVersion: m.AcceptedSetVersion,
	}, questions, nil
}

// AnswerQuestion records the accountable human's answer to one clarifying
// question. The answering member is the authenticated session.
func (s *Service) AnswerQuestion(ctx context.Context, actor domain.MemberID, p protocol.MissionQuestionAnswerParams) (protocol.MissionQuestionResult, error) {
	if strings.TrimSpace(p.QuestionID) == "" || strings.TrimSpace(p.Answer) == "" || !validTaskKey(p.IdempotencyKey) {
		return protocol.MissionQuestionResult{}, invalidMissionParams("question_id, a non-empty answer, and idempotency_key are required")
	}
	if len(p.Answer) > maxMissionTextBytes {
		return protocol.MissionQuestionResult{}, invalidMissionParams(fmt.Sprintf("answer is %d bytes, limit is %d", len(p.Answer), maxMissionTextBytes))
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	_, m, err := s.missionForQuestion(ctx, domain.MissionQuestionID(p.QuestionID))
	if err != nil {
		return protocol.MissionQuestionResult{}, err
	}
	if authErr := s.authorizeMissionHuman(ctx, actor, m, "answer a mission question"); authErr != nil {
		return protocol.MissionQuestionResult{}, authErr
	}
	question, err := s.cfg.Missions.AnswerMissionQuestion(ctx, domain.MissionQuestionID(p.QuestionID), actor, p.Answer, p.IdempotencyKey)
	if err != nil {
		return protocol.MissionQuestionResult{}, err
	}
	if publishErr := s.missionChanged(ctx, m.ID, domain.MissionQuestionAnswered, ""); publishErr != nil {
		return protocol.MissionQuestionResult{Question: protocol.MissionQuestionFromDomain(question)}, publishErr
	}
	return protocol.MissionQuestionResult{Question: protocol.MissionQuestionFromDomain(question)}, nil
}

// Cancel ends a planning or active mission. It only records the cancelled
// phase; the reconcile loop stops the mission's workers and integrator.
func (s *Service) Cancel(ctx context.Context, actor domain.MemberID, p protocol.MissionCancelParams) (protocol.MissionCancelResult, error) {
	if p.MissionID == "" || !validTaskKey(p.IdempotencyKey) {
		return protocol.MissionCancelResult{}, invalidMissionParams("mission_id and idempotency_key are required")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.cfg.Missions.GetMission(ctx, domain.MissionID(p.MissionID))
	if err != nil {
		return protocol.MissionCancelResult{}, err
	}
	if authErr := s.authorizeMissionHuman(ctx, actor, m, "cancel this mission"); authErr != nil {
		return protocol.MissionCancelResult{}, authErr
	}
	if _, cancelErr := s.cfg.Missions.CancelMission(ctx, m.ID, actor, p.IdempotencyKey); cancelErr != nil {
		return protocol.MissionCancelResult{}, cancelErr
	}
	if publishErr := s.missionChanged(ctx, m.ID, domain.MissionPhaseChanged, ""); publishErr != nil {
		return protocol.MissionCancelResult{}, publishErr
	}
	current, err := s.cfg.Missions.GetMission(ctx, m.ID)
	if err != nil {
		return protocol.MissionCancelResult{}, err
	}
	return protocol.MissionCancelResult{Mission: protocol.MissionFromDomain(current)}, nil
}

// invalidMissionParams is a caller mistake on the control channel. It is
// typed so sshd reports it as invalid params rather than an internal fault.
func invalidMissionParams(message string) error {
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: message}
}

func (s *Service) authorizeMissionHuman(ctx context.Context, actor domain.MemberID, m *domain.Mission, operation string) error {
	requesting, err := s.cfg.Store.GetMember(ctx, actor)
	if err != nil {
		return err
	}
	if actor != m.AccountableHumanID && requesting.Role != domain.RoleAdmin {
		return fmt.Errorf("%w: only the accountable human or an admin may %s", permissions.ErrDenied, operation)
	}
	return nil
}

// missionForQuestion resolves a question and the mission it belongs to, which
// is what the accountable-human rule is checked against. The wire carries no
// mission_id: a question id names its mission on its own.
func (s *Service) missionForQuestion(ctx context.Context, id domain.MissionQuestionID) (*domain.MissionQuestion, *domain.Mission, error) {
	question, err := s.cfg.Missions.GetMissionQuestion(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	m, err := s.cfg.Missions.GetMission(ctx, question.MissionID)
	return question, m, err
}
