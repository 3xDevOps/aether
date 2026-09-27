package edgeproto

import (
	"crypto/ed25519"
	"errors"
	"net/http"
)

// Edge HTTP paths. The patterns with a {name} segment are http.ServeMux
// patterns; build concrete paths with DataPath and ConnectPath.
//
// Every /v1 request authenticates with "Authorization: Bearer <token>":
// the device token for client paths, the open's ticket for
// PathServerData. PathServerControl authenticates by the enrollment
// signature, and PathDeviceStart, PathDeviceToken and PathEdgeKey need no
// token.
const (
	PathServerControl = "/v1/server/control"
	PathServerData    = "/v1/server/data/{conn_id}"
	PathConnect       = "/v1/connect/{server_id}"

	PathDeviceStart = "/v1/device/start"
	PathDeviceToken = "/v1/device/token"
	PathLogout      = "/v1/device/logout"
	PathServers     = "/v1/servers"
	PathClaim       = "/v1/claim"
	PathEdgeKey     = "/v1/edge-key"
)

// DataPath is the path a server dials to attach the data socket of connID.
func DataPath(connID string) string { return "/v1/server/data/" + connID }

// ConnectPath is the path a client dials to reach serverID.
func ConnectPath(serverID string) string { return "/v1/connect/" + serverID }

// HeaderVersion carries the sender's protocol Version on client requests
// and on edge responses.
const HeaderVersion = "Aether-Edge-Version"

// MaxRequestBodySize bounds the JSON body of any edge API request.
const MaxRequestBodySize = 64 << 10

// PathAddServer is the edge page where a signed-in person enters a claim
// code.
const PathAddServer = "/servers/add"

// Web sign-in. The server redirects the browser to
// https://<edge>PathAuthorize?server=&state=&challenge=; the edge returns it
// to https://<server hostname>PathAuthCallback?code=&state=, or, when
// return=app, to AppCallbackURL with the same parameters. The edge computes
// the return address; it never takes one from the request.
const (
	PathAuthorize    = "/authorize"
	PathAuthCallback = "/auth/callback"
	AppCallbackURL   = "aether://auth/callback"

	ParamServer    = "server"
	ParamState     = "state"
	ParamChallenge = "challenge"
	ParamCode      = "code"
	ParamReturn    = "return"
	ReturnApp      = "app"
)

// ErrorBody is the JSON body of every refused edge API request.
type ErrorBody struct {
	Error string `json:"error"`
}

// Refusal is a refusal message the edge sends as ErrorBody.Error, with the
// HTTP status from Status. Claim refusals come from the server, which sends
// the same text in ClaimResult.Error.
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
	case RefusalNotMember, RefusalClaimWrong, RefusalClaimExpired, RefusalClaimExhausted,
		RefusalServerBlocked, RefusalAccountBlocked, RefusalIdentityStale:
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
	Token   string  `json:"token"`
	Device  Device  `json:"device"`
	Account Account `json:"account"`
}

// ServerInfo is one server reachable by the account: as a member, with the
// member's role, or as an invitee, with the invited role.
type ServerInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	Role   string `json:"role"`
}

// ServersResponse answers GET PathServers. ServerDomain is the domain the
// edge passes dashboards through under: a server's dashboard is
// https://ServerHostname(id, ServerDomain). An edge older than the field
// leaves it out.
type ServersResponse struct {
	Servers      []ServerInfo `json:"servers"`
	ServerDomain string       `json:"server_domain,omitempty"`
}

// ClaimRequest is POST PathClaim: a claim code as the person typed it.
type ClaimRequest struct {
	Code string `json:"code"`
}

// ClaimResponse names the server the account now owns.
type ClaimResponse struct {
	ServerID string `json:"server_id"`
	Name     string `json:"name"`
}

// EdgeKeyResponse answers GET PathEdgeKey with the key grants are signed
// with and its EdgeKeyFingerprint. A reader checks the key is
// ed25519.PublicKeySize bytes and computes the fingerprint itself.
type EdgeKeyResponse struct {
	Key         ed25519.PublicKey `json:"key"`
	Fingerprint string            `json:"fingerprint"`
}
