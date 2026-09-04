package localgw

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/protocol"
)

var terminalTabName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// handleTerminal serves GET /ws/terminal?tab=<name>. Output is sent as
// binary frames, while input and window changes use the attach control JSON
// frames. The gateway validates the tab before opening a remote stream so a
// malformed browser URL cannot create a server-side tab.
func (g *Gateway) handleTerminal(w http.ResponseWriter, r *http.Request) {
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
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "main"
	}
	if !terminalTabName.MatchString(tab) {
		_ = writeFrame(ctx, conn, protocol.TerminalResponse{Code: protocol.CodeInvalidParams, Error: "invalid terminal tab"})
		_ = conn.Close(websocket.StatusPolicyViolation, "terminal refused")
		return
	}

	var req protocol.DashAttachRequest
	readCtx, readDone := context.WithTimeout(ctx, readHeaderTimeout)
	err = wsjson.Read(readCtx, conn, &req)
	readDone()
	if err != nil {
		return
	}
	cols, rows := req.Cols, req.Rows
	if cols == 0 || rows == 0 {
		cols, rows = defaultCols, defaultRows
	}
	term, ack, err := g.cfg.Backend.Terminal(protocol.TerminalRequest{Tab: tab, Cols: cols, Rows: rows})
	if err != nil || !ack.OK {
		var perr *protocol.Error
		if ack.Code == 0 {
			if errors.As(err, &perr) {
				ack.Code = perr.Code
			} else {
				ack.Code = protocol.CodeInternal
			}
		}
		if ack.Error == "" && perr != nil {
			ack.Error = perr.Message
		}
		if ack.Error == "" && err != nil {
			ack.Error = err.Error()
		}
		if ack.Error == "" {
			ack.Error = "terminal refused"
		}
		_ = writeFrame(ctx, conn, ack)
		_ = conn.Close(websocket.StatusPolicyViolation, "terminal refused")
		return
	}
	if writeFrame(ctx, conn, ack) != nil {
		return
	}
	if err := pumpTerminal(ctx, cancel, conn, term, true, true); err != nil {
		_ = conn.Close(attachEndClose(err))
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}
