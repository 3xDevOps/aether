package webgate

import (
	"bufio"
	"errors"
	"net/http"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// statusStreamEnded (1012, service restart) tells a client the event
// stream ended for any reason: connection drop, server shutdown, or a
// backlog drop. The wire cannot distinguish them, so the SPA takes its
// jittered-backoff reconnect path rather than the immediate 4000
// resubscribe; replay with after_seq still recovers a true backlog drop,
// just a beat slower.
const statusStreamEnded = websocket.StatusServiceRestart

// handleEvents serves GET /ws/events: the events subsystem's subscription
// semantics, replay cursor included, over a WebSocket.
func (g *Gateway) handleEvents(w http.ResponseWriter, r *http.Request) {
	s, ok := g.Accept(w, r)
	if !ok {
		return
	}
	defer s.Close()
	var req protocol.SubscribeRequest
	if s.ReadHeader(&req) != nil {
		return
	}

	stream, err := s.Backend.Events(s.Ctx, req)
	if err != nil {
		var perr *protocol.Error
		if !errors.As(err, &perr) {
			perr = &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
		}
		_ = s.WriteJSON(protocol.SubscribeResponse{OK: false, Code: perr.Code, Error: perr.Message})
		// The close reason carries the refusal itself.
		_ = s.Conn.Close(websocket.StatusPolicyViolation, perr.Message)
		return
	}
	defer func() { _ = stream.Close() }()
	if s.WriteJSON(protocol.SubscribeResponse{OK: true}) != nil {
		return
	}

	// Everything after the header is discarded - a client keepalive must
	// not tear the stream down - and the read loop noticing the peer go
	// away is what closes the stream and ends the pump.
	go func() {
		defer s.cancel()
		for {
			if _, _, err := s.Conn.Read(s.Ctx); err != nil {
				_ = stream.Close()
				return
			}
		}
	}()

	// The stream ends when the subscriber's backlog dropped, but also
	// when the transport dies or the server shuts down; the wire cannot
	// tell these apart. Report all of them as 1012 so the SPA
	// resubscribes with backoff instead of hot-looping - a true backlog
	// drop also resumes via replay/after_seq, just a beat slower.
	br := bufio.NewReader(stream)
	for {
		line, err := protocol.ReadLine(br)
		if err != nil {
			_ = s.Conn.Close(statusStreamEnded, "event stream ended; resubscribe with after_seq")
			return
		}
		if s.write(websocket.MessageText, line) != nil {
			return
		}
	}
}
