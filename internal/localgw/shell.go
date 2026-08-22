package localgw

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// handleShell serves GET /ws/shell: an interactive workspace shell with
// the attach socket's frame protocol - binary output, JSON control frames
// for input and resize (always honored here).
func (g *Gateway) handleShell(w http.ResponseWriter, r *http.Request) {
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
	var req protocol.WorkspaceShellRequest
	readCtx, readDone := context.WithTimeout(ctx, readHeaderTimeout)
	err = wsjson.Read(readCtx, conn, &req)
	readDone()
	if err != nil {
		return
	}
	if req.Cols == 0 || req.Rows == 0 {
		req.Cols, req.Rows = defaultCols, defaultRows
	}
	if verr := req.Validate(); verr != nil {
		_ = writeFrame(ctx, conn, protocol.WorkspaceShellResponse{OK: false, Code: protocol.CodeInvalidParams, Error: verr.Error()})
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid shell request")
		return
	}

	term, ack, err := g.cfg.Backend.Shell(req)
	if err != nil {
		if !ack.OK && ack.Code == 0 {
			ack = protocol.WorkspaceShellResponse{OK: false, Code: protocol.CodeInternal, Error: err.Error()}
		}
		_ = writeFrame(ctx, conn, ack)
		_ = conn.Close(websocket.StatusPolicyViolation, "shell refused")
		return
	}
	defer func() { _ = term.Close() }()
	if writeFrame(ctx, conn, ack) != nil {
		return
	}

	err = pumpTerminal(ctx, cancel, conn, term, true, true)
	if err != nil {
		// A nonzero remote exit status surfaces here; 4001 lets the SPA
		// distinguish a dirty exit from a clean 1000 close.
		reason := err.Error()
		if len(reason) > 120 {
			reason = reason[:120]
		}
		_ = conn.Close(statusDirtyExit, reason)
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "shell exited")
}
