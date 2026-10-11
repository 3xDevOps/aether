package sshd

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// The device change is already stored, so a failed publish is logged, not
// returned.
func (s *Server) publishDevice(ctx context.Context, actor domain.MemberID, id domain.DeviceID, status domain.DeviceStatus) {
	ev := events.Event{WorkspaceID: events.ServerScope, ActorID: actor, Payload: events.MemberDevicePayload{DeviceID: id, Status: status}}
	if _, err := s.cfg.Bus.Publish(ctx, ev); err != nil {
		slog.Warn("sshd: publish member.device", "device", id, "error", err)
	}
}
