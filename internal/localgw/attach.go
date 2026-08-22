package localgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// handleAttach serves GET /ws/attach/{run}: terminal output as binary
// frames, client input and resizes as JSON control frames. The attach is
// a read-only mirror unless the header asks for write.
func (g *Gateway) handleAttach(w http.ResponseWriter, r *http.Request) {
	if !g.authorized(r, true) {
		g.deny(w)
		return
	}
	run := r.PathValue("run")
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
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

	term, ack, err := g.cfg.Backend.Attach(protocol.AttachRequest{
		RunID:    run,
		ReadOnly: !req.Write,
		Cols:     cols,
		Rows:     rows,
	})
	if err != nil {
		if !ack.OK && ack.Code == 0 {
			ack = protocol.AttachResponse{OK: false, Code: protocol.CodeInternal, Error: err.Error()}
		}
		_ = writeFrame(ctx, conn, ack)
		_ = conn.Close(websocket.StatusPolicyViolation, "attach refused")
		return
	}
	defer func() { _ = term.Close() }()
	if writeFrame(ctx, conn, ack) != nil {
		return
	}

	// Mirror semantics match the remote dashboard: a read-only attach's
	// input is dropped rather than refused, and its resizes are ignored.
	err = pumpTerminal(ctx, cancel, conn, term, req.Write, req.Write)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "attach ended")
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// pumpTerminal bridges a WebSocket and a remote terminal: terminal output
// goes out as binary frames; JSON control frames come back as input and
// resizes, honored per the allow flags. It returns the terminal's read
// error, nil on clean EOF. The peer closing the socket closes the
// terminal, which in turn ends the output loop.
func pumpTerminal(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, term cli.Terminal, allowInput, allowResize bool) error {
	go func() {
		defer cancel()
		defer func() { _ = term.Close() }()
		for {
			typ, data, err := conn.Read(ctx)
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
			wctx, wdone := context.WithTimeout(ctx, wsWriteTimeout)
			werr := conn.Write(wctx, websocket.MessageBinary, buf[:n])
			wdone()
			if werr != nil {
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
