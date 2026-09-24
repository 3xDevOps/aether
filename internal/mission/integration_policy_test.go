package mission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	feature "github.com/3xDevOps/Aether/internal/integration"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type agentIntegrationNoop struct{}

func (agentIntegrationNoop) Prepare(context.Context, feature.Actor, protocol.IntegrationPrepareParams) (protocol.Candidate, error) {
	return protocol.Candidate{}, nil
}
func (agentIntegrationNoop) Show(context.Context, feature.Actor, protocol.IntegrationShowParams) (protocol.Candidate, error) {
	return protocol.Candidate{}, nil
}
func (agentIntegrationNoop) List(context.Context, feature.Actor, protocol.IntegrationListParams) (protocol.IntegrationListResult, error) {
	return protocol.IntegrationListResult{}, nil
}
func (agentIntegrationNoop) Patch(context.Context, feature.Actor, protocol.IntegrationShowParams) (protocol.IntegrationPatchResult, error) {
	return protocol.IntegrationPatchResult{}, nil
}
func (agentIntegrationNoop) Resolve(context.Context, feature.Actor, protocol.IntegrationResolveParams) (protocol.Candidate, error) {
	return protocol.Candidate{}, nil
}
func (agentIntegrationNoop) Verify(context.Context, feature.Actor, protocol.IntegrationVerifyParams) (protocol.Candidate, error) {
	return protocol.Candidate{}, nil
}
func (agentIntegrationNoop) RequestDelivery(context.Context, feature.Actor, protocol.IntegrationRequestDeliveryParams) (protocol.Candidate, error) {
	return protocol.Candidate{}, nil
}
func (agentIntegrationNoop) Decide(context.Context, feature.Actor, protocol.IntegrationDecideParams) (protocol.Candidate, error) {
	return protocol.Candidate{}, nil
}
func (agentIntegrationNoop) Deliver(context.Context, feature.Actor, protocol.IntegrationDeliverParams) (protocol.Candidate, error) {
	return protocol.Candidate{}, nil
}
func (agentIntegrationNoop) Delete(context.Context, feature.Actor, protocol.IntegrationDeleteParams) error {
	return nil
}

func acceptedIntegrationPolicyFixture(t *testing.T) (*Service, *domain.Mission, *domain.Member, protocol.SubmissionRef) {
	t.Helper()
	ctx := context.Background()
	db, mission, _, submission, _, _ := setupSubmissionRegression(t)
	if _, err := db.AcceptSubmission(ctx, submission.ID, mission.CurrentIntegratorRunID, mission.IntegratorGeneration, mission.AcceptedSetVersion, "", "integration-policy-accept"); err != nil {
		t.Fatalf("accept submission: %v", err)
	}
	mission, err := db.GetMission(ctx, mission.ID)
	if err != nil {
		t.Fatalf("reload mission: %v", err)
	}
	member, err := db.GetMember(ctx, mission.AccountableHumanID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	svc, err := New(Config{Store: db, Missions: db, AuthorizationMu: &sync.Mutex{}})
	if err != nil {
		t.Fatalf("new mission service: %v", err)
	}
	return svc, mission, member, protocol.SubmissionRef{
		WorkspaceID: string(submission.Ref.WorkspaceID), RunID: string(submission.Ref.RunID),
		EvidenceRef: submission.Ref.EvidenceRef, RetainedRevision: submission.Ref.RetainedRevision,
	}
}

func TestHandleAgentIntegrationPrepareDeniesStaleIntegrator(t *testing.T) {
	ctx := context.Background()
	svc, mission, _, _ := acceptedIntegrationPolicyFixture(t)
	db := svc.cfg.Store.(*store.DB)
	staleRun := mission.CurrentIntegratorRunID
	replacement := regressionMember(t, db, "replacement-agent")
	choice := domain.MissionIntegrator{
		AccountMemberID: replacement.ID,
		Harness:         "claude",
		Mode:            domain.LaunchTUI,
	}
	replaced, err := db.ReplaceIntegrator(ctx, mission.ID, mission.IntegratorGeneration, choice, replacement.ID, replacement.ID, "integration-agent-stale")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	regressionRun(t, db, replaced.CurrentIntegratorRunID, replaced.WorkspaceID, replacement.ID, "replacement-agent-run")

	svc.SetIntegrationService(agentIntegrationNoop{})
	_, err = svc.HandleAgent(ctx, staleRun, protocol.MethodIntegrationPrepare, []byte(`{
		"target_ref":"refs/heads/main",
		"expected_target_revision":"revision-1",
		"idempotency_key":"stale-agent"
	}`))
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("stale integration agent error = %v, want protocol error", err)
	}
	if perr.Code != protocol.CodeDenied {
		t.Fatalf("stale integration agent code = %d, want %d", perr.Code, protocol.CodeDenied)
	}
	if perr.Message == "" {
		t.Fatal("stale integration agent error message is empty")
	}

	_, err = svc.HandleAgent(ctx, replaced.CurrentIntegratorRunID, protocol.MethodIntegrationPrepare, []byte(`{`))
	perr = nil
	if !errors.As(err, &perr) {
		t.Fatalf("malformed integration agent error = %v, want protocol error", err)
	}
	if perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("malformed integration agent code = %d, want %d", perr.Code, protocol.CodeInvalidParams)
	}

}

func TestIntegrationPrepareResolvesMissionAndRejectsForgedRefs(t *testing.T) {
	ctx := context.Background()
	svc, mission, member, accepted := acceptedIntegrationPolicyFixture(t)
	resolved, err := svc.prepareIntegration(ctx, feature.Actor{MemberID: member.ID}, protocol.IntegrationPrepareParams{
		Submissions: []protocol.SubmissionRef{accepted}, TargetRef: "refs/heads/main", ExpectedTargetRevision: "revision-1", IdempotencyKey: "resolve-mission",
	})
	if err != nil {
		t.Fatalf("prepare omission: %v", err)
	}
	if resolved.WorkspaceID != string(mission.WorkspaceID) || resolved.MissionID != string(mission.ID) || len(resolved.Submissions) != 1 || resolved.Submissions[0] != accepted {
		t.Fatalf("resolved prepare = %+v, want mission/workspace/current source", resolved)
	}
	forged := accepted
	forged.EvidenceRef = "forged-evidence"
	if _, err := svc.prepareIntegration(ctx, feature.Actor{MemberID: member.ID}, protocol.IntegrationPrepareParams{
		WorkspaceID: string(mission.WorkspaceID), MissionID: string(mission.ID), Submissions: []protocol.SubmissionRef{forged},
		TargetRef: "refs/heads/main", ExpectedTargetRevision: "revision-1", IdempotencyKey: "forged-source",
	}); !errors.Is(err, feature.ErrConflict) {
		t.Fatalf("forged prepare error = %v, want integration conflict", err)
	}
}

func TestIntegrationPrepareRejectsMixedMissionAndOrdinaryRefsInEitherOrder(t *testing.T) {
	ctx := context.Background()
	svc, _, member, accepted := acceptedIntegrationPolicyFixture(t)
	ordinary := accepted
	ordinary.RunID = "ordinary-run"
	for _, refs := range [][]protocol.SubmissionRef{
		{ordinary, accepted},
		{accepted, ordinary},
	} {
		_, err := svc.prepareIntegration(ctx, feature.Actor{MemberID: member.ID}, protocol.IntegrationPrepareParams{
			Submissions: refs, TargetRef: "refs/heads/main", ExpectedTargetRevision: "revision-1",
			IdempotencyKey: "mixed-ref-order",
		})
		if !errors.Is(err, feature.ErrConflict) {
			t.Fatalf("mixed refs %v error = %v, want conflict", refs, err)
		}
	}
}

func TestIntegrationPrepareRejectsMalformedMissionRef(t *testing.T) {
	ctx := context.Background()
	svc, _, member, accepted := acceptedIntegrationPolicyFixture(t)
	accepted.EvidenceRef = ""
	_, err := svc.prepareIntegration(ctx, feature.Actor{MemberID: member.ID}, protocol.IntegrationPrepareParams{
		Submissions: []protocol.SubmissionRef{accepted}, TargetRef: "refs/heads/main",
		ExpectedTargetRevision: "revision-1", IdempotencyKey: "malformed-mission-ref",
	})
	if !errors.Is(err, feature.ErrConflict) {
		t.Fatalf("malformed mission ref error = %v, want conflict", err)
	}
}

func TestIntegrationAdmissionBindsSubsetAndFencesVersion(t *testing.T) {
	ctx := context.Background()
	svc, mission, member, accepted := acceptedIntegrationPolicyFixture(t)
	mu := svc.cfg.AuthorizationMu
	candidate := &protocol.Candidate{
		CandidateID: "candidate-subset", WorkspaceID: string(mission.WorkspaceID), MissionID: string(mission.ID),
		Submissions: []protocol.SubmissionRef{accepted},
	}
	release, err := svc.AdmitIntegration(ctx, feature.Admission{
		Operation: protocol.MethodIntegrationPrepare, Actor: feature.Actor{MemberID: member.ID},
		WorkspaceID: mission.WorkspaceID, MissionID: string(mission.ID), Candidate: candidate,
		Submissions: candidate.Submissions, NewCandidate: true,
	})
	if err != nil {
		t.Fatalf("subset admission: %v", err)
	}
	if candidate.MissionAcceptedSetVersion != mission.AcceptedSetVersion {
		t.Fatalf("candidate set version = %d, want %d", candidate.MissionAcceptedSetVersion, mission.AcceptedSetVersion)
	}
	blocked := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		close(blocked)
		mu.Lock()
		close(acquired)
		mu.Unlock()
	}()
	<-blocked
	select {
	case <-acquired:
		t.Fatal("authorization fence released before engine release callback")
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("authorization fence remained held after release callback")
	}

	stale := *candidate
	stale.MissionAcceptedSetVersion++
	if _, err := svc.AdmitIntegration(ctx, feature.Admission{
		Operation: protocol.MethodIntegrationVerify, Actor: feature.Actor{MemberID: member.ID},
		WorkspaceID: mission.WorkspaceID, MissionID: string(mission.ID), Candidate: &stale,
	}); !errors.Is(err, feature.ErrConflict) {
		t.Fatalf("stale candidate admission error = %v, want integration conflict", err)
	}
}

func TestIntegrationAdmissionUsesCurrentHumanWorkspacePermission(t *testing.T) {
	ctx := context.Background()
	svc, mission, _, accepted := acceptedIntegrationPolicyFixture(t)
	db := svc.cfg.Store.(*store.DB)
	reviewer := regressionMember(t, db, "reviewer")
	candidate := &protocol.Candidate{
		CandidateID: "candidate-reviewer", WorkspaceID: string(mission.WorkspaceID), MissionID: string(mission.ID),
		Submissions: []protocol.SubmissionRef{accepted}, MissionAcceptedSetVersion: mission.AcceptedSetVersion,
	}
	release, err := svc.AdmitIntegration(ctx, feature.Admission{
		Operation: protocol.MethodIntegrationVerify, Actor: feature.Actor{MemberID: reviewer.ID},
		WorkspaceID: mission.WorkspaceID, MissionID: string(mission.ID), Candidate: candidate,
	})
	if err != nil {
		t.Fatalf("currently authorized human admission: %v", err)
	}
	release()
}

func TestIntegrationAdmissionAllowsReplacementWithUnchangedAcceptedSet(t *testing.T) {
	ctx := context.Background()
	svc, mission, _, accepted := acceptedIntegrationPolicyFixture(t)
	db := svc.cfg.Store.(*store.DB)
	replacement := regressionMember(t, db, "replacement")
	choice := domain.MissionIntegrator{AccountMemberID: replacement.ID, Harness: "claude", Mode: domain.LaunchTUI}
	replaced, err := db.ReplaceIntegrator(ctx, mission.ID, mission.IntegratorGeneration, choice, replacement.ID, replacement.ID, "integration-policy-replace")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	regressionRun(t, db, replaced.CurrentIntegratorRunID, replaced.WorkspaceID, replacement.ID, "replacement-integrator")
	candidate := &protocol.Candidate{
		CandidateID: "candidate-replacement", WorkspaceID: string(replaced.WorkspaceID), MissionID: string(replaced.ID),
		Submissions: []protocol.SubmissionRef{accepted}, MissionAcceptedSetVersion: replaced.AcceptedSetVersion,
	}
	release, err := svc.AdmitIntegration(ctx, feature.Admission{
		Operation: protocol.MethodIntegrationVerify, Actor: feature.Actor{RunID: replaced.CurrentIntegratorRunID},
		WorkspaceID: replaced.WorkspaceID, MissionID: string(replaced.ID), Candidate: candidate,
	})
	if err != nil {
		t.Fatalf("replacement admission with unchanged accepted set: %v", err)
	}
	release()
}

func TestIntegrationAdmissionRechecksRevokedAgentHuman(t *testing.T) {
	ctx := context.Background()
	svc, mission, member, accepted := acceptedIntegrationPolicyFixture(t)
	member.Pending = true
	if err := svc.cfg.Store.UpdateMember(ctx, member); err != nil {
		t.Fatalf("revoke authorizer: %v", err)
	}
	candidate := &protocol.Candidate{
		CandidateID: "candidate-revoked", WorkspaceID: string(mission.WorkspaceID), MissionID: string(mission.ID),
		Submissions: []protocol.SubmissionRef{accepted}, MissionAcceptedSetVersion: mission.AcceptedSetVersion,
	}
	if _, err := svc.AdmitIntegration(ctx, feature.Admission{
		Operation: protocol.MethodIntegrationVerify, Actor: feature.Actor{RunID: mission.CurrentIntegratorRunID},
		WorkspaceID: mission.WorkspaceID, MissionID: string(mission.ID), Candidate: candidate,
	}); err == nil {
		t.Fatal("revoked agent authorizer unexpectedly passed integration admission")
	}
}
