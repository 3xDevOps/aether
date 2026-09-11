package webgate

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/version"
)

// MaxRequestBody bounds ordinary API request bodies. Image uploads get a
// separate cap below because an 8 MiB decoded payload is about 11.2 MiB
// in base64 plus JSON framing.
const MaxRequestBody = 1 << 20

const maxTerminalImageRequestBody = 12 << 20

func requestBodyLimit(method string) int64 {
	if method == protocol.MethodTerminalImage {
		return maxTerminalImageRequestBody
	}
	return MaxRequestBody
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
	backend, ok := g.authorize(w, r, false)
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
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestBodyLimit(r.PathValue("method"))))
	if err != nil {
		WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeParse, Message: "read body: " + err.Error()})
		return
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

// handlePatch serves GET /api/v1/run/{run}/patch by calling run.patch;
// protocol.RunPatchResult's JSON tags match what the SPA decodes, so the
// result bytes pass through verbatim. The optional from and to query
// parameters name diff-snapshot trees and render that interval instead of
// the run's whole diff.
func (g *Gateway) handlePatch(w http.ResponseWriter, r *http.Request) {
	backend, ok := g.authorize(w, r, false)
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
	backend, ok := g.authorize(w, r, false)
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
	if _, ok := g.authorize(w, r, false); !ok {
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
