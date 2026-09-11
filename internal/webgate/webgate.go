// Package webgate is the transport-neutral dashboard gateway: the
// embedded SPA, the /api/v1 shape, and the events, attach and terminal
// WebSockets, bridged onto a Backend the composer's Authorizer hands back
// per request. The local gateway (internal/localgw) composes it with a
// per-process bearer token over the member's SSH connection; the server
// gateway (internal/servergw) with Tailscale WhoIs and in-process
// subsystems. Framing, timeouts, keepalive pings and close reasons live
// here once, so both transports behave identically.
package webgate

import (
	"encoding/json"
	"net/http"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// ErrorBody is the failure envelope every endpoint answers with, carrying
// the JSON-RPC error object unchanged so a client can branch on the code
// rather than the HTTP status.
type ErrorBody struct {
	Error *protocol.Error `json:"error"`
}

// WriteError answers with the JSON error envelope under the given status.
func WriteError(w http.ResponseWriter, status int, e *protocol.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{Error: e})
}

// StatusFor maps a wire error code onto the closest HTTP status. The code
// stays the authority; the status is what browsers and proxies read.
func StatusFor(code int) int {
	switch code {
	case protocol.CodeParse, protocol.CodeInvalidRequest, protocol.CodeInvalidParams:
		return http.StatusBadRequest
	case protocol.CodeDenied:
		return http.StatusForbidden
	case protocol.CodeMethodNotFound, protocol.CodeNotFound:
		return http.StatusNotFound
	case protocol.CodeInvalidState, protocol.CodeConflict:
		return http.StatusConflict
	case protocol.CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
