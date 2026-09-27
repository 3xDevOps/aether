package domain

import "time"

// Identity binds a member to an account signed in at an edge. Provider and
// Subject are the account's immutable key; Email and Login are what the
// provider reported when the identity was bound, for display.
type Identity struct {
	Member    MemberID
	Provider  string
	Subject   string
	Email     string
	Login     string
	CreatedAt time.Time
}

// DeviceID identifies a Device.
type DeviceID string

// DeviceKind is how a device authenticates to the server.
type DeviceKind string

const (
	// DeviceSSH is a client install; its credential is its device key as
	// an authorized_keys line.
	DeviceSSH DeviceKind = "ssh"
	// DeviceBrowser is one browser's dashboard session; its credential is
	// the hash of the session token.
	DeviceBrowser DeviceKind = "browser"
)

// DeviceStatus is where a device stands in approval.
type DeviceStatus string

const (
	DevicePending  DeviceStatus = "pending"
	DeviceApproved DeviceStatus = "approved"
	DeviceRevoked  DeviceStatus = "revoked"
)

// Device is one credential a member reaches the server through an edge
// with. ApprovalCode is set only while the device is pending.
type Device struct {
	ID           DeviceID
	Member       MemberID
	Kind         DeviceKind
	Credential   string
	Label        string
	Status       DeviceStatus
	ApprovalCode string
	CreatedAt    time.Time
	LastSeenAt   *time.Time
	// ApprovedBy is the member who approved a device that needed approval;
	// empty for a member's first device.
	ApprovedBy MemberID
}

// InvitationID identifies an Invitation.
type InvitationID string

// Invitation names an edge account, by GitHub login or by verified email,
// that may join the server. With Member empty, accepting it creates a
// member with Role. With Member set, accepting it binds the account to that
// existing member instead: that is how a member who joined by key or
// tailnet links an edge account.
type Invitation struct {
	ID InvitationID
	// Provider is "github" for a login invitation, and "", "github" or
	// "google" for an email invitation; "" accepts either provider.
	Provider   string
	Login      string
	Email      string
	Role       Role
	Member     MemberID
	CreatedBy  MemberID
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}
