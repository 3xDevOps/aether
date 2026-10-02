package sshd

import (
	"context"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// planGateMissionService takes the shared authorization mutex the way the real
// mission service does, so a handler that also took it would deadlock here.
type planGateMissionService struct {
	mu    *sync.Mutex
	actor domain.MemberID
}

func (p *planGateMissionService) Create(context.Context, domain.MemberID, protocol.MissionCreateParams) (protocol.MissionCreateResult, error) {
	return protocol.MissionCreateResult{}, nil
}

func (p *planGateMissionService) List(context.Context, protocol.MissionListParams) (protocol.MissionListResult, error) {
	return protocol.MissionListResult{}, nil
}

func (p *planGateMissionService) Show(context.Context, protocol.MissionShowParams) (protocol.MissionShowResult, error) {
	return protocol.MissionShowResult{}, nil
}

func (p *planGateMissionService) ReplaceIntegrator(context.Context, domain.MemberID, protocol.MissionReplaceIntegratorParams) (protocol.MissionReplaceIntegratorResult, error) {
	return protocol.MissionReplaceIntegratorResult{}, nil
}

func (p *planGateMissionService) AnswerQuestion(_ context.Context, actor domain.MemberID, params protocol.MissionQuestionAnswerParams) (protocol.MissionQuestionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actor = actor
	return protocol.MissionQuestionResult{Question: protocol.MissionQuestion{ID: params.QuestionID, Answer: params.Answer}}, nil
}

func (p *planGateMissionService) DecidePlan(_ context.Context, actor domain.MemberID, params protocol.MissionPlanDecideParams) (protocol.MissionPlanDecideResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actor = actor
	return protocol.MissionPlanDecideResult{Mission: protocol.Mission{ID: params.MissionID, Phase: string(domain.MissionPhaseActive)}}, nil
}

func (p *planGateMissionService) Cancel(_ context.Context, actor domain.MemberID, params protocol.MissionCancelParams) (protocol.MissionCancelResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actor = actor
	return protocol.MissionCancelResult{Mission: protocol.Mission{ID: params.MissionID, Phase: string(domain.MissionPhaseRejected)}}, nil
}

// TestMissionPlanGateControlMethods: the human plan gate methods are Launch-guarded
// control-channel methods whose handlers are thin - they carry the
// authenticated member into the service and never take the authorization
// mutex the service itself holds.
func TestMissionPlanGateControlMethods(t *testing.T) {
	t.Parallel()
	shared := &sync.Mutex{}
	svc := &planGateMissionService{mu: shared}
	e := newTestEnv(t, func(cfg *Config) {
		cfg.AuthorizationMu = shared
		cfg.Services.Missions = svc
	})
	adminC := controlClient(t, e)

	var answered protocol.MissionQuestionResult
	if err := adminC.Call(protocol.MethodMissionQuestionAnswer, protocol.MissionQuestionAnswerParams{
		QuestionID: "question-1", Answer: "use the existing flow", IdempotencyKey: "answer-1",
	}, &answered); err != nil {
		t.Fatalf("mission.question.answer: %v", err)
	}
	if answered.Question.ID != "question-1" || svc.actor != e.member.ID {
		t.Fatalf("answer reached the service as %+v by %q, want question-1 by %q", answered.Question, svc.actor, e.member.ID)
	}

	var decided protocol.MissionPlanDecideResult
	if err := adminC.Call(protocol.MethodMissionPlanDecide, protocol.MissionPlanDecideParams{
		MissionID: "mission-1", ExpectedPlanVersion: 1, Decision: string(domain.MissionPlanApprove),
		IdempotencyKey: "decide-1",
	}, &decided); err != nil {
		t.Fatalf("mission.plan.decide: %v", err)
	}
	if decided.Mission.ID != "mission-1" || svc.actor != e.member.ID {
		t.Fatalf("decision reached the service as %+v by %q, want mission-1 by %q", decided.Mission, svc.actor, e.member.ID)
	}

	var cancelled protocol.MissionCancelResult
	if err := adminC.Call(protocol.MethodMissionCancel, protocol.MissionCancelParams{
		MissionID: "mission-1", IdempotencyKey: "cancel-1",
	}, &cancelled); err != nil {
		t.Fatalf("mission.cancel: %v", err)
	}
	if cancelled.Mission.Phase != string(domain.MissionPhaseRejected) || svc.actor != e.member.ID {
		t.Fatalf("cancel reached the service as %+v by %q, want rejected mission-1 by %q", cancelled.Mission, svc.actor, e.member.ID)
	}

	viewer, _ := addMember(t, e, "Vera", domain.RoleViewer, false)
	viewerC := controlAs(t, e, viewer)
	wantDenied(t, viewerC.Call(protocol.MethodMissionQuestionAnswer, protocol.MissionQuestionAnswerParams{
		QuestionID: "question-1", Answer: "not mine", IdempotencyKey: "answer-viewer",
	}, nil), "viewer mission.question.answer")
	wantDenied(t, viewerC.Call(protocol.MethodMissionPlanDecide, protocol.MissionPlanDecideParams{
		MissionID: "mission-1", ExpectedPlanVersion: 1, Decision: string(domain.MissionPlanApprove),
		IdempotencyKey: "decide-viewer",
	}, nil), "viewer mission.plan.decide")
	wantDenied(t, viewerC.Call(protocol.MethodMissionCancel, protocol.MissionCancelParams{
		MissionID: "mission-1", IdempotencyKey: "cancel-viewer",
	}, nil), "viewer mission.cancel")
}
