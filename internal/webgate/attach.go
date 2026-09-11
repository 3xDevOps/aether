package webgate

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/protocol"
)

// defaultCols/defaultRows are the geometry of a terminal whose header
// carries none.
const (
	defaultCols = 80
	defaultRows = 24
)

// handleAttach serves GET /ws/attach/{run}: terminal output as binary
// frames, client input and resizes as JSON control frames. The attach is
// a read-only mirror unless the header asks for write.
func (g *Gateway) handleAttach(w http.ResponseWriter, r *http.Request) {
	s, ok := g.Accept(w, r)
	if !ok {
		return
	}
	defer s.Close()
	var req protocol.DashAttachRequest
	if s.ReadHeader(&req) != nil {
		return
	}
	cols, rows := req.Cols, req.Rows
	if cols == 0 || rows == 0 {
		cols, rows = defaultCols, defaultRows
	}
	shell := r.URL.Query().Get("shell")
	allowWrite := req.Write || shell != ""
	term, ack, err := s.Backend.Attach(s.Ctx, protocol.AttachRequest{
		RunID:    r.PathValue("run"),
		ReadOnly: !allowWrite,
		Cols:     cols,
		Rows:     rows,
		Shell:    shell,
	})
	if err != nil {
		if !ack.OK && ack.Code == 0 {
			ack = protocol.AttachResponse{OK: false, Code: protocol.CodeInternal, Error: err.Error()}
		}
		_ = s.WriteJSON(ack)
		_ = s.Conn.Close(websocket.StatusPolicyViolation, "attach refused")
		return
	}
	defer func() { _ = term.Close() }()
	if s.WriteJSON(ack) != nil {
		return
	}

	// A read-only attach's input is dropped rather than refused, and its
	// resizes are ignored.
	if err := s.pumpTerminal(term, allowWrite, allowWrite); err != nil {
		_ = s.Conn.Close(attachEndClose(err))
		return
	}
	// A clean EOF is the run's terminal session ending (the attach
	// channel's exit-status 0): a finished agent or a drained transcript
	// replay. Name it so the dashboard stops reconnecting instead of
	// looping attach -> EOF -> reattach against a session that is over.
	_ = s.Conn.Close(websocket.StatusNormalClosure, "session ended")
}

// attachEndClose maps a terminal read error to the close frame the
// terminal view reads. The server ends a live attach with a distinct exit
// status when its authorization re-check fails; relayed as 1008 with the
// gate's name, the view downgrades to a mirror or stops reconnecting,
// exactly as it would for a refusal at attach time.
func attachEndClose(err error) (websocket.StatusCode, string) {
	var exit *protocol.RemoteExitError
	if errors.As(err, &exit) {
		switch exit.Status {
		case protocol.AttachExitSteerRevoked:
			return websocket.StatusPolicyViolation, "steer permission withdrawn"
		case protocol.AttachExitMembershipRevoked:
			return websocket.StatusPolicyViolation, "membership withdrawn"
		}
	}
	return websocket.StatusInternalError, "attach ended"
}

// pumpTerminal bridges the socket and a terminal: terminal output goes
// out as binary frames; JSON control frames come back as input and
// resizes, honored per the allow flags. It returns the terminal's read
// error, nil on clean EOF. The peer closing the socket closes the
// terminal, which in turn ends the output loop.
func (s *Socket) pumpTerminal(term Terminal, allowInput, allowResize bool) error {
	go func() {
		defer s.cancel()
		defer func() { _ = term.Close() }()
		for {
			typ, data, err := s.Conn.Read(s.Ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var ctl protocol.DashAttachControl
			if json.Unmarshal(data, &ctl) != nil {
				continue
			}
			switch ctl.Type {
			case protocol.DashAttachInput:
				if !allowInput || ctl.Data == "" {
					continue
				}
				if _, err := term.Write([]byte(ctl.Data)); err != nil {
					return
				}
			case protocol.DashAttachResize:
				if !allowResize || ctl.Cols == 0 || ctl.Rows == 0 {
					continue
				}
				_ = term.Resize(ctl.Cols, ctl.Rows)
			}
		}
	}()

	buf := make([]byte, 32<<10)
	for {
		n, err := term.Read(buf)
		if n > 0 {
			if s.write(websocket.MessageBinary, buf[:n]) != nil {
				return nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
