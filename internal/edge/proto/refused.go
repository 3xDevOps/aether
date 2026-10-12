package edgeproto

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RefusedKind names one refusal on the edge path, whoever made it: the
// server, in an SSH authentication banner, or the edge or the client
// before SSH starts.
type RefusedKind string

// Refusals the server sends as a banner.
const (
	RefusedDevicePending        RefusedKind = "device-pending"
	RefusedDeviceRevoked        RefusedKind = "device-revoked"
	RefusedNotMember            RefusedKind = "not-member"
	RefusedTooManyWaiting       RefusedKind = "too-many-waiting"
	RefusedKeyOfAnotherAccount  RefusedKind = "key-of-another-account"
	RefusedAccountMismatch      RefusedKind = "account-mismatch"
	RefusedGrantKeyMismatch     RefusedKind = "grant-key-mismatch"
	RefusedClaimUserMissing     RefusedKind = "claim-user-missing"
	RefusedClaimAccountMismatch RefusedKind = "claim-account-mismatch"
	RefusedClaimWrong           RefusedKind = "claim-wrong"
	RefusedClaimExpired         RefusedKind = "claim-expired"
	RefusedClaimExhausted       RefusedKind = "claim-exhausted"
	RefusedClaimed              RefusedKind = "claimed"
	RefusedClaimMemberGone      RefusedKind = "claim-member-gone"
	RefusedClaimMemberNotAdmin  RefusedKind = "claim-member-not-admin"
	RefusedClaimAccountTaken    RefusedKind = "claim-account-of-another-member"
	RefusedClaimDeviceRevoked   RefusedKind = "claim-device-revoked"
	RefusedClaimFailed          RefusedKind = "claim-failed"
	RefusedJoinWrong            RefusedKind = "join-wrong"
	RefusedInvitationNeedsLink  RefusedKind = "invitation-needs-link"
)

// Refusals the edge answers over HTTP, and the client's own when it holds
// no sign-in. RefusedEdge is any other edge refusal, its text in Refusal.
const (
	RefusedNotSignedIn    RefusedKind = "not-signed-in"
	RefusedTokenRevoked   RefusedKind = "token-revoked"
	RefusedNotConnected   RefusedKind = "server-not-connected"
	RefusedUnknownServer  RefusedKind = "unknown-server"
	RefusedTooMany        RefusedKind = "too-many-attempts"
	RefusedConnLimit      RefusedKind = "connection-limit"
	RefusedServerBlocked  RefusedKind = "server-blocked"
	RefusedAccountBlocked RefusedKind = "account-blocked"
	RefusedIdentityStale  RefusedKind = "identity-stale"
	RefusedEdge           RefusedKind = "edge-refused"
)

// refusedBanner starts the banner line that carries a Refused as JSON.
const refusedBanner = "aether-refusal/1 "

// Refused is a structured refusal on the edge path. Kind decides which
// fields are set and what Error says; a field a kind does not use is
// empty. ApprovalCode approves a pending device, so a Refused that
// carries one goes only to that device; the server sets it only when
// nobody can approve the device in the app.
type Refused struct {
	Kind RefusedKind `json:"kind"`
	// Server is the server's name, its host name.
	Server      string `json:"server,omitempty"`
	Device      string `json:"device,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Account is the account as its owner knows it: "github account dana".
	Account string `json:"account,omitempty"`
	// MemberID, MemberName and Role are the member approving a pending
	// device admits it as; Role alone for an invitation adding a new
	// member. For the claim kinds MemberID is the member the claim code
	// names, and BoundMember the member an account already belongs to.
	MemberID    string `json:"member_id,omitempty"`
	MemberName  string `json:"member_name,omitempty"`
	Role        string `json:"role,omitempty"`
	BoundMember string `json:"bound_member,omitempty"`
	Invitation  string `json:"invitation,omitempty"`
	// Waiting is how many devices of one account may wait for approval.
	Waiting int `json:"waiting,omitempty"`
	// Registered is set for a device that signing in admitted, under
	// account access, which now waits for approval; Direct for a refusal
	// on a direct connection, which admits approved devices only.
	Registered   bool   `json:"registered,omitempty"`
	Direct       bool   `json:"direct,omitempty"`
	ApprovalCode string `json:"approval_code,omitempty"`
	// User is the SSH user name the device connected as, and Principal the
	// account the edge signed it in as, "<provider>:<subject>". For a
	// claim, User is the account the claim names; for a join, the login or
	// email the invitation names.
	User      string `json:"user,omitempty"`
	Principal string `json:"principal,omitempty"`
	// Refusal is the edge's refusal text for the kinds that wrap one.
	Refusal Refusal `json:"refusal,omitempty"`
	// Detail is why a claim failed on the server.
	Detail string `json:"detail,omitempty"`
}

// ApprovalAlphabet is the alphabet of a device approval code. It leaves
// out 0, 1, I and O, which people misread.
const ApprovalAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// deviceIDAlphabet is the alphabet the server draws device ids from.
const deviceIDAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

func textOver(s, alphabet string) bool {
	return strings.Trim(s, alphabet) == ""
}

// validApprovalCode reports whether s is an approval code as the server
// shows it: XXXX-XXXX over ApprovalAlphabet.
func validApprovalCode(s string) bool {
	return len(s) == 9 && s[4] == '-' && textOver(s[:4]+s[5:], ApprovalAlphabet)
}

// validDeviceID reports whether s is a device id as the server generates
// it: "dev-" and 10 characters, or the older 26 characters.
func validDeviceID(s string) bool {
	if id, ok := strings.CutPrefix(s, "dev-"); ok {
		return len(id) == 10 && textOver(id, deviceIDAlphabet)
	}
	return len(s) == 26 && textOver(s, deviceIDAlphabet)
}

// RefusedBy is the Refused for r, which the edge or a claim sent.
func RefusedBy(r Refusal) *Refused {
	kind := RefusedEdge
	switch r {
	case RefusalTokenRequired:
		kind = RefusedNotSignedIn
	case RefusalTokenRevoked:
		kind = RefusedTokenRevoked
	case RefusalNotMember:
		kind = RefusedNotMember
	case RefusalUnknownServer:
		kind = RefusedUnknownServer
	case RefusalNotConnected:
		kind = RefusedNotConnected
	case RefusalTooMany:
		kind = RefusedTooMany
	case RefusalConnLimit:
		kind = RefusedConnLimit
	case RefusalClaimWrong:
		kind = RefusedClaimWrong
	case RefusalClaimExpired:
		kind = RefusedClaimExpired
	case RefusalClaimExhausted:
		kind = RefusedClaimExhausted
	case RefusalClaimed:
		kind = RefusedClaimed
	case RefusalServerBlocked:
		kind = RefusedServerBlocked
	case RefusalAccountBlocked:
		kind = RefusedAccountBlocked
	case RefusalIdentityStale:
		kind = RefusedIdentityStale
	}
	return &Refused{Kind: kind, Refusal: r}
}

// Error is the refusal as a terminal shows it, with the command that
// resolves it where there is one.
func (r *Refused) Error() string {
	switch r.Kind {
	case RefusedDevicePending:
		state := "is waiting for approval"
		switch {
		case r.Registered && r.Direct:
			state = "is not approved, and a direct connection accepts approved devices only"
		case r.Registered:
			state = "was admitted by signing in alone and is waiting for approval: this server now admits approved devices only"
		}
		if r.ApprovalCode == "" {
			who := "An admin approves it from the dashboard's Needs you card"
			if r.MemberID != "" {
				who += ", or you do, from an approved device of yours"
			}
			return fmt.Sprintf("device %q, signed in as %s, %s. %s, or with:\n  aether device approve %s\n"+
				"or on the server:\n  sudo aether-server device review",
				r.Device, r.Account, state, who, r.DeviceID)
		}
		who := "from an approved device, SSH key or tailnet connection of this account, or as an admin"
		if r.Invitation != "" {
			who = "as an admin, or as the member an account link names; approving it accepts the invitation"
		}
		return fmt.Sprintf("device %q, signed in as %s, %s. Approve it %s:\n"+
			"  aether device approve %s\nor on the server:\n  sudo aether-server device approve %s",
			r.Device, r.Account, state, who, r.ApprovalCode, r.ApprovalCode)
	case RefusedDeviceRevoked:
		return fmt.Sprintf("device %q was revoked on this server", r.Device)
	case RefusedNotMember:
		if r.Account == "" {
			return string(RefusalNotMember)
		}
		return r.Account + " is not a member of this server"
	case RefusedTooManyWaiting:
		return fmt.Sprintf("%d devices of %s are waiting for approval on this server already, so no new one is recorded. "+
			"An admin approves or revokes them with:\n  aether device list\nor on the server:\n  sudo aether-server device review",
			r.Waiting, r.Account)
	case RefusedKeyOfAnotherAccount:
		return "this device key is registered to another account on this server"
	case RefusedAccountMismatch:
		return fmt.Sprintf("this device connects as %q, but the edge signed the connection in as %s (%s); nothing was recorded",
			r.User, r.Account, r.Principal)
	case RefusedGrantKeyMismatch:
		return "the key offered is not the device key the edge signed this connection in with"
	case RefusedClaimUserMissing:
		return "this connection claims the server: its SSH user name must carry the claim code and the claiming account"
	case RefusedClaimAccountMismatch:
		return fmt.Sprintf("this device claims as %s, but the edge signed the connection in as %s; the claim was not attempted",
			r.User, r.Account)
	case RefusedClaimMemberGone:
		return fmt.Sprintf("the claim code names member %s, which no longer exists; get a new code on the server", r.MemberID)
	case RefusedClaimMemberNotAdmin:
		return fmt.Sprintf("the claim code names member %s, who is %s, not an admin; the claim was refused", r.MemberID, r.Role)
	case RefusedClaimAccountTaken:
		return fmt.Sprintf("%s belongs to member %s on this server, not to %s; the claim was refused", r.Account, r.BoundMember, r.MemberID)
	case RefusedClaimDeviceRevoked:
		return fmt.Sprintf("device %q was revoked on this server; claim from another device", r.Device)
	case RefusedClaimFailed:
		return "the claim failed on the server: " + r.Detail
	case RefusedJoinWrong:
		if r.Account != "" {
			return fmt.Sprintf("this invitation is for %s, and this device signed in as %s", r.User, r.Account)
		}
		return "the invitation link is not valid for this server: wrong, revoked or expired; ask the admin for a new one"
	case RefusedInvitationNeedsLink:
		return r.Account + " is invited to this server; open the invitation link the admin sent, or run: aether link <link>"
	case RefusedNotSignedIn:
		if r.Refusal == "" {
			return "not signed in"
		}
	}
	return string(r.Refusal)
}

// Banner is r as an SSH authentication banner: the line
// "aether-refusal/1 <JSON>", which ParseRefusedBanner reads, then the
// prose, which a client that does not read the line shows as it is.
func (r *Refused) Banner() string {
	// A struct of strings, an int and bools always marshals.
	raw, _ := json.Marshal(r)
	return refusedBanner + string(raw) + "\n" + r.Error() + "\n"
}

// ParseRefusedBanner reads the Refused a banner from Banner carries. A
// banner without that first line, one whose fields carry control
// characters, or one whose approval code or device id is not shaped as
// the server generates them, is not structured: Error puts both into
// commands a person runs.
func ParseRefusedBanner(banner string) (*Refused, bool) {
	line, _, _ := strings.Cut(banner, "\n")
	raw, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), refusedBanner)
	if !ok {
		return nil, false
	}
	var r Refused
	if json.Unmarshal([]byte(raw), &r) != nil || r.Kind == "" {
		return nil, false
	}
	for _, s := range []string{string(r.Kind), r.Server, r.Device, r.DeviceID, r.Fingerprint, r.Account, r.MemberID, r.MemberName,
		r.Role, r.BoundMember, r.Invitation, r.ApprovalCode, r.User, r.Principal, string(r.Refusal), r.Detail} {
		if !validText(s, maxErrorText) {
			return nil, false
		}
	}
	if r.ApprovalCode != "" && !validApprovalCode(r.ApprovalCode) || r.DeviceID != "" && !validDeviceID(r.DeviceID) {
		return nil, false
	}
	return &r, true
}
