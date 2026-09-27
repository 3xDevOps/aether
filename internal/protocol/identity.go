package protocol

// Edge access methods: the devices members reach the server through an
// edge with, and invitations naming edge accounts.
const (
	// MethodMemberDeviceList lists the caller's devices; an admin sees
	// every member's.
	MethodMemberDeviceList = "member.device.list"
	// MethodMemberDeviceApprove approves a pending device by its approval
	// code: its own member or an admin.
	MethodMemberDeviceApprove = "member.device.approve"
	// MethodMemberDeviceRevoke revokes a device and closes its
	// connections: its own member or an admin.
	MethodMemberDeviceRevoke = "member.device.revoke"
	// MethodMemberInvitationCreate invites an edge account as a new member
	// (admin only).
	MethodMemberInvitationCreate = "member.invitation.create"
	// MethodMemberInvitationList lists open invitations: an admin sees
	// every one, anyone else those they created.
	MethodMemberInvitationList = "member.invitation.list"
	// MethodMemberInvitationRevoke revokes an open invitation: its
	// creator or an admin.
	MethodMemberInvitationRevoke = "member.invitation.revoke"
	// MethodMemberIdentityLink names an edge account the caller signs in
	// with, as an invitation bound to the caller's own member: admin only.
	MethodMemberIdentityLink = "member.identity.link"
)

// Device is the wire form of a member's device. Fingerprint is the SHA256
// fingerprint of an ssh device's key; a browser device has none. It never
// carries a pending device's approval code: only the new device shows it,
// so approving with it proves the approver saw that device.
type Device struct {
	ID          string `json:"id"`
	MemberID    string `json:"member_id"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Fingerprint string `json:"fingerprint,omitempty"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
	ApprovedBy  string `json:"approved_by,omitempty"`
}

// MemberDeviceListResult is the result of member.device.list.
type MemberDeviceListResult struct {
	Devices []Device `json:"devices"`
}

// MemberDeviceApproveParams are the params of member.device.approve.
type MemberDeviceApproveParams struct {
	Code string `json:"code"`
}

// MemberDeviceRevokeParams are the params of member.device.revoke.
type MemberDeviceRevokeParams struct {
	DeviceID string `json:"device_id"`
}

// MemberDeviceResult is the result of member.device.approve and
// member.device.revoke: the device as it now stands.
type MemberDeviceResult struct {
	Device Device `json:"device"`
}

// Invitation is the wire form of an invitation. MemberID is set on a link
// created by member.identity.link, which binds the account to that member
// and carries no role of its own.
type Invitation struct {
	ID        string `json:"id"`
	Provider  string `json:"provider,omitempty"`
	Login     string `json:"login,omitempty"`
	Email     string `json:"email,omitempty"`
	Role      string `json:"role,omitempty"`
	MemberID  string `json:"member_id,omitempty"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

// MemberInvitationCreateParams are the params of member.invitation.create.
// Exactly one of Login and Email is set. A Login invitation's Provider is
// "github"; an Email invitation's Provider is "github", "google", or empty
// for either.
type MemberInvitationCreateParams struct {
	Provider string `json:"provider,omitempty"`
	Login    string `json:"login,omitempty"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`
}

// MemberIdentityLinkParams are the params of member.identity.link, shaped
// like an invitation without a role.
type MemberIdentityLinkParams struct {
	Provider string `json:"provider,omitempty"`
	Login    string `json:"login,omitempty"`
	Email    string `json:"email,omitempty"`
}

// MemberInvitationResult is the result of member.invitation.create and
// member.identity.link.
type MemberInvitationResult struct {
	Invitation Invitation `json:"invitation"`
}

// MemberInvitationListResult is the result of member.invitation.list.
type MemberInvitationListResult struct {
	Invitations []Invitation `json:"invitations"`
}

// MemberInvitationRevokeParams are the params of member.invitation.revoke.
type MemberInvitationRevokeParams struct {
	InvitationID string `json:"invitation_id"`
}
