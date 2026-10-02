package webgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/version"
)

// MaxRequestBody bounds ordinary API request bodies.
const MaxRequestBody = 1 << 20

const (
	maxTerminalImageRequestBody = 12 << 20
	// Each config import request carries up to 64 MiB decoded in base64 JSON;
	// leave room for encoding and framing without limiting directory size.
	maxConfigImportRequestBody = 96 << 20
	// JSON escaping can expand a supported 64 MiB editor document sixfold;
	// leave room for the remaining request fields while bounding memory.
	maxEditorWriteRequestBody = 6*(64<<20) + MaxRequestBody
)

func requestBodyLimit(method string) int64 {
	switch method {
	case protocol.MethodTerminalImage:
		return maxTerminalImageRequestBody
	case protocol.MethodConfigImport:
		return maxConfigImportRequestBody
	case protocol.MethodFilesWrite, protocol.MethodConfigWrite:
		return maxEditorWriteRequestBody
	default:
		return MaxRequestBody
	}
}

// handleAPI serves POST /api/v1/{method}: the path segment is the
// control-channel method name and the body is its params. There is no
// method allowlist: the backend carries the caller's own authority, and
// every call still passes the server's capability checks.
//
// The body must be declared application/json. A cross-site page can post
// a text/plain body without a CORS preflight, and on the server gateway
// nothing else stands between it and the member's authority; a JSON
// content type forces the preflight, which the gateway never answers.
func (g *Gateway) handleAPI(w http.ResponseWriter, r *http.Request) {
	backend, ok := g.Authorize(w, r, false)
	if !ok {
		return
	}
	if mediaType, _, cerr := mime.ParseMediaType(r.Header.Get("Content-Type")); cerr != nil || mediaType != "application/json" {
		WriteError(w, http.StatusUnsupportedMediaType, &protocol.Error{
			Code:    protocol.CodeInvalidRequest,
			Message: "request body must be application/json",
		})
		return
	}
	var body []byte
	var err error
	if r.PathValue("method") == protocol.MethodConfigImport {
		select {
		case g.configImports <- struct{}{}:
			defer func() { <-g.configImports }()
		default:
			// Do not let net/http drain an unread rejected HTTP/1 body.
			w.Header().Set("Connection", "close")
			WriteError(w, http.StatusServiceUnavailable, &protocol.Error{
				Code: protocol.CodeUnavailable, Message: "configuration import capacity is busy; retry later",
			})
			return
		}
		var status int
		body, status, err = g.readConfigImportBody(w, r)
		if err != nil {
			w.Header().Set("Connection", "close")
			code := protocol.CodeParse
			if status == http.StatusInternalServerError {
				code = protocol.CodeInternal
			}
			WriteError(w, status, &protocol.Error{Code: code, Message: "read body: " + err.Error()})
			return
		}
	} else {
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, requestBodyLimit(r.PathValue("method"))))
		if err != nil {
			WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeParse, Message: "read body: " + err.Error()})
			return
		}
	}
	params := json.RawMessage(bytes.TrimSpace(body))
	if len(params) == 0 {
		params = nil
	} else if !json.Valid(params) {
		WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeParse, Message: "request body is not valid JSON"})
		return
	}
	result, perr := backend.Call(r.Context(), r.PathValue("method"), params)
	if perr != nil {
		WriteError(w, StatusFor(perr.Code), perr)
		return
	}
	writeResult(w, result)
}

// readConfigImportBody uses socket/stream deadlines, not a detached body
// reader: a timeout must unblock the read and release its buffer and slot.
func (g *Gateway) readConfigImportBody(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	controller := http.NewResponseController(w)
	reader := &configImportReader{
		reader:     http.MaxBytesReader(w, r.Body, maxConfigImportRequestBody),
		controller: controller,
		ctx:        r.Context(),
		idle:       g.configImportIdle,
		end:        time.Now().Add(g.configImportDuration),
	}
	if err := controller.SetReadDeadline(reader.deadline()); err != nil {
		// Wrappers must expose Unwrap or SetReadDeadline. An unsupported
		// writer is not permission to accept an unbounded-lifetime upload.
		return nil, http.StatusInternalServerError, err
	}
	canceled := make(chan struct{})
	stop := context.AfterFunc(r.Context(), func() {
		_ = controller.SetReadDeadline(time.Now())
		close(canceled)
	})
	body, err := io.ReadAll(reader)
	if !stop() {
		<-canceled
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrDeadlineExceeded) || r.Context().Err() != nil {
			status = http.StatusRequestTimeout
		}
		return nil, status, err
	}
	// Stop/join cancellation before resetting: a late callback must not
	// poison a keepalive connection's next request.
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return body, http.StatusOK, nil
}

type configImportReader struct {
	reader     io.Reader
	controller *http.ResponseController
	ctx        context.Context
	idle       time.Duration
	end        time.Time
}

func (r *configImportReader) deadline() time.Time {
	deadline := time.Now().Add(r.idle)
	if r.end.Before(deadline) {
		return r.end
	}
	return deadline
}

func (r *configImportReader) Read(p []byte) (int, error) {
	if !time.Now().Before(r.end) {
		return 0, os.ErrDeadlineExceeded
	}
	if err := r.controller.SetReadDeadline(r.deadline()); err != nil {
		return 0, err
	}
	// Check after refreshing so cancellation cannot set an expired
	// deadline that this reader then overwrites before blocking again.
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// handlePatch serves GET /api/v1/run/{run}/patch by calling run.patch;
// protocol.RunPatchResult's JSON tags match what the SPA decodes, so the
// result bytes pass through verbatim. The optional from and to query
// parameters name diff-snapshot trees and render that interval instead of
// the run's whole diff.
func (g *Gateway) handlePatch(w http.ResponseWriter, r *http.Request) {
	backend, ok := g.Authorize(w, r, false)
	if !ok {
		return
	}
	query := r.URL.Query()
	params, err := json.Marshal(protocol.RunPatchParams{
		RunID: r.PathValue("run"),
		From:  query.Get("from"),
		To:    query.Get("to"),
	})
	if err != nil {
		WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()})
		return
	}
	result, perr := backend.Call(r.Context(), protocol.MethodRunPatch, params)
	if perr != nil {
		WriteError(w, StatusFor(perr.Code), perr)
		return
	}
	writeResult(w, result)
}

// handleDisk serves GET /api/v1/disk by calling server.disk verbatim.
func (g *Gateway) handleDisk(w http.ResponseWriter, r *http.Request) {
	backend, ok := g.Authorize(w, r, false)
	if !ok {
		return
	}
	result, perr := backend.Call(r.Context(), protocol.MethodServerDisk, nil)
	if perr != nil {
		WriteError(w, StatusFor(perr.Code), perr)
		return
	}
	writeResult(w, result)
}

// handleCapabilities serves GET /api/v1/capabilities: the composer's
// descriptor, with the parts every gateway shares filled in.
func (g *Gateway) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if _, ok := g.Authorize(w, r, false); !ok {
		return
	}
	caps := g.cfg.Capabilities
	caps.Methods = []string{"*"}
	caps.Version = version.Version
	caps.Commit = version.Commit
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(caps)
}

// writeResult answers 200 with the raw result object; a call that
// returned nothing still answers a JSON object so clients always decode.
func writeResult(w http.ResponseWriter, result json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	if len(result) == 0 {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_, _ = w.Write(result)
}
