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

// AssistantBackend opens the caller's assistant stream. Both shipped
// backends implement it alongside Backend.
type AssistantBackend interface {
	Assistant(context.Context, protocol.AssistantStreamRequest) (io.ReadCloser, protocol.ACPStreamResponse, error)
}

// handleAssistant serves GET /ws/assistant: the header and ack as JSON text
// frames, then one text frame per protocol.AssistantFrame. The client sends
// nothing after the header. Every end other than a revocation closes 1012,
// and the client resubscribes from its last seq.
func (g *Gateway) handleAssistant(w http.ResponseWriter, r *http.Request) {
	s, ok := g.Accept(w, r)
	if !ok {
		return
	}
	defer s.Close()
	var req protocol.AssistantStreamRequest
	if s.ReadHeader(&req) != nil {
		return
	}
	backend, ok := s.Backend.(AssistantBackend)
	if !ok {
		_ = s.WriteJSON(protocol.ACPStreamResponse{Code: protocol.CodeUnavailable, Error: "assistant streams unavailable"})
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "assistant refused")
		return
	}
	stream, ack, err := backend.Assistant(s.Ctx, req)
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
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "assistant refused")
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
			if _, _, err := s.Conn.Read(s.Ctx); err != nil {
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
