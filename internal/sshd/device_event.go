package sshd

import (
	"context"
	"log/slog"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

// publishDevice publishes member.device for the stored device id.
func (s *Server) publishDevice(ctx context.Context, actor domain.MemberID, id domain.DeviceID, status domain.DeviceStatus) {
	var owner domain.MemberID
	if ids, err := s.identityStore(); err == nil {
		if d, err := ids.GetDevice(ctx, id); err == nil {
			owner = deviceOwner(ctx, ids, d)
		}
	}
	s.publishDeviceOf(ctx, actor, id, status, owner)
}

// The device change is already stored, so a failed publish is logged, not
// returned.
func (s *Server) publishDeviceOf(ctx context.Context, actor domain.MemberID, id domain.DeviceID, status domain.DeviceStatus, owner domain.MemberID) {
	ev := events.Event{WorkspaceID: events.ServerScope, ActorID: actor,
		Payload: events.MemberDevicePayload{DeviceID: id, Status: status, MemberID: owner}}
	if _, err := s.cfg.Bus.Publish(ctx, ev); err != nil {
		slog.Warn("sshd: publish member.device", "device", id, "error", err)
	}
}

// deviceOwner is the member d signs in as, as MemberDevicePayload names it.
func deviceOwner(ctx context.Context, ids store.IdentityStore, d *domain.Device) domain.MemberID {
	if d.Member != "" || d.Invitation == "" {
		return d.Member
	}
	inv, err := ids.GetInvitation(ctx, d.Invitation)
	if err != nil {
		return ""
	}
	if inv.Member != "" {
		return inv.Member
	}
	return inv.CreatedBy
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
			s.publishDeviceOf(ctx, actor, d.ID, domain.DeviceRevoked, deviceOwner(ctx, ids, d))
		}
	}
}
