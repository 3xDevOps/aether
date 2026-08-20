package sshd

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Bridge is the dashboard gateway's view of the control channel: a second
// transport onto the registered method handlers, so an HTTP request and an
// SSH JSON-RPC request run the same code down to the pending-member and
// capability gates. It never listens, handshakes, or holds a connection.
//
// It is built from the same Config the SSH server is built from, resolved
// on first use because service builders keep attaching their seams to that
// config after the gateway itself is constructed.
type Bridge struct {
	cfg  *Config
	once sync.Once
	srv  *Server
}

// NewBridge returns a bridge over cfg. The pointer is retained, not
// copied: the config is still being assembled when the gateway is built.
func NewBridge(cfg *Config) *Bridge { return &Bridge{cfg: cfg} }

func (b *Bridge) server() *Server {
	b.once.Do(func() { b.srv = &Server{cfg: *b.cfg, baseCtx: context.Background()} })
	return b.srv
}

// Call dispatches one control-channel method and returns the response the
// SSH transport would have written.
func (b *Bridge) Call(ctx context.Context, member domain.MemberID, method string, params json.RawMessage) protocol.Response {
	switch method {
	case protocol.MethodWorkspaceToolsList, protocol.MethodWorkspaceToolsVerify,
		protocol.MethodWorkspaceToolsRollback, protocol.MethodWorkspaceToolsReset:
		return protocol.Response{JSONRPC: "2.0", ID: json.RawMessage(`1`), Error: &protocol.Error{Code: protocol.CodeDenied, Message: "workspace tools are available only on the SSH control channel"}}
	}
	line, err := json.Marshal(protocol.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return protocol.Response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &protocol.Error{Code: protocol.CodeInvalidParams, Message: "invalid params: " + err.Error()},
		}
	}
	return b.server().handleRequest(ctx, member, line)
}

// CheckMember re-validates that member still exists and is approved: the
// gate the event and attach subsystems apply before streaming anything.
func (b *Bridge) CheckMember(ctx context.Context, member domain.MemberID) *protocol.Error {
	if err := b.server().checkMember(ctx, member); err != nil {
		return rpcError(err)
	}
	return nil
}

// PublishAttachPresence publishes the presence transition an attach makes,
// through the same publisher the attach subsystem uses: a browser mirror
// has to reach the roster exactly like an SSH attach, or two members steer
// the same run each believing they are alone. Best effort and never
// canceled by the caller's context, so the detach notice still goes out
// once the socket is gone.
func (b *Bridge) PublishAttachPresence(ctx context.Context, run domain.RunID, member domain.MemberID, state events.PresenceState) {
	s := b.server()
	r, err := s.cfg.Store.GetRun(context.WithoutCancel(ctx), run)
	if err != nil {
		return
	}
	s.publishPresence(r, member, state)
}

// WireError maps a seam error - a failed PTY attach above all - onto the
// wire error object, through the same table the SSH transport uses.
func (b *Bridge) WireError(err error) *protocol.Error { return rpcError(err) }

// CheckSteer reports whether member may write to run's terminal - the same
// capability the PTY write gate enforces, checked up front so a browser
// asking for a write attach it cannot have is told so instead of silently
// mirroring.
func (b *Bridge) CheckSteer(ctx context.Context, member domain.MemberID, run domain.RunID) *protocol.Error {
	s := b.server()
	if err := NewWriteGate(s.cfg.Store)(ctx, member, run); err != nil {
		return rpcError(err)
	}
	return nil
}
