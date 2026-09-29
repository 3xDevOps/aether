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

// DeviceStatus is where a device stands in approval. The values are the
// edge protocol's device statuses.
type DeviceStatus string

const (
	// DeviceRegistered is a device admitted under account access on first
	// connection. No person approved it, so under approved-devices access
	// it is refused like a pending one.
	DeviceRegistered DeviceStatus = "registered"
	DevicePending    DeviceStatus = "pending"
	DeviceApproved   DeviceStatus = "approved"
	DeviceRevoked    DeviceStatus = "revoked"
)

// AwaitsApproval reports whether a person may still approve a device with
// status s.
func (s DeviceStatus) AwaitsApproval() bool {
	return s == DevicePending || s == DeviceRegistered
}

// Device is one client install a member reaches the server through an
// edge with. Credential is its device key as an authorized_keys line.
// Provider and Subject name the edge account the device signed in with,
// the member's identity; only grants naming that account may use it.
// Email, Login and Name are what the edge reported for that account when
// the device registered. ApprovalCode is set only while the device awaits
// approval.
//
// A device waiting on an invitation has Invitation set and no Member:
// approving it accepts the invitation for its account.
type Device struct {
	ID           DeviceID
	Member       MemberID
	Invitation   InvitationID
	Provider     string
	Subject      string
	Email        string
	Login        string
	Name         string
	Credential   string
	Label        string
	Status       DeviceStatus
	ApprovalCode string
	CreatedAt    time.Time
	LastSeenAt   *time.Time
	// ApprovedBy is the member who approved the device; empty when the
	// machine's administrator did, by a claim code or on the console.
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
