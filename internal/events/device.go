package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeMemberDevice reports a device recorded, approved or revoked. It never
// carries the approval code.
const TypeMemberDevice Type = "member.device"

// ServerScope is the workspace id of events that belong to no workspace:
// devices are server-wide.
const ServerScope domain.WorkspaceID = "server"

type MemberDevicePayload struct {
	DeviceID domain.DeviceID     `json:"device_id"`
	Status   domain.DeviceStatus `json:"status"`
}

func (MemberDevicePayload) EventType() Type { return TypeMemberDevice }

func init() { registerPayload[MemberDevicePayload](TypeMemberDevice) }
