package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/timeline"
)

func init() {
	registerMethod(protocol.MethodSessionTimeline, (*Server).sessionTimeline)
}

func (s *Server) sessionTimeline(ctx context.Context, _ domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	reader := s.cfg.Services.Timeline
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "session.timeline: session history is not available"}
	}
	p, perr := decodeParams[protocol.SessionTimelineParams](params)
	if perr != nil {
		return nil, perr
	}
	if p.SessionID == "" {
		return nil, invalidParams("session_id is required")
	}
	if _, err := s.cfg.Store.GetSession(ctx, domain.SessionID(p.SessionID)); err != nil {
		return nil, rpcError(err)
	}
	types := make([]events.Type, 0, len(p.Types))
	for _, t := range p.Types {
		types = append(types, events.Type(t))
	}
	page, err := reader.Page(ctx, timeline.Filter{
		Session: domain.SessionID(p.SessionID),
		Run:     domain.RunID(p.RunID),
		Member:  domain.MemberID(p.MemberID),
		Types:   types,
	}, p.AfterSeq, p.Limit)
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.SessionTimelineResult{
		Events:  make([]protocol.Event, 0, len(page.Events)),
		NextSeq: page.NextSeq,
		More:    page.More,
	}
	for _, ev := range page.Events {
		payload, merr := json.Marshal(ev.Payload)
		if merr != nil {
			return nil, rpcError(fmt.Errorf("sshd: encode %s payload of event %s: %w", ev.Type, ev.ID, merr))
		}
		out.Events = append(out.Events, protocol.Event{
			ID:        ev.ID,
			Seq:       ev.Seq,
			Time:      ev.Time.UTC().Format(time.RFC3339Nano),
			SessionID: string(ev.SessionID),
			RunID:     string(ev.RunID),
			ActorID:   string(ev.ActorID),
			Type:      string(ev.Type),
			Payload:   payload,
		})
	}
	return out, nil
}
