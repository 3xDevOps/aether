package sshd

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// The device change is already stored, so a failed publish is logged, not
// returned.
func (s *Server) publishDevice(ctx context.Context, actor domain.MemberID, id domain.DeviceID, status domain.DeviceStatus) {
	ev := events.Event{WorkspaceID: events.ServerScope, ActorID: actor, Payload: events.MemberDevicePayload{DeviceID: id, Status: status}}
	if _, err := s.cfg.Bus.Publish(ctx, ev); err != nil {
		slog.Warn("sshd: publish member.device", "device", id, "error", err)
	}
}

// publishDroppedDevices publishes member.device revoked for each device in
// before that ids no longer holds: a deleted member or invitation takes
// its devices with it.
func (s *Server) publishDroppedDevices(ctx context.Context, actor domain.MemberID, ids store.IdentityStore, before []*domain.Device) {
	after, err := ids.ListDevices(ctx, "")
	if err != nil {
		slog.Warn("sshd: list devices to publish member.device", "error", err)
		return
	}
	kept := make(map[domain.DeviceID]bool, len(after))
	for _, d := range after {
		kept[d.ID] = true
	}
	for _, d := range before {
		if !kept[d.ID] {
			s.publishDevice(ctx, actor, d.ID, domain.DeviceRevoked)
		}
	}
}
