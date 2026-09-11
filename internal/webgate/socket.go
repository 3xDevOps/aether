package webgate

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	// wsWriteTimeout bounds one frame write, so a client that stopped
	// reading cannot pin a handler.
	wsWriteTimeout = 10 * time.Second
	// wsReadLimit bounds one client frame: subscribe headers, attach
	// headers, and terminal input are all small.
	wsReadLimit = 64 << 10
	// readHeaderTimeout bounds how long a client may take to send its
	// header frame after the socket opens.
	readHeaderTimeout = 10 * time.Second
	// pingInterval is how often a live socket is pinged. A phone that
	// changed networks or went to sleep leaves a half-open TCP
	// connection that reads as live on both ends; the ping turns that
	// into a close within pingInterval plus wsWriteTimeout, which is what
	// releases its PTY client and stops it clamping the geometry.
	pingInterval = 30 * time.Second
)

// Socket is one accepted WebSocket. Ctx is canceled when the peer goes
// away, the gateway closes, or a ping goes unanswered; every read and
// write derives from it.
type Socket struct {
	Conn    *websocket.Conn
	Backend Backend
	Ctx     context.Context
	cancel  context.CancelFunc
	release func()
}

// Accept runs the prologue every WebSocket shares: authorize the
// handshake, upgrade, cap frame size, register the handler so Close can
// end it, and start the keepalive pings. A false result means the
// response has already been written. The caller must Close the socket.
func (g *Gateway) Accept(w http.ResponseWriter, r *http.Request) (*Socket, bool) {
	backend, ok := g.authorize(w, r, true)
	if !ok {
		return nil, false
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, false
	}
	if !g.beginHandler(conn) {
		_ = conn.Close(websocket.StatusGoingAway, "gateway closing")
		return nil, false
	}
	conn.SetReadLimit(wsReadLimit)
	ctx, cancel := context.WithCancel(g.ctx)
	stop := context.AfterFunc(r.Context(), cancel)
	s := &Socket{Conn: conn, Backend: backend, Ctx: ctx, cancel: cancel}
	s.release = func() {
		stop()
		g.endHandler(conn)
	}
	go s.keepAlive()
	return s, true
}

// keepAlive pings until the socket ends; a ping the peer does not answer
// within the write timeout closes the socket, which ends the handler's
// reads and writes with it.
func (s *Socket) keepAlive() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.Ctx.Done():
			return
		case <-ticker.C:
			ctx, done := context.WithTimeout(s.Ctx, wsWriteTimeout)
			err := s.Conn.Ping(ctx)
			done()
			if err != nil {
				s.cancel()
				_ = s.Conn.CloseNow()
				return
			}
		}
	}
}

// Close ends the socket and releases its handler slot.
func (s *Socket) Close() {
	s.cancel()
	_ = s.Conn.CloseNow()
	s.release()
}

// ReadHeader decodes the client's one JSON header frame, which must
// arrive within the header timeout.
func (s *Socket) ReadHeader(v any) error {
	ctx, done := context.WithTimeout(s.Ctx, readHeaderTimeout)
	defer done()
	return wsjson.Read(ctx, s.Conn, v)
}

// WriteJSON sends one JSON text frame under the write timeout.
func (s *Socket) WriteJSON(v any) error {
	ctx, done := context.WithTimeout(s.Ctx, wsWriteTimeout)
	defer done()
	return wsjson.Write(ctx, s.Conn, v)
}

// write sends one frame of the given type under the write timeout.
func (s *Socket) write(typ websocket.MessageType, p []byte) error {
	ctx, done := context.WithTimeout(s.Ctx, wsWriteTimeout)
	defer done()
	return s.Conn.Write(ctx, typ, p)
}
