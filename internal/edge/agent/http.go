package edgeagent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// serveHTTP serves the requests of one http connection, opened on s, with
// Config.HTTP, one after another, until either side closes it. Each
// request's context carries the relay origin it came in on (RelayOrigin).
// net/http interrupts a read by moving its deadline into the past, which
// closes a websocket.NetConn, so the server reads and writes a pipe that
// is copied to and from rc.
func (a *Agent) serveHTTP(ctx context.Context, s *session, rc *relayConn) {
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
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin, err := a.relayOrigin(s, r.Header.Get(edgeproto.HeaderRelayOrigin))
			if err != nil {
				http.Error(w, err.Error(), http.StatusMisdirectedRequest)
				return
			}
			r.Header.Del(edgeproto.HeaderRelayOrigin)
			a.cfg.HTTP.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), relayOriginKey{}, origin)))
		}),
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

type relayOriginKey struct{}

// RelayOrigin is the relay origin the edge says the request of ctx came
// in on, empty for a request the agent did not relay.
func RelayOrigin(ctx context.Context) string {
	o, _ := ctx.Value(relayOriginKey{}).(string)
	return o
}

// relayOrigin is the relay origin a request relayed on s came in on:
// header, the edge's HeaderRelayOrigin, when s speaks
// edgeproto.VersionRelayOrigin, else the origin the server dialed. An
// origin s did not announce is refused.
func (a *Agent) relayOrigin(s *session, header string) (string, error) {
	if s.protocol < edgeproto.VersionRelayOrigin || header == "" {
		return a.origin, nil
	}
	if !slices.Contains(s.origins, header) {
		return "", fmt.Errorf("the edge relayed this request from %q, which is not one of the relay origins it announced: %s",
			header, strings.Join(s.origins, ", "))
	}
	return header, nil
}
