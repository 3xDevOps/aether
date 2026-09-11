package webgate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// Backend is what an authorized request may do: control-channel calls plus
// the streaming subsystems the WebSocket handlers bridge. The local
// gateway proxies it over the member's SSH connection; the server gateway
// serves it in-process for the member WhoIs identified.
type Backend interface {
	// Call performs one control-channel method call. Server-reported
	// failures and transport failures both surface as *protocol.Error.
	Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error)
	// Events opens the events subsystem: NDJSON events, the header and ack
	// already consumed. A server refusal surfaces as *protocol.Error.
	Events(ctx context.Context, req protocol.SubscribeRequest) (io.ReadCloser, error)
	// Attach opens the attach subsystem for one run's PTY.
	Attach(ctx context.Context, req protocol.AttachRequest) (Terminal, protocol.AttachResponse, error)
	// Terminal opens the member's persistent terminal PTY.
	Terminal(ctx context.Context, req protocol.TerminalRequest) (Terminal, protocol.TerminalResponse, error)
}

// Terminal is an attached PTY: a byte stream whose window can be resized
// while it is open. A read error of *protocol.RemoteExitError carries the
// exit status the server ended the attach with.
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows uint) error
}

// Authorizer identifies the caller of r and returns the backend acting as
// them, or the refusal to answer with. handshake marks a WebSocket
// upgrade, which cannot carry headers, so a credential may also ride the
// query string there.
type Authorizer func(r *http.Request, handshake bool) (Backend, *Refusal)

// Refusal is an authorizer's answer for a caller it does not admit: the
// HTTP status and the JSON-RPC error the SPA decodes.
type Refusal struct {
	Status int
	Error  *protocol.Error
}

// Write answers the request with the refusal.
func (r *Refusal) Write(w http.ResponseWriter) {
	if r.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	WriteError(w, r.Status, r.Error)
}
