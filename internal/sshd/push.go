package sshd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/push"
	"github.com/3xDevOps/Aether/internal/store"
)

// PushService is the seam for Web Push notifications. Satisfied by
// *push.Service.
type PushService interface {
	// PublicKey is the VAPID key a browser subscribes with.
	PublicKey() string
	Subscribed(ctx context.Context, member domain.MemberID, endpoint string) (bool, error)
	Subscribe(ctx context.Context, member domain.MemberID, endpoint, p256dh, auth string) error
	Unsubscribe(ctx context.Context, member domain.MemberID, endpoint string) error
	// Test sends one notification now and returns the push service's error.
	Test(ctx context.Context, member domain.MemberID, endpoint string) error
	// Active records that member is using a dashboard.
	Active(member domain.MemberID)
}

func init() {
	registerMethod(protocol.MethodPushStatus, (*Server).pushStatus)
	registerMethod(protocol.MethodPushSubscribe, (*Server).pushSubscribe)
	registerMethod(protocol.MethodPushUnsubscribe, (*Server).pushUnsubscribe)
	registerMethod(protocol.MethodPushTest, (*Server).pushTest)
	registerMethod(protocol.MethodPushActive, (*Server).pushActive)
}

func (s *Server) push() (PushService, *protocol.Error) {
	if s.cfg.Services.Push == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "push service not configured"}
	}
	return s.cfg.Services.Push, nil
}

func pushError(err error) *protocol.Error {
	switch {
	case errors.Is(err, push.ErrInvalid):
		return invalidParams(err.Error())
	case errors.Is(err, store.ErrLimit):
		return &protocol.Error{Code: protocol.CodeInvalidState, Message: err.Error()}
	case errors.Is(err, push.ErrDelivery):
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error()}
	}
	return rpcError(err)
}

func (s *Server) pushStatus(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.push()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.PushStatusParams](params)
	if perr != nil {
		return nil, perr
	}
	out := protocol.PushStatusResult{PublicKey: svc.PublicKey()}
	if p.Endpoint != "" {
		subscribed, err := svc.Subscribed(ctx, member, p.Endpoint)
		if err != nil {
			return nil, pushError(err)
		}
		out.Subscribed = subscribed
	}
	return out, nil
}

func (s *Server) pushSubscribe(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.push()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.PushSubscribeParams](params)
	if perr != nil {
		return nil, perr
	}
	if err := svc.Subscribe(ctx, member, p.Endpoint, p.Keys.P256DH, p.Keys.Auth); err != nil {
		return nil, pushError(err)
	}
	return struct{}{}, nil
}

func (s *Server) pushUnsubscribe(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return s.pushEndpoint(ctx, member, params, PushService.Unsubscribe)
}

func (s *Server) pushTest(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	return s.pushEndpoint(ctx, member, params, PushService.Test)
}

func (s *Server) pushEndpoint(ctx context.Context, member domain.MemberID, params json.RawMessage,
	act func(PushService, context.Context, domain.MemberID, string) error) (any, *protocol.Error) {
	svc, perr := s.push()
	if perr != nil {
		return nil, perr
	}
	p, perr := decodeParams[protocol.PushEndpointParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.Endpoint == "" {
		return nil, invalidParams("endpoint is required")
	}
	if err := act(svc, ctx, member, p.Endpoint); err != nil {
		return nil, pushError(err)
	}
	return struct{}{}, nil
}

func (s *Server) pushActive(_ context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	svc, perr := s.push()
	if perr != nil {
		return nil, perr
	}
	svc.Active(member)
	return struct{}{}, nil
}
