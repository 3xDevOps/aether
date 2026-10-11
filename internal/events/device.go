package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeMemberDevice reports a device recorded, approved or revoked. Devices
// are server-wide and the timeline is per workspace, so it is published
// once per workspace. It never carries the approval code.
const TypeMemberDevice Type = "member.device"

type MemberDevicePayload struct {
	DeviceID     domain.DeviceID     `json:"device_id"`
	Status       domain.DeviceStatus `json:"status"`
	MemberID     domain.MemberID     `json:"member_id,omitempty"`
	InvitationID domain.InvitationID `json:"invitation_id,omitempty"`
}

func (MemberDevicePayload) EventType() Type { return TypeMemberDevice }

func init() { registerPayload[MemberDevicePayload](TypeMemberDevice) }
