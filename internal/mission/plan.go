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

// maxMissionTextBytes bounds every free-text body the plan gate carries.
// internal/store does not import internal/protocol, so the wire cap is
// applied here, once, for both the agent socket and the control channel.
const maxMissionTextBytes = protocol.CoordMaxBodyBytes

// planShowPoll is the re-read interval of mission.plan.show's bounded wait.
// The gate changes through human action, so a poll is cheaper than a waiter
// registry and cannot leak a channel when an integrator is replaced mid-wait.
const planShowPoll = 500 * time.Millisecond

// planSnapshot is the tuple mission.plan.show waits on. Comparing it is the
// whole change test: anything an integrator must react to moves one field.
type planSnapshot struct {
	phase                domain.MissionPhase
	planVersion          uint64
	integratorGeneration uint64
	answered             int
	latestDecision       domain.MissionPlanDecision
}

// missionPhaseRefusal repeats the store's gate at the service boundary so an
// agent is refused before any work is done. It wraps the store's sentinel:
// both RPC classifiers must see the same error from either layer.
func missionPhaseRefusal(m *domain.Mission, operation string) error {
	return fmt.Errorf("%w: %s: mission is in phase %s", store.ErrMissionPhase, operation, m.Phase)
}

func (s *Service) handlePlanAgent(ctx context.Context, run domain.RunID, method string, raw json.RawMessage) (any, error) {
	switch method {
	case protocol.MethodMissionQuestionAsk:
		return s.questionAsk(ctx, run, raw)
	case protocol.MethodMissionClarificationComplete:
		return s.clarificationComplete(ctx, run, raw)
	case protocol.MethodMissionPlanShow:
		return s.planShow(ctx, run, raw)
	case protocol.MethodMissionPlanSubmit:
		return s.planSubmit(ctx, run, raw)
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
	if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
		return nil, publishErr
	}
	return protocol.MissionQuestionResult{Question: protocol.MissionQuestionFromDomain(question)}, nil
}

// clarificationComplete is the integrator declaring that it has what it needs
// to plan. Questions are optional; this call is what separates "still asking"
// from "ready to submit", and the store refuses it while an answer is pending.
func (s *Service) clarificationComplete(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.MissionClarificationCompleteParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	if !validTaskKey(p.IdempotencyKey) {
		return nil, errors.New("mission: mission.clarification.complete requires idempotency_key")
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.integratorMission(ctx, run, "")
	if err != nil {
		return nil, err
	}
	if _, completeErr := s.cfg.Missions.CompleteMissionClarification(ctx, m.ID, run, p.IdempotencyKey); completeErr != nil {
		return nil, completeErr
	}
	if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
		return nil, publishErr
	}
	state, _, _, err := s.planState(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	return protocol.MissionClarificationCompleteResult{Plan: state}, nil
}

func (s *Service) planSubmit(ctx context.Context, run domain.RunID, raw json.RawMessage) (any, error) {
	var p protocol.MissionPlanSubmitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Summary) == "" || !validTaskKey(p.IdempotencyKey) {
		return nil, errors.New("mission: mission.plan.submit requires summary and idempotency_key")
	}
	if len(p.Summary) > maxMissionTextBytes {
		return nil, fmt.Errorf("mission: mission.plan.submit summary is %d bytes, limit is %d", len(p.Summary), maxMissionTextBytes)
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.integratorMission(ctx, run, "")
	if err != nil {
		return nil, err
	}
	if _, submitErr := s.cfg.Missions.SubmitMissionPlan(ctx, m.ID, run, p.Summary, p.IdempotencyKey); submitErr != nil {
		return nil, submitErr
	}
	if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
		return nil, publishErr
	}
	state, _, _, err := s.planState(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	return protocol.MissionPlanSubmitResult{Plan: state}, nil
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
	state, questions, reviews, err := s.planState(ctx, missionID)
	if err != nil {
		return protocol.MissionPlanShowResult{}, planSnapshot{}, err
	}
	out := protocol.MissionPlanShowResult{
		Plan:        state,
		Questions:   make([]protocol.MissionQuestion, 0, len(questions)),
		PlanReviews: make([]protocol.MissionPlanReview, 0, len(reviews)),
	}
	answered := 0
	for _, question := range questions {
		if question.AnsweredAt != nil {
			answered++
		}
		out.Questions = append(out.Questions, protocol.MissionQuestionFromDomain(question))
	}
	for _, review := range reviews {
		out.PlanReviews = append(out.PlanReviews, protocol.MissionPlanReviewFromDomain(review))
	}
	snapshot := planSnapshot{
		phase: domain.MissionPhase(state.Phase), planVersion: state.PlanVersion,
		integratorGeneration: state.IntegratorGeneration, answered: answered,
	}
	if len(reviews) > 0 {
		snapshot.latestDecision = reviews[len(reviews)-1].Decision
	}
	return out, snapshot, nil
}

// planState composes what the store deliberately does not: OpenQuestions comes
// from GetMission, LatestFeedback from the most recent revise decision.
func (s *Service) planState(ctx context.Context, missionID domain.MissionID) (protocol.MissionPlanState, []*domain.MissionQuestion, []*domain.MissionPlanReview, error) {
	m, err := s.cfg.Missions.GetMission(ctx, missionID)
	if err != nil {
		return protocol.MissionPlanState{}, nil, nil, err
	}
	questions, err := s.cfg.Missions.ListMissionQuestions(ctx, missionID)
	if err != nil {
		return protocol.MissionPlanState{}, nil, nil, err
	}
	reviews, err := s.cfg.Missions.ListMissionPlanReviews(ctx, missionID)
	if err != nil {
		return protocol.MissionPlanState{}, nil, nil, err
	}
	return protocol.MissionPlanState{
		MissionID: string(m.ID), Phase: string(m.Phase), PlanVersion: m.PlanVersion,
		IntegratorGeneration: m.IntegratorGeneration, OpenQuestions: m.OpenQuestions,
		LatestFeedback: latestReviseFeedback(reviews),
	}, questions, reviews, nil
}

func latestReviseFeedback(reviews []*domain.MissionPlanReview) string {
	feedback := ""
	for _, review := range reviews {
		if review.Decision == domain.MissionPlanRevise {
			feedback = review.Feedback
		}
	}
	return feedback
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
	asked, m, err := s.missionForQuestion(ctx, domain.MissionQuestionID(p.QuestionID))
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
	// An answer can only be recorded once, so a question answered before
	// this call was answered by an idempotent replay the integrator was
	// already told about.
	if asked.AnsweredAt == nil {
		s.noticeIntegrator(ctx, m, answerNotice)
	}
	if publishErr := s.publishMissionChanged(ctx, m.ID); publishErr != nil {
		return protocol.MissionQuestionResult{Question: protocol.MissionQuestionFromDomain(question)}, publishErr
	}
	return protocol.MissionQuestionResult{Question: protocol.MissionQuestionFromDomain(question)}, nil
}

// DecidePlan is the human boundary before any worker runs: it approves,
// requests changes to, or rejects one submitted plan version.
func (s *Service) DecidePlan(ctx context.Context, actor domain.MemberID, p protocol.MissionPlanDecideParams) (protocol.MissionPlanDecideResult, error) {
	if p.MissionID == "" || p.ExpectedPlanVersion == 0 || !validTaskKey(p.IdempotencyKey) {
		return protocol.MissionPlanDecideResult{}, invalidMissionParams("mission_id, expected_plan_version, and idempotency_key are required")
	}
	decision := domain.MissionPlanDecision(p.Decision)
	if !decision.Valid() {
		return protocol.MissionPlanDecideResult{}, invalidMissionParams(fmt.Sprintf("decision %q must be approve, revise, or reject", p.Decision))
	}
	if decision == domain.MissionPlanRevise && strings.TrimSpace(p.Feedback) == "" {
		return protocol.MissionPlanDecideResult{}, invalidMissionParams("requesting changes requires feedback")
	}
	if len(p.Feedback) > maxMissionTextBytes {
		return protocol.MissionPlanDecideResult{}, invalidMissionParams(fmt.Sprintf("feedback is %d bytes, limit is %d", len(p.Feedback), maxMissionTextBytes))
	}
	s.cfg.AuthorizationMu.Lock()
	defer s.cfg.AuthorizationMu.Unlock()
	m, err := s.cfg.Missions.GetMission(ctx, domain.MissionID(p.MissionID))
	if err != nil {
		return protocol.MissionPlanDecideResult{}, err
	}
	if authErr := s.authorizeMissionHuman(ctx, actor, m, "decide this plan"); authErr != nil {
		return protocol.MissionPlanDecideResult{}, authErr
	}
	// Approval is what lets the mission dispatch, so the mission's own launch
	// admission is re-resolved for it: an accountable human who lost Launch
	// or the account share cannot carry the mission past this gate. Revise
	// and reject dispatch nothing and stay available, or plan_review would
	// have no exit once that admission is gone.
	if decision == domain.MissionPlanApprove {
		if _, admissionErr := sshd.AuthorizeLaunch(ctx, s.cfg.Store, m.AccountableHumanID, string(m.Integrator.AccountMemberID)); admissionErr != nil {
			return protocol.MissionPlanDecideResult{}, admissionErr
		}
	}
	decided, err := s.cfg.Missions.DecideMissionPlan(ctx, m.ID, p.ExpectedPlanVersion, decision, p.Feedback, actor, p.IdempotencyKey)
	if err != nil {
		return protocol.MissionPlanDecideResult{}, err
	}
	// A fresh decision always moves the mission out of review of the version
	// it names; a mission read outside that state was decided by an
	// idempotent replay, whose notice was already sent.
	if (m.Phase == domain.MissionPhasePlanReview || m.Phase == domain.MissionPhaseAmendmentReview) && m.PlanVersion == p.ExpectedPlanVersion {
		s.noticeIntegrator(ctx, decided, planDecisionNotice(decision))
	}
	if publishErr := s.publishMissionChanged(ctx, decided.ID); publishErr != nil {
		return protocol.MissionPlanDecideResult{Mission: protocol.MissionFromDomain(decided)}, publishErr
	}
	// DecideMissionPlan reads the mission inside its own transaction, which
	// leaves OpenQuestions zero; re-read so the answer carries the count.
	current, err := s.cfg.Missions.GetMission(ctx, decided.ID)
	if err != nil {
		return protocol.MissionPlanDecideResult{Mission: protocol.MissionFromDomain(decided)}, err
	}
	return protocol.MissionPlanDecideResult{Mission: protocol.MissionFromDomain(current)}, nil
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
