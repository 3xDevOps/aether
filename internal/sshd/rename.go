package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const maxDisplayNameRunes = 64

func init() {
	registerMethod(protocol.MethodMemberRename, (*Server).memberRename)
}

func (s *Server) memberRename(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.MemberRenameParams](params)
	if perr != nil {
		return nil, perr
	}
	name, err := cleanDisplayName(p.DisplayName)
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	target := member
	if p.MemberID != "" && p.MemberID != string(member) {
		target = domain.MemberID(p.MemberID)
		if aerr := s.requireAdmin(ctx, member, protocol.MethodMemberRename); aerr != nil {
			return nil, aerr
		}
	}
	m, gerr := s.cfg.Store.GetMember(ctx, target)
	if gerr != nil {
		return nil, rpcError(gerr)
	}
	if m.DisplayName == name {
		return protocol.MemberRenameResult{Member: protocol.MemberFromDomain(m)}, nil
	}
	m.DisplayName = name
	if uerr := s.cfg.Store.UpdateMember(ctx, m); uerr != nil {
		return nil, rpcError(uerr)
	}
	// A member without a git name signs commits with the display name.
	s.cfg.Runs.RefreshMemberCoAuthors(ctx, target)
	if rerr := s.refreshHomeGitIdentity(ctx, m); rerr != nil {
		return nil, rpcError(rerr)
	}
	s.publishMemberChanged(ctx, member, m)
	return protocol.MemberRenameResult{Member: protocol.MemberFromDomain(m)}, nil
}

func cleanDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", fmt.Errorf("display_name is required")
	case utf8.RuneCountInString(name) > maxDisplayNameRunes:
		return "", fmt.Errorf("display_name is longer than %d characters", maxDisplayNameRunes)
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "", fmt.Errorf("display_name must not contain control characters")
	}
	return name, nil
}

// The rename is already stored, so a failed publish is logged, not returned.
func (s *Server) publishMemberChanged(ctx context.Context, actor domain.MemberID, m *domain.Member) {
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("sshd: list workspaces for member.changed", "member", m.ID, "error", err)
		return
	}
	payload := events.MemberChangedPayload{MemberID: m.ID, DisplayName: m.DisplayName}
	for _, ws := range workspaces {
		if _, perr := s.cfg.Bus.Publish(ctx, events.Event{WorkspaceID: ws.ID, ActorID: actor, Payload: payload}); perr != nil {
			slog.Warn("sshd: publish member.changed", "member", m.ID, "workspace", ws.ID, "error", perr)
		}
	}
}
