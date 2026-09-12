package webgate

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const recordingContentType = "application/x-asciicast"

// handleRecording serves a finite asciicast snapshot. Unlike the WebSocket
// attach route this never sends input or resize messages, and it does not
// install a live PTY client: Backend.Attach's Recording branch owns the
// authenticated run lookup and returns a read-only stream.
func (g *Gateway) handleRecording(w http.ResponseWriter, r *http.Request) {
	backend, ok := g.Authorize(w, r, false)
	if !ok {
		return
	}
	term, ack, err := backend.Attach(r.Context(), protocol.AttachRequest{
		RunID:     r.PathValue("run"),
		ReadOnly:  true,
		Recording: true,
		Follow:    true,
	})
	if err != nil || !ack.OK || !ack.Recording {
		if term != nil {
			_ = term.Close()
		}
		if err == nil && ack.OK && !ack.Recording {
			WriteError(w, http.StatusInternalServerError, &protocol.Error{
				Code:    protocol.CodeInternal,
				Message: "server did not acknowledge a recording; update aether-server",
			})
			return
		}
		code := ack.Code
		message := ack.Error
		if code == 0 {
			code = protocol.CodeInternal
		}
		if message == "" {
			message = "recording refused"
			if err != nil {
				message = err.Error()
			}
		}
		WriteError(w, StatusFor(code), &protocol.Error{Code: code, Message: message})
		return
	}
	if term == nil {
		WriteError(w, http.StatusInternalServerError, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "recording attach returned no stream",
		})
		return
	}
	defer func() { _ = term.Close() }()

	// A server-side stream may be backed by a file, SSH channel, or another
	// finite transport. Closing it as soon as the request is abandoned keeps
	// all those implementations from retaining descriptors or channels.
	cancelDone := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
			_ = term.Close()
		case <-cancelDone:
		}
	}()
	defer close(cancelDone)

	buf := make([]byte, 32<<10)
	n, readErr := term.Read(buf)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		// No response headers have been sent yet, so an immediate stream
		// failure can still be represented as a normal JSON error rather
		// than a successful response with a truncated first chunk.
		WriteError(w, http.StatusInternalServerError, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: fmt.Sprintf("recording stream: %v", readErr),
		})
		return
	}
	if n == 0 && readErr != nil {
		WriteError(w, http.StatusInternalServerError, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: fmt.Sprintf("recording stream: %v", readErr),
		})
		return
	}
	w.Header().Set("Content-Type", recordingContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if n > 0 {
		if _, writeErr := w.Write(buf[:n]); writeErr != nil {
			return
		}
	}
	if readErr != nil {
		return
	}
	for {
		n, readErr = term.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				slog.Warn("webgate: recording stream failed", "run", r.PathValue("run"), "error", readErr)
				// Returning would finish a successful chunked response.
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}
