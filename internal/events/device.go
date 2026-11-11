package events

import "github.com/3xDevOps/Aether/internal/domain"

// TypeMemberDevice reports a device recorded, approved or revoked. It never
// carries the approval code.
const TypeMemberDevice Type = "member.device"

// ServerScope is the workspace id of events that belong to no workspace:
// devices are server-wide.
const ServerScope domain.WorkspaceID = "server"

// MemberDevicePayload names, as MemberID, the member the device signs in
// as: for a device waiting on an invitation, the member the invitation
// links or, for a new member, its creator. It is empty when unknown, and
// then only super admins are shown the event.
type MemberDevicePayload struct {
	DeviceID domain.DeviceID     `json:"device_id"`
	Status   domain.DeviceStatus `json:"status"`
	MemberID domain.MemberID     `json:"member_id,omitempty"`
}

func (MemberDevicePayload) EventType() Type { return TypeMemberDevice }

func (p MemberDevicePayload) NamedMembers() []domain.MemberID { return []domain.MemberID{p.MemberID} }

func init() { registerPayload[MemberDevicePayload](TypeMemberDevice) }
