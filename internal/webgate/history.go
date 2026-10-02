package webgate

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const maxHistoryFormBody = 8 << 10

// handleHistoryForm is the native-download fallback for browsers that cannot
// stream a fetch response into a save-file writer. The bearer is in the
// bounded POST body, never the URL; the cloned request then takes the exact
// same Origin and authorizer path as the GET handler.
func (g *Gateway) handleHistoryForm(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		WriteError(w, http.StatusUnsupportedMediaType, &protocol.Error{
			Code:    protocol.CodeInvalidRequest,
			Message: "history download form must be application/x-www-form-urlencoded",
		})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHistoryFormBody))
	if err != nil {
		WriteError(w, http.StatusBadRequest, &protocol.Error{
			Code:    protocol.CodeParse,
			Message: "read history download form: " + err.Error(),
		})
		return
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		WriteError(w, http.StatusBadRequest, &protocol.Error{
			Code:    protocol.CodeParse,
			Message: "history download form is not valid URL encoding",
		})
		return
	}
	tokenValues, hasToken := values["token"]
	if (len(values) > 0 && !hasToken) || len(values) > 1 || (hasToken && len(tokenValues) != 1) {
		WriteError(w, http.StatusBadRequest, &protocol.Error{
			Code:    protocol.CodeInvalidParams,
			Message: "history download form accepts only one token",
		})
		return
	}
	clone := r.Clone(r.Context())
	clone.Body = http.NoBody
	if hasToken && tokenValues[0] != "" {
		if strings.ContainsAny(tokenValues[0], "\r\n") {
			WriteError(w, http.StatusBadRequest, &protocol.Error{
				Code:    protocol.CodeInvalidParams,
				Message: "history download token is invalid",
			})
			return
		}
		clone.Header.Set("Authorization", "Bearer "+tokenValues[0])
	}
	g.handleHistory(w, clone)
}

// handleHistory serves the complete retained transcript for one run. The
// attach is deliberately read-only and follows the session's geometry, so it
// never acquires the PTY lease or changes the interactive terminal. Replay is
// a byte count in the attach acknowledgement; stopping at that boundary is
// what keeps a live session's output tail from turning this finite download
// into a live stream.
func (g *Gateway) handleHistory(w http.ResponseWriter, r *http.Request) {
	backend, ok := g.Authorize(w, r, false)
	if !ok {
		return
	}

	term, ack, err := backend.Attach(r.Context(), protocol.AttachRequest{
		RunID: r.PathValue("run"),
		// History must not become a writer, even if a future client adds
		// interactive headers of its own.
		ReadOnly: true,
		Follow:   true,
		Framed:   true,
		Screen:   false,
	})
	var closeOnce sync.Once
	closeTerm := func() {
		if term != nil {
			closeOnce.Do(func() { _ = term.Close() })
		}
	}
	defer closeTerm()
	if err != nil || !ack.OK {
		writeHistoryAttachError(w, ack, err)
		return
	}
	if !ack.Framed {
		writeHistoryAttachError(w, ack, errors.New("server does not support ordered terminal history; update aether-server"))
		return
	}
	if term == nil {
		writeHistoryAttachError(w, ack, errors.New("terminal history attach returned no stream"))
		return
	}
	if ack.Replay < 0 {
		writeHistoryAttachError(w, ack, errors.New("server returned a negative terminal history length"))
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+historyFilename(r.PathValue("run"))+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(ack.Replay))
	w.WriteHeader(http.StatusOK)

	// A client disappearing while TerminalReader is blocked in Read must close
	// the SSH/PTY stream too. The request context is the only cancellation
	// signal available to an ordinary HTTP response writer.
	stop := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
			closeTerm()
		case <-stop:
		}
	}()
	defer close(stop)

	reader := &protocol.TerminalReader{Reader: term}
	buf := make([]byte, 32<<10)
	remaining := ack.Replay
	flusher, canFlush := w.(http.Flusher)
	for remaining > 0 {
		chunk := buf
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		n, _, readErr := reader.Read(chunk)
		if n > 0 {
			written, writeErr := w.Write(chunk[:n])
			if writeErr != nil || written != n {
				return
			}
			remaining -= n
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			// A complete final read is valid even when the underlying reader
			// reports EOF alongside its last bytes. Any short replay is a
			// transport failure; headers are already committed, so there is no
			// useful JSON error to append to the ANSI archive.
			return
		}
	}
	// Explicitly fence the stream at the acknowledged replay boundary. In
	// particular, this returns for a running session rather than waiting for
	// the next live byte.
	closeTerm()
}

func writeHistoryAttachError(w http.ResponseWriter, ack protocol.AttachResponse, err error) {
	if ack.Code != 0 || ack.Error != "" {
		code := ack.Code
		if code == 0 {
			code = protocol.CodeInternal
		}
		message := ack.Error
		if message == "" && err != nil {
			message = err.Error()
		}
		WriteError(w, StatusFor(code), &protocol.Error{Code: code, Message: message})
		return
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		WriteError(w, StatusFor(perr.Code), perr)
		return
	}
	if err == nil {
		err = errors.New("terminal history attach failed")
	}
	WriteError(w, http.StatusInternalServerError, &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()})
}

func historyFilename(run string) string {
	var safe strings.Builder
	for _, r := range run {
		if safe.Len() >= 96 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			safe.WriteRune(r)
		} else {
			safe.WriteByte('_')
		}
	}
	if safe.Len() == 0 || safe.String() == "." || safe.String() == ".." {
		return "terminal-history.ansi"
	}
	return "terminal-history-" + safe.String() + ".ansi"
}
