package sshd

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func init() {
	registerGuarded(protocol.MethodCoordMessagesList, permissions.View, coordMessagesTarget, (*Server).coordMessagesList)
}

func coordMessagesTarget(s *Server, ctx context.Context, raw json.RawMessage) (permissions.Target, *protocol.Error) {
	var p protocol.CoordMessagesListParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return permissions.Target{}, invalidParams("invalid params: " + err.Error())
	}
	if p.WorkspaceID == "" {
		return permissions.Target{}, invalidParams("workspace_id is required")
	}
	if _, err := s.cfg.Store.GetWorkspace(ctx, domain.WorkspaceID(p.WorkspaceID)); err != nil {
		return permissions.Target{}, rpcError(err)
	}
	return permissions.Target{Workspace: domain.WorkspaceID(p.WorkspaceID)}, nil
}

func (s *Server) coordMessagesList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.CoordMessagesListParams](raw)
	if perr != nil {
		return nil, perr
	}
	if perr := validateCollaborationPage(p.Before, p.Limit); perr != nil {
		return nil, perr
	}
	page, err := s.cfg.Store.ListRunMessages(ctx, store.RunMessageFilter{
		WorkspaceID:   domain.WorkspaceID(p.WorkspaceID),
		RunID:         domain.RunID(p.RunID),
		CorrelationID: p.CorrelationID,
		Before:        p.Before,
		Limit:         p.Limit,
	})
	if err != nil {
		return nil, rpcError(err)
	}
	if err := s.redactWaitWakes(ctx, member, page.Items); err != nil {
		return nil, rpcError(err)
	}
	return protocol.CoordMessagesPageFromStore(page), nil
}

// redactWaitWakes cuts a wait's wake, a session's message to itself, to
// its first line for members who do not read that session's wait details:
// the rest holds what the owner's GitHub login or another agent returned.
func (s *Server) redactWaitWakes(ctx context.Context, member domain.MemberID, items []*store.ListedRunMessage) error {
	sees := map[domain.RunID]bool{}
	for _, m := range items {
		if m.FromRun != m.ToRun || !strings.HasPrefix(m.CorrelationID, "wat-") {
			continue
		}
		full, ok := sees[m.ToRun]
		if !ok {
			run, err := s.cfg.Store.GetRun(ctx, m.ToRun)
			if err != nil {
				return err
			}
			full = s.seesWaitDetails(ctx, member, run)
			sees[m.ToRun] = full
		}
		if !full {
			m.Body, _, _ = strings.Cut(m.Body, "\n")
		}
	}
	return nil
}
