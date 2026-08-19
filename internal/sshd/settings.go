package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodSessionSettings, permissions.SessionAdmin, nil, (*Server).sessionSettings)
	registerGuarded(protocol.MethodRunProtect, permissions.Protect, runTarget, (*Server).runProtect)
}

// sessionSettings updates a session's settings (admin only; the guard has
// already checked SessionAdmin). The change is stamped into the session
// timeline attributed to the caller.
func (s *Server) sessionSettings(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.SessionSettingsParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.SessionID == "" {
		return nil, invalidParams("session_id is required")
	}
	if !domain.ValidSteerOthers(p.SteerOthers) {
		return nil, invalidParams(`steer_others must be "" or "admins_only"`)
	}
	id := domain.SessionID(p.SessionID)
	if err := s.cfg.Store.SetSessionSteerOthers(ctx, id, p.SteerOthers); err != nil {
		return nil, rpcError(err)
	}
	sess, err := s.cfg.Store.GetSession(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	setting := p.SteerOthers
	if setting == "" {
		setting = "default"
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		SessionID: sess.ID,
		ActorID:   member,
		Payload: events.TimelinePayload{
			Kind:    events.TimelineNote,
			Message: "session settings: steer_others set to " + setting,
		},
	})
	return protocol.SessionSettingsResult{Session: protocol.SessionFromDomain(sess)}, nil
}

// runProtect toggles a run's protected flag (owner or admin; the guard has
// already checked Protect against the run). The change is stamped into the
// session timeline attributed to the caller.
func (s *Server) runProtect(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunProtectParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.RunID == "" {
		return nil, invalidParams("run_id is required")
	}
	id := domain.RunID(p.RunID)
	if err := s.cfg.Store.SetRunProtected(ctx, id, p.Protected); err != nil {
		return nil, rpcError(err)
	}
	run, err := s.cfg.Store.GetRun(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	msg := "run protection disabled"
	if run.Protected {
		msg = "run protection enabled"
	}
	_, _ = s.cfg.Bus.Publish(ctx, events.Event{
		SessionID: run.SessionID,
		RunID:     run.ID,
		ActorID:   member,
		Payload:   events.TimelinePayload{Kind: events.TimelineNote, Message: msg},
	})
	return protocol.RunResult{Run: protocol.RunFromDomain(run)}, nil
}
