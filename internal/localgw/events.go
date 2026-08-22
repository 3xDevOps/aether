package localgw

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// wsWriteTimeout bounds one frame write, so a client that stopped
	// reading cannot pin a handler.
	wsWriteTimeout = 10 * time.Second
	// wsReadLimit bounds one client frame: subscribe headers, attach
	// headers, and terminal input are all small.
	wsReadLimit = 64 << 10
	// readHeaderTimeout bounds how long a client may take to send its
	// header frame after the socket opens.
	readHeaderTimeout = 10 * time.Second
	// statusStreamEnded (1012, service restart) tells a client the SSH
	// event stream ended for any reason: connection drop, server
	// shutdown, or a backlog drop. The wire cannot distinguish them, so
	// the SPA takes its jittered-backoff reconnect path rather than the
	// immediate 4000 resubscribe; replay with after_seq still recovers a
	// true backlog drop, just a beat slower.
	statusStreamEnded = websocket.StatusServiceRestart
	// statusDirtyExit tells a shell client the remote command ended with
	// a nonzero exit status, distinct from a clean 1000 close.
	statusDirtyExit = 4001
	// defaultCols/defaultRows are the geometry of a terminal whose header
	// carries none.
	defaultCols = 80
	defaultRows = 24
)

// handleEvents serves GET /ws/events: the SSH event subsystem's
// subscription semantics, replay cursor included, over a WebSocket.
func (g *Gateway) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r, true) {
		g.deny(w)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var req protocol.SubscribeRequest
	readCtx, readDone := context.WithTimeout(ctx, readHeaderTimeout)
	rerr := wsjson.Read(readCtx, conn, &req)
	readDone()
	if rerr != nil {
		return
	}

	stream, err := g.cfg.Backend.Events(req)
	if err != nil {
		var perr *protocol.Error
		if !errors.As(err, &perr) {
			perr = &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
		}
		_ = writeFrame(ctx, conn, protocol.SubscribeResponse{OK: false, Code: perr.Code, Error: perr.Message})
		// The close reason carries the refusal itself, matching the
		// remote dashboard's subscribe refusal.
		_ = conn.Close(websocket.StatusPolicyViolation, perr.Message)
		return
	}
	defer func() { _ = stream.Close() }()
	if writeFrame(ctx, conn, protocol.SubscribeResponse{OK: true}) != nil {
		return
	}

	// Everything after the header is discarded - a client keepalive must
	// not tear the stream down - and the read loop noticing the peer go
	// away is what closes the SSH stream and ends the pump.
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				_ = stream.Close()
				return
			}
		}
	}()

	// The stream ends when the subscriber's backlog dropped, but also
	// when the shared SSH connection dies or the server shuts down; the
	// wire cannot tell these apart. Report all of them as 1012 so the
	// SPA resubscribes with backoff instead of hot-looping - a true
	// backlog drop also resumes via replay/after_seq, just a beat
	// slower.
	br := bufio.NewReader(stream)
	for {
		line, err := protocol.ReadLine(br)
		if err != nil {
			_ = conn.Close(statusStreamEnded, "event stream ended; resubscribe with after_seq")
			return
		}
		if writeText(ctx, conn, line) != nil {
			return
		}
	}
}

// writeFrame sends one JSON text frame under a write deadline.
func writeFrame(ctx context.Context, conn *websocket.Conn, v any) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, v)
}

// writeText sends pre-encoded JSON as one text frame under a write deadline.
func writeText(ctx context.Context, conn *websocket.Conn, line []byte) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, line)
}
