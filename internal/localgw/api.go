package localgw

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/version"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// maxRequestBody bounds ordinary API request bodies, matching the remote
// dashboard's limit. Image uploads get a separate cap below because an
// 8 MiB decoded payload is about 11.2 MiB in base64 plus JSON framing.
const maxRequestBody = 1 << 20

const maxTerminalImageRequestBody = 12 << 20

func requestBodyLimit(method string) int64 {
	if method == protocol.MethodTerminalImage {
		return maxTerminalImageRequestBody
	}
	return maxRequestBody
}

// handleAPI serves POST /api/v1/{method}: the path segment is the
// control-channel method name and the body is its params, proxied over
// SSH. Unlike the remote dashboard there is no method allowlist - the SSH
// key on this machine already holds full authority, so the token guards
// the loopback port, not the method set.
func (g *Gateway) handleAPI(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r, false) {
		g.deny(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestBodyLimit(r.PathValue("method"))))
	if err != nil {
		webgate.WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeParse, Message: "read body: " + err.Error()})
		return
	}
	params := json.RawMessage(bytes.TrimSpace(body))
	if len(params) == 0 {
		params = nil
	} else if !json.Valid(params) {
		webgate.WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeParse, Message: "request body is not valid JSON"})
		return
	}
	result, perr := g.cfg.Backend.Call(r.Context(), r.PathValue("method"), params)
	if perr != nil {
		webgate.WriteError(w, webgate.StatusFor(perr.Code), perr)
		return
	}
	writeResult(w, result)
}

// handlePatch serves GET /api/v1/run/{run}/patch by proxying run.patch;
// protocol.RunPatchResult's JSON tags match what the SPA decodes, so the
// result bytes pass through verbatim. The optional from and to query
// parameters name diff-snapshot trees and render that interval instead of
// the run's whole diff.
func (g *Gateway) handlePatch(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r, false) {
		g.deny(w)
		return
	}
	query := r.URL.Query()
	params, err := json.Marshal(protocol.RunPatchParams{
		RunID: r.PathValue("run"),
		From:  query.Get("from"),
		To:    query.Get("to"),
	})
	if err != nil {
		webgate.WriteError(w, http.StatusBadRequest, &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()})
		return
	}
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodRunPatch, params)
	if perr != nil {
		webgate.WriteError(w, webgate.StatusFor(perr.Code), perr)
		return
	}
	writeResult(w, result)
}

// handleDisk serves GET /api/v1/disk by proxying server.disk verbatim.
func (g *Gateway) handleDisk(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r, false) {
		g.deny(w)
		return
	}
	result, perr := g.cfg.Backend.Call(r.Context(), protocol.MethodServerDisk, nil)
	if perr != nil {
		webgate.WriteError(w, webgate.StatusFor(perr.Code), perr)
		return
	}
	writeResult(w, result)
}

// handleCapabilities serves GET /api/v1/capabilities: this gateway
// forwards every control-channel method ("*") and adds the local verbs.
func (g *Gateway) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r, false) {
		g.deny(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protocol.GatewayCapabilities{
		Gateway: "local",
		Methods: []string{"*"},
		WS:      []string{"events", "attach", "terminal", "envscan"},
		Local:   localVerbs,
		Version: version.Version,
		Commit:  version.Commit,
	})
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
