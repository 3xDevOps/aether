package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	integration "github.com/3xDevOps/Aether/internal/integration"
	"github.com/3xDevOps/Aether/internal/protocol"
)

type integrationHandlerFake struct {
	candidate protocol.Candidate
	actor     integration.Actor
	calls     int
	err       error
}

func (f *integrationHandlerFake) record(actor integration.Actor) (protocol.Candidate, error) {
	f.actor = actor
	f.calls++
	return f.candidate, f.err
}

func (f *integrationHandlerFake) Prepare(_ context.Context, actor integration.Actor, _ protocol.IntegrationPrepareParams) (protocol.Candidate, error) {
	return f.record(actor)
}
func (f *integrationHandlerFake) Show(_ context.Context, actor integration.Actor, _ protocol.IntegrationShowParams) (protocol.Candidate, error) {
	return f.record(actor)
}
func (f *integrationHandlerFake) Patch(_ context.Context, actor integration.Actor, _ protocol.IntegrationShowParams) (protocol.IntegrationPatchResult, error) {
	f.actor = actor
	f.calls++
	if f.err != nil {
		return protocol.IntegrationPatchResult{}, f.err
	}
	return protocol.IntegrationPatchResult{Patch: "diff --git a/file b/file\n", Truncated: false}, nil
}
func (f *integrationHandlerFake) List(_ context.Context, actor integration.Actor, _ protocol.IntegrationListParams) (protocol.IntegrationListResult, error) {
	f.actor = actor
	f.calls++
	if f.err != nil {
		return protocol.IntegrationListResult{}, f.err
	}
	return protocol.IntegrationListResult{Candidates: []protocol.CandidateSummary{{
		CandidateID:            f.candidate.CandidateID,
		WorkspaceID:            f.candidate.WorkspaceID,
		State:                  f.candidate.State,
		CandidateRevision:      f.candidate.CandidateRevision,
		TargetRef:              f.candidate.TargetRef,
		ExpectedTargetRevision: f.candidate.ExpectedTargetRevision,
		CreatedAt:              f.candidate.CreatedAt,
		ExpiresAt:              f.candidate.ExpiresAt,
	}}}, nil
}
func (f *integrationHandlerFake) Resolve(_ context.Context, actor integration.Actor, _ protocol.IntegrationResolveParams) (protocol.Candidate, error) {
	return f.record(actor)
}
func (f *integrationHandlerFake) Verify(_ context.Context, actor integration.Actor, _ protocol.IntegrationVerifyParams) (protocol.Candidate, error) {
	return f.record(actor)
}
func (f *integrationHandlerFake) RequestDelivery(_ context.Context, actor integration.Actor, _ protocol.IntegrationRequestDeliveryParams) (protocol.Candidate, error) {
	return f.record(actor)
}
func (f *integrationHandlerFake) Decide(_ context.Context, actor integration.Actor, _ protocol.IntegrationDecideParams) (protocol.Candidate, error) {
	return f.record(actor)
}
func (f *integrationHandlerFake) Deliver(_ context.Context, actor integration.Actor, _ protocol.IntegrationDeliverParams) (protocol.Candidate, error) {
	return f.record(actor)
}
func (f *integrationHandlerFake) Delete(_ context.Context, actor integration.Actor, _ protocol.IntegrationDeleteParams) error {
	f.actor = actor
	f.calls++
	return f.err
}

func integrationTestCandidate(ws domain.WorkspaceID) protocol.Candidate {
	return protocol.Candidate{
		CandidateID: "candidate-1",
		WorkspaceID: string(ws),
		State:       protocol.CandidateFrozen,
	}
}

func TestIntegrationHandlersUseAuthenticatedActorAndCandidateEnvelope(t *testing.T) {
	fake := &integrationHandlerFake{}
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.Integration = fake })
	fake.candidate = integrationTestCandidate(e.ws.ID)

	params := protocol.IntegrationShowParams{WorkspaceID: string(e.ws.ID), CandidateID: fake.candidate.CandidateID}
	var result protocol.IntegrationShowResult
	if err := handlerCallJSON(t, e, e.member.ID, protocol.MethodIntegrationShow, params, &result); err != nil {
		t.Fatalf("integration.show: %v", err)
	}
	if result.Candidate.CandidateID != fake.candidate.CandidateID {
		t.Fatalf("candidate envelope = %+v", result.Candidate)
	}
	if fake.calls != 1 || fake.actor.MemberID != e.member.ID || fake.actor.RunID != "" {
		t.Fatalf("service actor = %+v, calls=%d; actor must come from authenticated member only", fake.actor, fake.calls)
	}

	// An actor-like field in params is not authority. A viewer forging the
	// admin's identity is denied by the current permission guard and never
	// reaches the service.
	_, viewer := addMember(t, e, "Viewer", domain.RoleViewer, false)
	var patchResult protocol.IntegrationPatchResult
	if err := handlerCallJSON(t, e, viewer.ID, protocol.MethodIntegrationPatch, params, &patchResult); err != nil {
		t.Fatalf("integration.patch for viewer: %v", err)
	}
	if patchResult.Patch == "" || fake.actor.MemberID != viewer.ID || fake.actor.RunID != "" {
		t.Fatalf("patch actor/result = %+v, actor=%+v", patchResult, fake.actor)
	}
	raw, err := json.Marshal(map[string]any{
		"workspace_id":             string(e.ws.ID),
		"target_ref":               "refs/heads/main",
		"expected_target_revision": "base",
		"idempotency_key":          "forged",
		"actor":                    map[string]string{"member_id": string(e.member.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var perr *protocol.Error
	if _, err := e.srv.Local(viewer.ID).Call(context.Background(), protocol.MethodIntegrationPrepare, raw); !errors.As(err, &perr) || perr.Code != protocol.CodeDenied {
		t.Fatalf("forged viewer actor call = %v, want CodeDenied", err)
	}
	if fake.calls != 2 {
		t.Fatalf("forged actor reached integration service: calls=%d", fake.calls)
	}
}

func TestIntegrationHandlersRejectRevokedMemberAndMapServiceErrors(t *testing.T) {
	fake := &integrationHandlerFake{}
	e := newTestEnv(t, func(cfg *Config) { cfg.Services.Integration = fake })
	fake.candidate = integrationTestCandidate(e.ws.ID)

	_, revoked := addMember(t, e, "Revoked", domain.RoleCollaborator, false)
	if err := e.store.DeleteMember(context.Background(), revoked.ID); err != nil {
		t.Fatalf("delete member: %v", err)
	}
	var perr *protocol.Error
	if _, err := e.srv.Local(revoked.ID).Call(context.Background(), protocol.MethodIntegrationShow, mustJSON(t, protocol.IntegrationShowParams{WorkspaceID: string(e.ws.ID), CandidateID: fake.candidate.CandidateID})); !errors.As(err, &perr) || perr.Code != protocol.CodeDenied {
		t.Fatalf("revoked member call = %v, want CodeDenied", err)
	}
	if fake.calls != 0 {
		t.Fatalf("revoked member reached integration service: calls=%d", fake.calls)
	}

	fake.err = fmt.Errorf("service wrapper: %w", integration.ErrConflict)
	if _, err := e.srv.Local(e.member.ID).Call(context.Background(), protocol.MethodIntegrationShow, mustJSON(t, protocol.IntegrationShowParams{WorkspaceID: string(e.ws.ID), CandidateID: fake.candidate.CandidateID})); !errors.As(err, &perr) || perr.Code != protocol.CodeConflict || perr.Message != fake.err.Error() {
		t.Fatalf("wrapped conflict = %v, want CodeConflict with useful message", err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
