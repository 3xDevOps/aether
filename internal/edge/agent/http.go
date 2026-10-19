package edgeagent

import (
	"context"
	"io"
	"net"
	"net/http"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// serveHTTP serves the requests of one http connection with Config.HTTP,
// one after another, until either side closes it. net/http interrupts a
// read by moving its deadline into the past, which closes a
// websocket.NetConn, so the server reads and writes a pipe that is
// copied to and from rc.
func (a *Agent) serveHTTP(ctx context.Context, rc *relayConn) {
	conn, bridge := net.Pipe()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(bridge, rc)
		_ = bridge.Close()
	}()
	go func() {
		_, _ = io.Copy(rc, bridge)
		_ = rc.Close()
		close(done)
	}()
	srv := &http.Server{
		Handler:           a.cfg.HTTP,
		ReadHeaderTimeout: handshakeTimeout,
		IdleTimeout:       2 * edgeproto.HTTPIdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	_ = srv.Serve(&oneConn{conn: conn, addr: conn.LocalAddr(), done: done})
}

// oneConn accepts conn, then nothing: its next Accept returns once done
// is closed, which ends Serve with the connection.
type oneConn struct {
	conn net.Conn
	addr net.Addr
	done <-chan struct{}
}

func (l *oneConn) Accept() (net.Conn, error) {
	if c := l.conn; c != nil {
		l.conn = nil
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConn) Close() error   { return nil }
func (l *oneConn) Addr() net.Addr { return l.addr }
