package webgate

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/protocol"
)

var terminalTabName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// handleTerminal serves GET /ws/terminal?tab=<name>, or with
// assistant_login=<agent> that agent's sign-in for the assistant. Output is sent as
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
	login := r.URL.Query().Get("assistant_login")
	if !terminalTabName.MatchString(tab) || login != "" && !terminalTabName.MatchString(login) {
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
	// An assistant sign-in may install its adapter first, past a ping.
	s.starting.Store(true)
	term, ack, err := s.Backend.Terminal(s.Ctx, protocol.TerminalRequest{
		Tab:            tab,
		AssistantLogin: login,
		Cols:           cols,
		Rows:           rows,
		Follow:         req.Follow,
		Framed:         true,
	})
	s.starting.Store(false)
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
	if err := s.pumpTerminal(term, true, true, false); err != nil {
		_ = s.Conn.Close(attachEndClose(err))
		return
	}
	_ = s.Conn.Close(websocket.StatusNormalClosure, "")
}
