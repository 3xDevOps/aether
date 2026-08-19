package dashboard

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// wsWriteTimeout bounds one frame write, so a client that stopped
	// reading cannot pin a handler.
	wsWriteTimeout = 10 * time.Second
	// wsReadLimit bounds one client frame: subscribe headers, attach
	// headers, and terminal input are all small.
	wsReadLimit = 64 << 10
	// statusBacklogDropped tells a client its subscription lost events;
	// resubscribing with replay and its last seq recovers them.
	statusBacklogDropped = 4000
)

// handleEvents serves GET /ws/events: the SSH event subsystem's
// subscription semantics, replay cursor included, over a WebSocket.
func (g *Gateway) handleEvents(w http.ResponseWriter, r *http.Request) {
	member, token, ok := g.authenticate(w, r, true)
	if !ok {
		return
	}
	if !g.beginHandler() {
		return
	}
	defer g.wg.Done()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	if !g.trackConn(conn) {
		return
	}
	defer g.untrackConn(conn)
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go g.watchAuthorization(ctx, cancel, conn, token, member, "")
	var req protocol.SubscribeRequest
	readCtx, readDone := context.WithTimeout(ctx, readHeaderTimeout)
	rerr := wsjson.Read(readCtx, conn, &req)
	readDone()
	if rerr != nil {
		return
	}
	// The watch ticks are snapshots: re-resolve the token at header time
	// so a revoke between ticks cannot be converted into a subscription.
	if _, ok := g.tokens.resolve(token); !ok {
		rejectSubscribe(ctx, conn, &protocol.Error{Code: protocol.CodeDenied, Message: "dashboard token revoked or expired"})
		return
	}
	// Everything after the header is discarded - a client keepalive must
	// not tear the stream down - and the read loop noticing the peer go
	// away is what ends the stream.
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	if perr := g.cfg.RPC.CheckMember(ctx, member); perr != nil {
		rejectSubscribe(ctx, conn, perr)
		return
	}
	sub, perr := events.SubscribeWire(ctx, g.cfg.Bus, req)
	if perr != nil {
		rejectSubscribe(ctx, conn, perr)
		return
	}
	defer func() { _ = sub.Close() }()
	// A live subscription is ended by closing it, not by canceling the
	// context it was opened with: without this the handler would outlive
	// the socket it streams to.
	stop := context.AfterFunc(ctx, func() { _ = sub.Close() })
	defer stop()
	if writeFrame(ctx, conn, protocol.SubscribeResponse{OK: true}) != nil {
		return
	}

	if err := events.StreamWire(sub, func(ev protocol.Event) error {
		return writeFrame(ctx, conn, ev)
	}); errors.Is(err, events.ErrDropped) {
		_ = conn.Close(statusBacklogDropped, "event backlog dropped; resubscribe with after_seq")
	}
}

func rejectSubscribe(ctx context.Context, conn *websocket.Conn, e *protocol.Error) {
	_ = writeFrame(ctx, conn, protocol.SubscribeResponse{OK: false, Code: e.Code, Error: e.Message})
	// The close reason carries the refusal itself: the client keys its
	// dead-token stop on the reason string, and a generic label here would
	// leave it retrying a token that can never work.
	_ = conn.Close(websocket.StatusPolicyViolation, e.Message)
}

// writeFrame sends one JSON text frame under a write deadline.
func writeFrame(ctx context.Context, conn *websocket.Conn, v any) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, v)
}
