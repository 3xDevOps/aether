package edgeproto

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
)

// Edge HTTP paths. An edge answers on two origins, which one process may
// serve: the relay origin, which servers enroll with and clients connect
// through, and the sign-in origin, which signs people in and serves the
// client API. Each block below names the origin that serves its paths. A
// client that knows only the relay origin reads the sign-in origin from
// PathEdgeInfo.
//
// Every /v1 request authenticates with "Authorization: Bearer <token>":
// the device token for client paths, the open's ticket for
// PathServerData. PathServerControl authenticates by the enrollment
// signature, and PathEdgeInfo, PathDeviceStart and PathDeviceToken need
// no token. The patterns with a {name} segment are http.ServeMux
// patterns; build concrete paths with the functions below.

// Paths served on the relay origin.
const (
	PathEdgeInfo      = "/v1/edge"
	PathServerControl = "/v1/server/control"
	PathServerData    = "/v1/server/data/{conn_id}"
	PathConnect       = "/v1/connect/{server_id}"
	// PathClaimConnect opens a connection of KindClaim. The edge refuses
	// it with RefusalClaimed for a server that has an owner.
	PathClaimConnect = "/v1/connect/{server_id}/claim"
)

// Paths served on the sign-in origin.
const (
	PathDeviceStart = "/v1/device/start"
	PathDeviceToken = "/v1/device/token"
	PathLogout      = "/v1/device/logout"
	PathServers     = "/v1/servers"
	PathAccount     = "/v1/account"
	// PathAccountPage is the Account page, where a browser that signed in
	// within the last few minutes deletes the account.
	PathAccountPage = "/account"
)

// DataPath is the path a server dials to attach the data socket of connID.
func DataPath(connID string) string { return "/v1/server/data/" + connID }

// ConnectPath is the path a client dials to reach serverID.
func ConnectPath(serverID string) string { return "/v1/connect/" + serverID }

// ClaimConnectPath is the path a client dials to claim serverID.
func ClaimConnectPath(serverID string) string { return "/v1/connect/" + serverID + "/claim" }

// HeaderVersion carries the sender's protocol Version on client requests
// and on edge responses.
const HeaderVersion = "Aether-Edge-Version"

// MaxRequestBodySize bounds the JSON body of any edge API request.
const MaxRequestBodySize = 64 << 10

// ErrorBody is the JSON body of every refused edge API request.
type ErrorBody struct {
	Error string `json:"error"`
}

// Refusal is a refusal message the edge sends as ErrorBody.Error, with the
// HTTP status from Status. The claim code refusals are the server's: it
// sends their text as the SSH authentication banner of a claim connection.
type Refusal string

const (
	RefusalTokenRequired  Refusal = "device token required"
	RefusalTokenRevoked   Refusal = "device token revoked"
	RefusalNotMember      Refusal = "not a member of this server"
	RefusalUnknownServer  Refusal = "unknown server"
	RefusalNotConnected   Refusal = "server is not connected to the edge"
	RefusalNotAttached    Refusal = "server did not attach"
	RefusalTooMany        Refusal = "too many attempts"
	RefusalConnLimit      Refusal = "connection limit reached"
	RefusalClaimWrong     Refusal = "claim code is wrong"
	RefusalClaimExpired   Refusal = "claim code expired"
	RefusalClaimExhausted Refusal = "claim code has no attempts left"
	RefusalClaimed        Refusal = "server is already claimed"
	RefusalServerBlocked  Refusal = "server is blocked by this edge's operator"
	RefusalAccountBlocked Refusal = "account is blocked by this edge's operator"
	// RefusalIdentityStale refuses an account an invitation would admit
	// but whose login and email are older than IdentityMaxAge.
	RefusalIdentityStale Refusal = "your login and email were last confirmed over 24 hours ago; open this edge in a browser to confirm them, then retry"
)

func (r Refusal) Error() string { return string(r) }

// Status is the HTTP status the edge sends with r: 403 for a refusal text
// this package does not name, such as a server's OpenResult.Error.
func (r Refusal) Status() int {
	switch r {
	case RefusalTokenRequired, RefusalTokenRevoked:
		return http.StatusUnauthorized
	case RefusalNotMember, RefusalServerBlocked, RefusalAccountBlocked, RefusalIdentityStale:
		return http.StatusForbidden
	case RefusalUnknownServer:
		return http.StatusNotFound
	case RefusalClaimed:
		return http.StatusConflict
	case RefusalTooMany, RefusalConnLimit:
		return http.StatusTooManyRequests
	case RefusalNotConnected:
		return http.StatusServiceUnavailable
	case RefusalNotAttached:
		return http.StatusGatewayTimeout
	}
	return http.StatusForbidden
}

// Device authorization (RFC 8628 shape). A client posts DeviceStartRequest
// to PathDeviceStart, shows VerificationURI and UserCode, then polls
// PathDeviceToken every Interval seconds. Until the person confirms, the
// edge answers 400 with ErrorBody.Error set to one of the Device* states;
// on DeviceSlowDown the client adds 5 seconds to its interval.
const (
	DevicePending  = "authorization_pending"
	DeviceSlowDown = "slow_down"
	DeviceDenied   = "access_denied"
	DeviceExpired  = "expired_token"
)

// DeviceStartRequest registers a client install: its label and its device
// key as an authorized_keys line.
type DeviceStartRequest struct {
	Label string `json:"label"`
	Key   string `json:"key"`
}

// Validate checks the label and parses the key.
func (r DeviceStartRequest) Validate() error {
	if !validRequiredText(r.Label, maxIDText) {
		return errors.New("edgeproto: device label is empty, too long or has control characters")
	}
	_, err := ParseDeviceKey(r.Key)
	return err
}

// DeviceStartResponse starts the authorization. VerificationURI holds no
// secret; the person types UserCode there.
type DeviceStartResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceTokenRequest polls for the device token.
type DeviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

// DeviceTokenResponse is the approved result: the device token (shown only
// here), the device it belongs to and the signed-in account.
type DeviceTokenResponse struct {
	Token   string      `json:"token"`
	Device  Device      `json:"device"`
	Account AccountInfo `json:"account"`
}

// ServerKind says who runs a server, and so who can read its workspaces.
type ServerKind string

const (
	// ServerSelfHosted runs on hardware its owner operates.
	ServerSelfHosted ServerKind = "self-hosted"
	// ServerHosted is reserved for a server Aether operates. This version
	// parses and shows it and never produces it.
	ServerHosted ServerKind = "hosted"
)

// ParseServerKind returns the kind s names. An empty s is
// ServerSelfHosted: an edge that sends no kind predates hosted servers.
func ParseServerKind(s string) (ServerKind, error) {
	switch k := ServerKind(s); k {
	case "":
		return ServerSelfHosted, nil
	case ServerSelfHosted, ServerHosted:
		return k, nil
	}
	return "", fmt.Errorf("edgeproto: unknown server kind %q, want %s or %s", s, ServerSelfHosted, ServerHosted)
}

// ServerInfo is one server reachable by the account: as a member, with the
// member's role, or as an invitee, with the invited role. AccessPolicy is
// the policy the server announced in its hello and Kind is the edge's
// record; read them with ParseAccessPolicy and ParseServerKind. Both are
// the edge's statement, for display: the server enforces its own policy.
type ServerInfo struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Online       bool         `json:"online"`
	Role         string       `json:"role"`
	AccessPolicy AccessPolicy `json:"access_policy"`
	Kind         ServerKind   `json:"kind"`
}

// ServersResponse answers GET PathServers.
type ServersResponse struct {
	Servers []ServerInfo `json:"servers"`
}

// AccountSummary answers GET PathAccount: the signed-in account and what
// deleting it touches, the servers it owns and the other servers it is a
// member of. Confirm is the text the Account page asks the person to type
// to delete it.
type AccountSummary struct {
	Account AccountInfo  `json:"account"`
	Owned   []ServerInfo `json:"owned"`
	Member  []ServerInfo `json:"member"`
	Confirm string       `json:"confirm"`
}

// EdgeInfo answers GET PathEdgeInfo on the relay origin: the sign-in
// origin, the key grants are signed with and its EdgeKeyFingerprint, and
// the protocol versions the edge speaks.
type EdgeInfo struct {
	SigninOrigin string            `json:"signin_origin"`
	Key          ed25519.PublicKey `json:"key"`
	Fingerprint  string            `json:"fingerprint"`
	Version      int               `json:"version"`
	MinVersion   int               `json:"min_version"`
}

// Validate checks that SigninOrigin is a canonical Origin, that Key is an
// Ed25519 public key whose fingerprint is Fingerprint, and that the
// versions are ordered. A valid EdgeInfo is still only what the origin
// that served it states.
func (i EdgeInfo) Validate() error {
	if o, err := Origin(i.SigninOrigin); err != nil || o != i.SigninOrigin {
		return fmt.Errorf("edgeproto: sign-in origin %q is not a canonical edge origin", i.SigninOrigin)
	}
	if len(i.Key) != ed25519.PublicKeySize {
		return fmt.Errorf("edgeproto: edge key is %d bytes, want %d", len(i.Key), ed25519.PublicKeySize)
	}
	if got := EdgeKeyFingerprint(i.Key); got != i.Fingerprint {
		return fmt.Errorf("edgeproto: edge key fingerprint is %s, the edge stated %q", got, i.Fingerprint)
	}
	if i.MinVersion < 1 || i.Version < i.MinVersion {
		return fmt.Errorf("edgeproto: edge speaks versions %d to %d", i.MinVersion, i.Version)
	}
	return nil
}
