package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/timeline"
)

const (
	changesDefaultLimit = 20
	changesMaxLimit     = 100
	// changesUntrustedRunes clips agent-written text so one change stays
	// small enough for a model's context.
	changesUntrustedRunes = 300
)

// changeKinds maps each kind to the event types SQL selects it by, and the
// workspace.timeline entry kinds that belong to it.
var changeKinds = map[string]struct {
	types    []events.Type
	timeline []events.TimelineKind
}{
	protocol.ChangeKindStatus:    {types: []events.Type{events.TypeRunStatus, events.TypeRunInput}},
	protocol.ChangeKindReports:   {types: []events.Type{events.TypeTimeline}, timeline: []events.TimelineKind{events.TimelineReport}},
	protocol.ChangeKindMessages:  {types: []events.Type{events.TypeCoordMessage}},
	protocol.ChangeKindPR:        {types: []events.Type{events.TypeRunPR}},
	protocol.ChangeKindLifecycle: {types: []events.Type{events.TypeRunArchived, events.TypeRunDeleted, events.TypeWorkspaceDeleted, events.TypeTimeline}, timeline: []events.TimelineKind{events.TimelineHandoff}},
}

var changeKindOrder = []string{
	protocol.ChangeKindStatus, protocol.ChangeKindReports, protocol.ChangeKindMessages,
	protocol.ChangeKindPR, protocol.ChangeKindLifecycle,
}

func init() {
	registerGuarded(protocol.MethodChangesList, permissions.View, changesTarget, (*Server).changesList)
}

// changesTarget checks the workspace a changes.list request is scoped to,
// so a per-workspace boundary applies to it; a member-wide request targets
// no workspace.
func changesTarget(s *Server, ctx context.Context, raw json.RawMessage) (permissions.Target, *protocol.Error) {
	p, perr := decodeParams[protocol.ChangesListParams](raw)
	if perr != nil {
		return permissions.Target{}, perr
	}
	ws, live, perr := s.changesScope(ctx, p)
	if perr != nil || !live {
		return permissions.Target{}, perr
	}
	return permissions.Target{Workspace: ws}, nil
}

// changesScope resolves the one workspace the named workspace and run
// share, and whether it still exists. A workspace or run deleted since the caller's cursor was issued is no error, so the feed still
// delivers its deletion.
func (s *Server) changesScope(ctx context.Context, p protocol.ChangesListParams) (domain.WorkspaceID, bool, *protocol.Error) {
	gone := func(err error) bool { return p.Cursor != "" && errors.Is(err, store.ErrNotFound) }
	var ws domain.WorkspaceID
	if p.RunID != "" {
		run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
		switch {
		case gone(err):
		case err != nil:
			return "", false, rpcError(err)
		default:
			ws = run.WorkspaceID
		}
	}
	if p.WorkspaceID != "" {
		id := domain.WorkspaceID(p.WorkspaceID)
		if ws != "" && ws != id {
			return "", false, invalidParams("workspace_id does not contain the named session")
		}
		if _, err := s.cfg.Store.GetWorkspace(ctx, id); gone(err) {
			deleted, derr := s.workspaceDeleted(ctx, id)
			if derr != nil {
				return "", false, rpcError(derr)
			}
			if !deleted {
				return "", false, rpcError(err)
			}
			return id, false, nil
		} else if err != nil {
			return "", false, rpcError(err)
		}
		ws = id
	}
	return ws, ws != "", nil
}

// workspaceDeleted reports whether the log holds a deletion notice for id,
// which tells a deleted workspace from one that never existed.
func (s *Server) workspaceDeleted(ctx context.Context, id domain.WorkspaceID) (bool, error) {
	reader := s.cfg.Services.Timeline
	if reader == nil {
		return false, nil
	}
	page, err := reader.Page(ctx, timeline.Filter{Workspace: id, Types: []events.Type{events.TypeWorkspaceDeleted}}, 0, 1)
	if err != nil {
		return false, fmt.Errorf("changes.list: look up the deletion of workspace %s: %w", id, err)
	}
	return len(page.Events) > 0, nil
}

func (s *Server) changesList(ctx context.Context, _ domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	reader := s.cfg.Services.Timeline
	if reader == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "changes.list: workspace history is not available"}
	}
	p, perr := decodeParams[protocol.ChangesListParams](raw)
	if perr != nil {
		return nil, perr
	}
	ws, live, perr := s.changesScope(ctx, p)
	if perr != nil {
		return nil, perr
	}
	filter := timeline.Filter{
		Workspace: ws,
		Run:       domain.RunID(p.RunID),
		NoRereads: true,
	}
	if ws != "" && !live {
		// A deleted workspace's log holds only its workspace.deleted, which
		// carries no run: the run filter would hide it.
		filter.Run = ""
	}
	kinds := p.Kinds
	if len(kinds) == 0 {
		kinds = changeKindOrder
	}
	requested := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		requested[k] = true
		spec, ok := changeKinds[k]
		if !ok {
			return nil, invalidParams(fmt.Sprintf("unknown kind %q; kinds are %s", k, strings.Join(changeKindOrder, ", ")))
		}
		for _, t := range spec.types {
			if !slices.Contains(filter.Types, t) {
				filter.Types = append(filter.Types, t)
			}
		}
		filter.TimelineKinds = append(filter.TimelineKinds, spec.timeline...)
	}
	limit := p.Limit
	switch {
	case limit < 0:
		return nil, invalidParams("limit must not be negative")
	case limit == 0:
		limit = changesDefaultLimit
	case limit > changesMaxLimit:
		limit = changesMaxLimit
	}

	var page timeline.Page
	if p.Cursor == "" {
		var err error
		if page, err = reader.Before(ctx, filter, 0, limit); err != nil {
			return nil, rpcError(err)
		}
		page.More = false
	} else {
		cursor, err := strconv.ParseUint(p.Cursor, 10, 64)
		if err != nil {
			return nil, invalidParams("cursor must be the next_cursor of an earlier changes.list call")
		}
		if page, err = reader.Page(ctx, filter, cursor, limit); err != nil {
			return nil, rpcError(err)
		}
		if cursor > page.Head {
			data, _ := json.Marshal(protocol.ChangesCursorAheadData{Reason: protocol.ChangesCursorAhead, Head: strconv.FormatUint(page.Head, 10)})
			return nil, &protocol.Error{
				Code:    protocol.CodeUnavailable,
				Message: fmt.Sprintf("changes.list: cursor %d is past the server's event log head %d; its history was reset or restored, so call again without a cursor", cursor, page.Head),
				Data:    data,
			}
		}
	}

	out := protocol.ChangesListResult{
		Changes:    make([]protocol.Change, 0, len(page.Events)),
		NextCursor: strconv.FormatUint(page.NextSeq, 10),
		More:       page.More,
		Head:       strconv.FormatUint(page.Head, 10),
	}
	for _, ev := range page.Events {
		if c, ok := changeOf(ev); ok && requested[c.Kind] {
			out.Changes = append(out.Changes, c)
		}
	}
	return out, nil
}

// changeOf projects one event onto its compact change; ok is false for an
// event no kind keeps.
func changeOf(ev events.Event) (protocol.Change, bool) {
	c := protocol.Change{
		Seq:         ev.Seq,
		Time:        ev.Time.UTC().Format(time.RFC3339Nano),
		Type:        string(ev.Type),
		WorkspaceID: string(ev.WorkspaceID),
		RunID:       string(ev.RunID),
		ActorID:     string(ev.ActorID),
	}
	switch v := ev.Payload.(type) {
	case events.RunStatusPayload:
		c.Kind = protocol.ChangeKindStatus
		c.Status = &protocol.ChangeStatus{From: string(v.From), To: string(v.To), State: string(v.Activity), Cause: string(v.Cause)}
		if v.Wait != nil {
			c.Status.Wait = string(v.Wait.Kind)
		}
		if v.Reason != "" {
			c.Untrusted = &protocol.ChangeUntrusted{Reason: clipUntrusted(v.Reason)}
		}
	case events.RunInputPayload:
		c.Kind = protocol.ChangeKindStatus
		c.Input = &protocol.ChangeInput{Pending: len(v.PendingInputs)}
	case events.TimelinePayload:
		switch v.Kind {
		case events.TimelineReport:
			c.Kind = protocol.ChangeKindReports
			c.Report = &protocol.ChangeReport{Outcome: v.Outcome, WaitingFor: v.WaitingFor}
			if v.ReportID != "" || v.Summary != "" {
				c.Untrusted = &protocol.ChangeUntrusted{ReportID: clipUntrusted(v.ReportID), Summary: clipUntrusted(v.Summary)}
			}
		case events.TimelineHandoff:
			c.Kind = protocol.ChangeKindLifecycle
			c.Lifecycle = &protocol.ChangeLifecycle{Action: "handoff", ToMemberID: v.Message}
		default:
			return protocol.Change{}, false
		}
	case events.CoordMessagePayload:
		c.Kind = protocol.ChangeKindMessages
		c.Message = &protocol.ChangeMessage{MessageID: v.MessageID, FromRunID: string(v.FromRunID), ToRunID: string(v.ToRunID), Kind: v.Kind, CorrelationID: v.CorrelationID}
	case events.RunPRPayload:
		c.Kind = protocol.ChangeKindPR
		c.PR = &protocol.ChangePR{Number: v.Number, State: v.State, URL: v.URL, Merged: v.MergedAt != nil}
	case events.RunArchivedPayload:
		c.Kind = protocol.ChangeKindLifecycle
		c.Lifecycle = &protocol.ChangeLifecycle{Action: "archived"}
		if v.ArchivedAt == nil {
			c.Lifecycle.Action = "restored"
		}
	case events.RunDeletedPayload:
		c.Kind = protocol.ChangeKindLifecycle
		c.Lifecycle = &protocol.ChangeLifecycle{Action: "deleted"}
	case events.WorkspaceDeletedPayload:
		c.Kind = protocol.ChangeKindLifecycle
		c.Lifecycle = &protocol.ChangeLifecycle{Action: "workspace-deleted"}
	default:
		return protocol.Change{}, false
	}
	return c, true
}

func clipUntrusted(s string) string {
	if r := []rune(s); len(r) > changesUntrustedRunes {
		return string(r[:changesUntrustedRunes]) + "…"
	}
	return s
}
