package sshd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	integration "github.com/3xDevOps/Aether/internal/integration"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// IntegrationService is the SSH/control-channel seam for the candidate
// service. The authenticated member is supplied out-of-band by dispatch and
// is the only source of Actor.MemberID. The wire params intentionally contain
// no actor, generation, or authority fields.
type IntegrationService interface {
	Prepare(context.Context, integration.Actor, protocol.IntegrationPrepareParams) (protocol.Candidate, error)
	Show(context.Context, integration.Actor, protocol.IntegrationShowParams) (protocol.Candidate, error)
	List(context.Context, integration.Actor, protocol.IntegrationListParams) (protocol.IntegrationListResult, error)
	Patch(context.Context, integration.Actor, protocol.IntegrationShowParams) (protocol.IntegrationPatchResult, error)
	Resolve(context.Context, integration.Actor, protocol.IntegrationResolveParams) (protocol.Candidate, error)
	Verify(context.Context, integration.Actor, protocol.IntegrationVerifyParams) (protocol.Candidate, error)
	RequestDelivery(context.Context, integration.Actor, protocol.IntegrationRequestDeliveryParams) (protocol.Candidate, error)
	Decide(context.Context, integration.Actor, protocol.IntegrationDecideParams) (protocol.Candidate, error)
	Deliver(context.Context, integration.Actor, protocol.IntegrationDeliverParams) (protocol.Candidate, error)
	Delete(context.Context, integration.Actor, protocol.IntegrationDeleteParams) error
}

func init() {
	registerGuarded(protocol.MethodIntegrationPrepare, permissions.Push, workspaceTarget, (*Server).integrationPrepare)
	registerGuarded(protocol.MethodIntegrationShow, permissions.View, workspaceTarget, (*Server).integrationShow)
	registerGuarded(protocol.MethodIntegrationList, permissions.View, workspaceTarget, (*Server).integrationList)
	registerGuarded(protocol.MethodIntegrationPatch, permissions.View, workspaceTarget, (*Server).integrationPatch)
	registerGuarded(protocol.MethodIntegrationResolve, permissions.Push, workspaceTarget, (*Server).integrationResolve)
	registerGuarded(protocol.MethodIntegrationVerify, permissions.Push, workspaceTarget, (*Server).integrationVerify)
	registerGuarded(protocol.MethodIntegrationRequestDelivery, permissions.Push, workspaceTarget, (*Server).integrationRequestDelivery)
	registerGuarded(protocol.MethodIntegrationDecide, permissions.Push, workspaceTarget, (*Server).integrationDecide)
	registerGuarded(protocol.MethodIntegrationDeliver, permissions.Push, workspaceTarget, (*Server).integrationDeliver)
	registerGuarded(protocol.MethodIntegrationDelete, permissions.Push, workspaceTarget, (*Server).integrationDelete)
}

func (s *Server) integrationService() (IntegrationService, *protocol.Error) {
	if s.cfg.Services.Integration == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "integration service not configured"}
	}
	return s.cfg.Services.Integration, nil
}

func integrationActor(member domain.MemberID) integration.Actor {
	return integration.Actor{MemberID: member}
}

func integrationServiceError(err error) *protocol.Error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, integration.ErrInvalidRequest):
		return invalidParams(err.Error())
	case errors.Is(err, integration.ErrUnauthorized):
		return &protocol.Error{Code: protocol.CodeDenied, Message: err.Error()}
	case errors.Is(err, integration.ErrExpired):
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	case errors.Is(err, integration.ErrConflict):
		return &protocol.Error{Code: protocol.CodeConflict, Message: err.Error()}
	case errors.Is(err, integration.ErrUnavailable):
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
	default:
		return rpcError(err)
	}
}

func (s *Server) integrationPrepare(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationPrepareParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.TargetRef == "" || p.ExpectedTargetRevision == "" || p.IdempotencyKey == "" {
		return nil, invalidParams("target_ref, expected_target_revision, and idempotency_key are required")
	}
	candidate, err := svc.Prepare(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return protocol.IntegrationPrepareResult{Candidate: candidate}, nil
}

func (s *Server) integrationShow(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationShowParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.CandidateID == "" {
		return nil, invalidParams("workspace_id and candidate_id are required")
	}
	candidate, err := svc.Show(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return protocol.IntegrationShowResult{Candidate: candidate}, nil
}

func (s *Server) integrationList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationListParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" {
		return nil, invalidParams("workspace_id is required")
	}
	result, err := svc.List(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return result, nil
}

func (s *Server) integrationPatch(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationShowParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.CandidateID == "" {
		return nil, invalidParams("workspace_id and candidate_id are required")
	}
	patch, err := svc.Patch(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return patch, nil
}

func (s *Server) integrationResolve(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationResolveParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.CandidateID == "" || p.IdempotencyKey == "" {
		return nil, invalidParams("workspace_id, candidate_id, and idempotency_key are required")
	}
	candidate, err := svc.Resolve(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return protocol.IntegrationResolveResult{Candidate: candidate}, nil
}

func (s *Server) integrationVerify(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationVerifyParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.CandidateID == "" || p.CandidateRevision == "" || p.IdempotencyKey == "" {
		return nil, invalidParams("workspace_id, candidate_id, candidate_revision, and idempotency_key are required")
	}
	candidate, err := svc.Verify(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return protocol.IntegrationVerifyResult{Candidate: candidate}, nil
}

func (s *Server) integrationRequestDelivery(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationRequestDeliveryParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.CandidateID == "" || p.CandidateRevision == "" || p.IdempotencyKey == "" {
		return nil, invalidParams("workspace_id, candidate_id, candidate_revision, and idempotency_key are required")
	}
	candidate, err := svc.RequestDelivery(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return protocol.IntegrationRequestDeliveryResult{Candidate: candidate}, nil
}

func (s *Server) integrationDecide(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationDecideParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.CandidateID == "" || p.RequestID == "" {
		return nil, invalidParams("workspace_id, candidate_id, and request_id are required")
	}
	candidate, err := svc.Decide(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return protocol.IntegrationDecideResult{Candidate: candidate}, nil
}

func (s *Server) integrationDeliver(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationDeliverParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.CandidateID == "" || p.RequestID == "" {
		return nil, invalidParams("workspace_id, candidate_id, and request_id are required")
	}
	candidate, err := svc.Deliver(ctx, integrationActor(member), p)
	if err != nil {
		return nil, integrationServiceError(err)
	}
	return protocol.IntegrationDeliverResult{Candidate: candidate}, nil
}

func (s *Server) integrationDelete(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.integrationService()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.IntegrationDeleteParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.WorkspaceID == "" || p.CandidateID == "" {
		return nil, invalidParams("workspace_id and candidate_id are required")
	}
	if err := svc.Delete(ctx, integrationActor(member), p); err != nil {
		return nil, integrationServiceError(err)
	}
	return protocol.IntegrationDeleteResult{}, nil
}
