package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	// attachAckGrace is how long the handler waits for the PTY host to
	// fail fast before acknowledging an attach that produced no output
	// yet - an idle terminal must still ack promptly.
	attachAckGrace = 200 * time.Millisecond
	// defaultCols/defaultRows are the geometry of an attach whose header
	// carries none.
	defaultCols = 80
	defaultRows = 24
)

// handleAttach serves GET /ws/attach/{run}: terminal output as binary
// frames, client input and resizes as JSON control frames. The attach is
// a read-only mirror unless the header asks for write and the member
// holds the steer capability on that run.
func (g *Gateway) handleAttach(w http.ResponseWriter, r *http.Request) {
	member, token, ok := g.authenticate(w, r, true)
	if !ok {
		return
	}
	if !g.beginHandler() {
		return
	}
	defer g.wg.Done()
	run := domain.RunID(r.PathValue("run"))
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	if !g.trackConn(conn) {
		return
	}
	defer g.untrackConn(conn)
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// The socket holds a token from the moment it is accepted, so the
	// authorization watch starts before the header arrives - a parked,
	// headerless socket must not survive a revoke. It watches without a
	// write requirement until the gates below establish one.
	preCtx, preDone := context.WithCancel(ctx)
	defer preDone()
	go g.watchAuthorization(preCtx, cancel, conn, token, member, "")

	var req protocol.DashAttachRequest
	readCtx, readDone := context.WithTimeout(ctx, readHeaderTimeout)
	err = wsjson.Read(readCtx, conn, &req)
	readDone()
	if err != nil {
		return
	}
	// The watch ticks are snapshots too: re-resolve the token at header
	// time so a revoke between ticks cannot be converted into an attach.
	if _, ok := g.tokens.resolve(token); !ok {
		rejectAttach(ctx, conn, &protocol.Error{Code: protocol.CodeDenied, Message: "dashboard token revoked or expired"})
		return
	}
	if perr := g.cfg.RPC.CheckMember(ctx, member); perr != nil {
		rejectAttach(ctx, conn, perr)
		return
	}
	// The run is resolved through the same run.get handler the SSH
	// transport serves, so an unknown run answers not-found here too.
	params, _ := json.Marshal(protocol.RunIDParams{RunID: string(run)})
	if resp := g.cfg.RPC.Call(ctx, member, protocol.MethodRunGet, params); resp.Error != nil {
		rejectAttach(ctx, conn, resp.Error)
		return
	}
	if req.Write {
		if perr := g.cfg.RPC.CheckSteer(ctx, member, run); perr != nil {
			rejectAttach(ctx, conn, perr)
			return
		}
	}
	// Re-arm the watch with the run and the write flag the gates above just
	// checked: the same authority has to keep holding for as long as the
	// socket does.
	watched := domain.RunID("")
	if req.Write {
		watched = run
	}
	preDone()
	go g.watchAuthorization(ctx, cancel, conn, token, member, watched)

	cols, rows := req.Cols, req.Rows
	if cols == 0 || rows == 0 {
		cols, rows = defaultCols, defaultRows
	}

	resize := make(chan [2]uint, 4)
	pty := newWSPTY(ctx, conn, protocol.AttachResponse{OK: true, Cols: cols, Rows: rows})
	go pty.readLoop(req.Write, resize)

	errCh := make(chan error, 1)
	go func() { errCh <- g.cfg.PTY.Attach(ctx, run, member, cols, rows, !req.Write, pty, resize) }()

	var attachErr error
	returned := false
	select {
	case attachErr = <-errCh:
		returned = true
	case <-time.After(attachAckGrace):
		// The grace timer can race a failing Attach; prefer the error so
		// the client gets ok:false instead of a bogus ack.
		select {
		case attachErr = <-errCh:
			returned = true
		default:
		}
	}
	if returned && attachErr != nil && !pty.acked() {
		rejectAttach(ctx, conn, g.cfg.RPC.WireError(attachErr))
		return
	}
	pty.ack()
	g.cfg.RPC.PublishAttachPresence(ctx, run, member, events.PresenceWatching)
	if !returned {
		attachErr = <-errCh
	}
	g.cfg.RPC.PublishAttachPresence(ctx, run, member, events.PresenceOnline)
	if attachErr != nil {
		_ = conn.Close(websocket.StatusInternalError, "attach ended: "+attachErr.Error())
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func rejectAttach(ctx context.Context, conn *websocket.Conn, e *protocol.Error) {
	_ = writeFrame(ctx, conn, protocol.AttachResponse{OK: false, Code: e.Code, Error: e.Message})
	_ = conn.Close(websocket.StatusPolicyViolation, "attach refused")
}

// wsPTY adapts a WebSocket to the io.ReadWriter the PTY host attaches to:
// output goes out as binary frames, JSON control frames come back as
// input. The ack rides the same lock as the output frames so it can never
// land after the first byte of terminal output.
type wsPTY struct {
	ctx  context.Context
	conn *websocket.Conn
	in   chan []byte

	pending []byte

	ackMsg  protocol.AttachResponse
	ackOnce sync.Once
	acknown atomic.Bool
}

func newWSPTY(ctx context.Context, conn *websocket.Conn, ack protocol.AttachResponse) *wsPTY {
	return &wsPTY{ctx: ctx, conn: conn, in: make(chan []byte), ackMsg: ack}
}

func (c *wsPTY) ack() {
	c.ackOnce.Do(func() {
		_ = writeFrame(c.ctx, c.conn, c.ackMsg)
		c.acknown.Store(true)
	})
}

func (c *wsPTY) acked() bool { return c.acknown.Load() }

func (c *wsPTY) Write(p []byte) (int, error) {
	c.ack()
	ctx, cancel := context.WithTimeout(c.ctx, wsWriteTimeout)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsPTY) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		select {
		case b, ok := <-c.in:
			if !ok {
				return 0, io.EOF
			}
			c.pending = b
		case <-c.ctx.Done():
			return 0, io.EOF
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// readLoop turns client control frames into terminal input and resizes.
// A read-only attach's input is dropped rather than refused: the mirror
// stays open, which is what a viewer's terminal expects.
func (c *wsPTY) readLoop(write bool, resize chan<- [2]uint) {
	defer close(c.in)
	for {
		typ, data, err := c.conn.Read(c.ctx)
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
			if !write || ctl.Data == "" {
				continue
			}
			select {
			case c.in <- []byte(ctl.Data):
			case <-c.ctx.Done():
				return
			}
		case protocol.DashAttachResize:
			if ctl.Cols == 0 || ctl.Rows == 0 {
				continue
			}
			select {
			case resize <- [2]uint{ctl.Cols, ctl.Rows}:
			default:
			}
		}
	}
}
