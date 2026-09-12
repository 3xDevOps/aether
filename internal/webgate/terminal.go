package webgate

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/protocol"
)

var terminalTabName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// handleTerminal serves GET /ws/terminal?tab=<name>. Output is sent as
// binary frames, while input and window changes use the attach control
// JSON frames. The tab is validated before a stream is opened so a
// malformed browser URL cannot create a server-side tab.
func (g *Gateway) handleTerminal(w http.ResponseWriter, r *http.Request) {
	s, ok := g.Accept(w, r)
	if !ok {
		return
	}
	defer s.Close()
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "main"
	}
	if !terminalTabName.MatchString(tab) {
		_ = s.WriteJSON(protocol.TerminalResponse{Code: protocol.CodeInvalidParams, Error: "invalid terminal tab"})
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "terminal refused")
		return
	}

	var req protocol.DashAttachRequest
	if s.ReadHeader(&req) != nil {
		return
	}
	cols, rows := req.Cols, req.Rows
	if cols == 0 || rows == 0 {
		cols, rows = defaultCols, defaultRows
	}
	term, ack, err := s.Backend.Terminal(s.Ctx, protocol.TerminalRequest{
		Tab:    tab,
		Cols:   cols,
		Rows:   rows,
		Follow: req.Follow,
		Framed: true,
	})
	if err == nil && ack.OK && !ack.Framed {
		err = errors.New("server does not support ordered terminal snapshots; update aether-server")
		ack = protocol.TerminalResponse{Code: protocol.CodeInternal, Error: err.Error()}
	}
	if err != nil || !ack.OK {
		if term != nil {
			_ = term.Close()
		}
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
		_ = s.WriteJSON(ack)
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "terminal refused")
		return
	}
	defer func() { _ = term.Close() }()
	if s.WriteJSON(ack) != nil {
		return
	}
	if err := s.pumpTerminal(term, true, true); err != nil {
		_ = s.Conn.Close(attachEndClose(err))
		return
	}
	_ = s.Conn.Close(websocket.StatusNormalClosure, "")
}
