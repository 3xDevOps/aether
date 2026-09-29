package protocol

// Edge access methods: the devices members reach the server through an
// edge with, and invitations naming edge accounts.
const (
	// MethodMemberDeviceList lists the caller's devices; an admin sees
	// every member's.
	MethodMemberDeviceList = "member.device.list"
	// MethodMemberDeviceApprove approves a device awaiting approval by its
	// approval code: its own member or an admin, on a connection that did
	// not sign in with a device awaiting approval.
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
	// MethodServerOwnerTransfer makes another admin, through one of their
	// edge identities, the server's owner at its edge: admin only.
	MethodServerOwnerTransfer = "server.owner.transfer"
)

// Device is the wire form of a member's device. Status is one of the edge
// protocol's device statuses: registered, pending, approved or revoked.
// Fingerprint is the SHA256 fingerprint of its device key. It never
// carries the approval code of a device awaiting approval: only that
// device shows it, so approving with it proves the approver saw that
// device.
type Device struct {
	ID          string `json:"id"`
	MemberID    string `json:"member_id"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Fingerprint string `json:"fingerprint"`
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

// ServerOwnerTransferParams are the params of server.owner.transfer.
// Provider picks one of the member's edge identities and may be left out
// when the member has exactly one.
type ServerOwnerTransferParams struct {
	MemberID string `json:"member_id"`
	Provider string `json:"provider,omitempty"`
}

// ServerOwnerTransferResult is the new owner: the member and the edge
// identity the edge now records as the server's owner.
type ServerOwnerTransferResult struct {
	MemberID string `json:"member_id"`
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
	Login    string `json:"login,omitempty"`
	Email    string `json:"email,omitempty"`
}
