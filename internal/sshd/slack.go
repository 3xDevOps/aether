package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// SlackService is the seam for the server's Slack bot. Satisfied by
// *slack.Service.
type SlackService interface {
	Status(ctx context.Context, member domain.MemberID) (protocol.SlackStatusResult, error)
	// Code mints a link code for grant, replacing any of its member's
	// not yet spent.
	Code(grant ConnectorGrant) (protocol.SlackLinkResult, error)
	// Unlink removes member's link and returns it, or returns
	// store.ErrNotFound.
	Unlink(ctx context.Context, member domain.MemberID) (protocol.SlackLink, error)
}

func init() {
	registerMethod(protocol.MethodSlackStatus, (*Server).slackStatus)
	registerMethod(protocol.MethodSlackLink, (*Server).slackLink)
	registerMethod(protocol.MethodSlackUnlink, (*Server).slackUnlink)
}

func (s *Server) slack(method string) (SlackService, *protocol.Error) {
	if s.cfg.Services.Slack == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method +
			": Slack is not set up on this server; an admin sets it up with: sudo aether-server slack setup (docs/slack.md)"}
	}
	return s.cfg.Services.Slack, nil
}

func (s *Server) slackStatus(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	if s.cfg.Services.Slack == nil {
		return protocol.SlackStatusResult{}, nil
	}
	out, err := s.cfg.Services.Slack.Status(ctx, member)
	if err != nil {
		return nil, rpcError(err)
	}
	return out, nil
}

func (s *Server) slackLink(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodSlackLink
	if perr := refuseEdgeSignIn(ctx, method); perr != nil {
		return nil, perr
	}
	if perr := s.requireApprovedCaller(ctx, method, false); perr != nil {
		return nil, perr
	}
	svc, perr := s.slack(method)
	if perr != nil {
		return nil, perr
	}
	id, _ := ctx.Value(connIdentityKey{}).(connIdentity)
	out, err := svc.Code(ConnectorGrant{Member: member, Device: id.device})
	if err != nil {
		return nil, rpcError(err)
	}
	slog.Info("sshd: slack link code minted", "member", member, "device", id.device)
	return out, nil
}

func (s *Server) slackUnlink(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodSlackUnlink
	svc, perr := s.slack(method)
	if perr != nil {
		return nil, perr
	}
	link, err := svc.Unlink(ctx, member)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf(
			"%s: you have no Slack account linked; aether slack link links one", method)}
	}
	if err != nil {
		return nil, rpcError(err)
	}
	slog.Info("sshd: slack unlinked", "member", member, "slack_user", link.UserID)
	return protocol.SlackUnlinkResult{Link: link}, nil
}
