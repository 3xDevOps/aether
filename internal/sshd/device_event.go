package sshd

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// The device change is already stored, so a failed publish is logged, not
// returned.
func (s *Server) publishDevice(ctx context.Context, actor domain.MemberID, dev *domain.Device) {
	workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("sshd: list workspaces for member.device", "device", dev.ID, "error", err)
		return
	}
	payload := events.MemberDevicePayload{DeviceID: dev.ID, Status: dev.Status, MemberID: dev.Member, InvitationID: dev.Invitation}
	for _, ws := range workspaces {
		if _, perr := s.cfg.Bus.Publish(ctx, events.Event{WorkspaceID: ws.ID, ActorID: actor, Payload: payload}); perr != nil {
			slog.Warn("sshd: publish member.device", "device", dev.ID, "workspace", ws.ID, "error", perr)
		}
	}
}
