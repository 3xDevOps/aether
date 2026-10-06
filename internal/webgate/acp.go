package webgate

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// ACPBackend opens an enhanced run's session stream. Both shipped backends
// implement it alongside Backend.
type ACPBackend interface {
	ACP(context.Context, protocol.ACPStreamRequest) (io.ReadWriteCloser, protocol.ACPStreamResponse, error)
}

// handleACP serves GET /ws/acp/{run}: the header and ack as JSON text frames,
// then one text frame per protocol.ACPFrame or control frame. Client text
// frames are control and takeover frames. Every end other than a revocation
// closes 1012, and the client resubscribes from its last seq.
func (g *Gateway) handleACP(w http.ResponseWriter, r *http.Request) {
	s, ok := g.Accept(w, r)
	if !ok {
		return
	}
	defer s.Close()
	var req protocol.ACPStreamRequest
	if s.ReadHeader(&req) != nil {
		return
	}
	req.RunID = r.PathValue("run")
	backend, ok := s.Backend.(ACPBackend)
	if !ok {
		_ = s.WriteJSON(protocol.ACPStreamResponse{Code: protocol.CodeUnavailable, Error: "enhanced session streams unavailable"})
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "acp refused")
		return
	}
	stream, ack, err := backend.ACP(s.Ctx, req)
	if err != nil || !ack.OK {
		if stream != nil {
			_ = stream.Close()
		}
		if ack.Code == 0 {
			ack = protocol.ACPStreamResponse{Code: protocol.CodeInternal, Error: err.Error()}
			var perr *protocol.Error
			if errors.As(err, &perr) {
				ack.Code, ack.Error = perr.Code, perr.Message
			}
		}
		_ = s.WriteJSON(ack)
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "acp refused")
		return
	}
	defer func() { _ = stream.Close() }()
	if s.WriteJSON(ack) != nil {
		return
	}
	go func() {
		defer s.cancel()
		defer func() { _ = stream.Close() }()
		for {
			typ, data, err := s.Conn.Read(s.Ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			if _, err := stream.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}()
	lines := bufio.NewReader(stream)
	for {
		line, err := protocol.ReadLine(lines)
		if err != nil {
			_ = s.Conn.Close(acpEndClose(err))
			return
		}
		if s.write(websocket.MessageText, line) != nil {
			return
		}
	}
}

func acpEndClose(err error) (websocket.StatusCode, string) {
	var exit *protocol.RemoteExitError
	if errors.As(err, &exit) {
		switch exit.Status {
		case protocol.AttachExitSteerRevoked:
			return websocket.StatusPolicyViolation, "steer permission withdrawn"
		case protocol.AttachExitMembershipRevoked:
			return websocket.StatusPolicyViolation, "membership withdrawn"
		}
	}
	return websocket.StatusServiceRestart, "session stream ended; resubscribe with after_seq"
}
