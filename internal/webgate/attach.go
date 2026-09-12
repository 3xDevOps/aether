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
		Follow:   req.Follow,
		Resume:   req.Resume,
		Cursor:   req.Cursor,
		Framed:   true,
	})
	if err == nil && ack.OK && !ack.Framed {
		err = errors.New("server does not support ordered terminal snapshots; update aether-server")
		ack = protocol.AttachResponse{Code: protocol.CodeInternal, Error: err.Error()}
	}
	if err != nil || !ack.OK {
		if term != nil {
			_ = term.Close()
		}
		if ack.Code == 0 && err != nil {
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

	// A read-only attach's input is dropped rather than refused. Its
	// resizes still travel: the session decides whether a mirror's size
	// counts, and a lone one's does.
	if err := s.pumpTerminal(term, allowWrite, true); err != nil {
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

// pumpTerminal bridges the socket and a terminal: terminal output records are
// decoded in order, binary records go out as binary frames, and geometry
// records become JSON controls before the next record is read. Client input
// and resize controls are handled concurrently.
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

	reader := &protocol.TerminalReader{Reader: term}
	buf := make([]byte, 32<<10)
	for {
		n, size, err := reader.Read(buf)
		if size != [2]uint{} {
			if s.WriteJSON(protocol.DashAttachControl{
				Type: protocol.DashAttachGeometry,
				Cols: size[0],
				Rows: size[1],
			}) != nil {
				return nil
			}
		}
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
