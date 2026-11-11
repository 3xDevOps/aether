package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/timeline"
)

func init() {
	registerGuarded(protocol.MethodWorkspaceTimeline, permissions.View, workspaceTarget, (*Server).workspaceTimeline)
}

func (s *Server) workspaceTimeline(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	reader := s.cfg.Services.Timeline
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "workspace.timeline: workspace history is not available"}
	}
	p, perr := decodeParams[protocol.WorkspaceTimelineParams](params)
	if perr != nil {
		return nil, perr
	}
	types := make([]events.Type, 0, len(p.Types))
	for _, t := range p.Types {
		types = append(types, events.Type(t))
	}
	if p.Newest && p.AfterSeq > 0 {
		return nil, invalidParams("after_seq and newest cannot be combined")
	}
	if !p.Newest && p.BeforeSeq > 0 {
		return nil, invalidParams("before_seq needs newest")
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	filter := timeline.Filter{
		Workspace: domain.WorkspaceID(p.WorkspaceID),
		Run:       domain.RunID(p.RunID),
		Member:    domain.MemberID(p.MemberID),
		Types:     types,
		Scope:     s.eventScope(ctx, actor),
	}
	var page timeline.Page
	if p.Newest {
		page, err = reader.Before(ctx, filter, p.BeforeSeq, p.Limit)
	} else {
		page, err = reader.Page(ctx, filter, p.AfterSeq, p.Limit)
	}
	if err != nil {
		return nil, rpcError(err)
	}
	out := protocol.WorkspaceTimelineResult{
		Events:   make([]protocol.Event, 0, len(page.Events)),
		NextSeq:  page.NextSeq,
		More:     page.More,
		OlderSeq: page.OlderSeq,
	}
	for _, ev := range page.Events {
		payload, merr := json.Marshal(ev.Payload)
		if merr != nil {
			return nil, rpcError(fmt.Errorf("sshd: encode %s payload of event %s: %w", ev.Type, ev.ID, merr))
		}
		out.Events = append(out.Events, protocol.Event{
			ID:          ev.ID,
			Seq:         ev.Seq,
			Time:        ev.Time.UTC().Format(time.RFC3339Nano),
			WorkspaceID: string(ev.WorkspaceID),
			RunID:       string(ev.RunID),
			ActorID:     string(ev.ActorID),
			Type:        string(ev.Type),
			Payload:     payload,
		})
	}
	return out, nil
}
