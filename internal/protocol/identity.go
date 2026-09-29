package protocol

// Edge access methods: the devices members reach the server through an
// edge with, and invitations naming edge accounts.
const (
	// MethodMemberDeviceList lists the caller's devices; an admin sees
	// every member's.
	MethodMemberDeviceList = "member.device.list"
	// MethodMemberDeviceLookup describes the device an approval code names
	// and whom approving it admits, without approving it: the callers
	// member.device.approve accepts.
	MethodMemberDeviceLookup = "member.device.lookup"
	// MethodMemberDeviceApprove approves a device awaiting approval by its
	// approval code and the id member.device.lookup gave for it: its own
	// member or an admin, on a connection that did not sign in with a
	// device awaiting approval.
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
	// MethodMemberIdentityList lists a member's edge identities and the
	// devices that signed in with each: the member or an admin.
	MethodMemberIdentityList = "member.identity.list"
	// MethodMemberIdentityRemove unbinds one edge identity from a member,
	// revokes the devices that signed in with it and closes their
	// connections: the member or an admin, and under approved-devices not
	// on a connection that signed in with a device no person approved.
	MethodMemberIdentityRemove = "member.identity.remove"
	// MethodServerOwnerTransfer makes another admin, through one of their
	// edge identities, the server's owner at its edge: admin only.
	MethodServerOwnerTransfer = "server.owner.transfer"
)

// Device is the wire form of a member's device. Status is one of the edge
// protocol's device statuses: registered, pending, approved or revoked.
// Provider and Account name the edge account the device signed in as:
// its login, else its email, else its subject. A device waiting on an
// invitation has InvitationID and no MemberID. Fingerprint is the SHA256
// fingerprint of its device key. It never carries the approval code of a
// device awaiting approval: only that device shows it, so approving with
// it proves the approver saw that device.
type Device struct {
	ID           string `json:"id"`
	MemberID     string `json:"member_id"`
	InvitationID string `json:"invitation_id,omitempty"`
	Provider     string `json:"provider"`
	Account      string `json:"account"`
	Label        string `json:"label"`
	Status       string `json:"status"`
	Fingerprint  string `json:"fingerprint"`
	CreatedAt    string `json:"created_at"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
	ApprovedBy   string `json:"approved_by,omitempty"`
}

// MemberDeviceListResult is the result of member.device.list.
type MemberDeviceListResult struct {
	Devices []Device `json:"devices"`
}

// MemberDeviceLookupParams are the params of member.device.lookup.
type MemberDeviceLookupParams struct {
	Code string `json:"code"`
}

// MemberDeviceLookupResult is the result of member.device.lookup: the
// device and the member approving it admits it as, its own or the one a
// link invitation names, with that member's role. For a device waiting on
// an invitation that adds a new member, MemberID and DisplayName are empty
// and Role is the invited role.
type MemberDeviceLookupResult struct {
	Device      Device `json:"device"`
	MemberID    string `json:"member_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
}

// MemberDeviceApproveParams are the params of member.device.approve.
// DeviceID is the device member.device.lookup described for Code; the
// server refuses a code that names another, so what is approved is what
// the approver was shown.
type MemberDeviceApproveParams struct {
	Code     string `json:"code"`
	DeviceID string `json:"device_id"`
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
// Exactly one of Login, a GitHub login, and Email, a verified primary
// email of a GitHub account, is set.
type MemberInvitationCreateParams struct {
	Login string `json:"login,omitempty"`
	Email string `json:"email,omitempty"`
	Role  string `json:"role"`
}

// MemberIdentityLinkParams are the params of member.identity.link, shaped
// like an invitation without a role.
type MemberIdentityLinkParams struct {
	Login string `json:"login,omitempty"`
	Email string `json:"email,omitempty"`
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

// Identity is the wire form of an edge account bound to a member, as the
// provider reported it when it was bound.
type Identity struct {
	Provider  string `json:"provider"`
	Subject   string `json:"subject"`
	Login     string `json:"login,omitempty"`
	Email     string `json:"email,omitempty"`
	CreatedAt string `json:"created_at"`
}

// MemberIdentityListParams are the params of member.identity.list.
type MemberIdentityListParams struct {
	MemberID string `json:"member_id"`
}

// MemberIdentityListResult is the result of member.identity.list: the
// member's identities, and every device of the member with the account it
// signed in as, revoked ones and those of removed identities included.
type MemberIdentityListResult struct {
	Identities []Identity `json:"identities"`
	Devices    []Device   `json:"devices"`
}

// MemberIdentityRemoveParams are the params of member.identity.remove.
type MemberIdentityRemoveParams struct {
	MemberID string `json:"member_id"`
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
}

// MemberIdentityRemoveResult is the result of member.identity.remove: the
// devices that signed in with the removed identity, now revoked.
type MemberIdentityRemoveResult struct {
	Revoked []Device `json:"revoked"`
}

// ServerOwnerTransferParams are the params of server.owner.transfer. The
// member must have exactly one GitHub identity.
type ServerOwnerTransferParams struct {
	MemberID string `json:"member_id"`
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
