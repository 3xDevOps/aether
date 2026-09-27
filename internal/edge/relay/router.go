package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// Listener returns the listener the edge's own HTTPS server accepts on:
// the connections Serve routes to the edge host, ClientHello included and
// RemoteAddr the real client address.
func (r *Relay) Listener() net.Listener { return r.local }

// Serve accepts connections on the public TLS listener ln and routes each
// by the SNI of its ClientHello, without terminating TLS: the edge host
// goes to Listener, "<server id>.<server domain>" is passed through to
// that server if it is claimed and connected, and anything else is
// closed. An address block (edgeproto.RateLimitKey) holds at most
// maxConnsPerAddress open connections; one more is closed at once. It
// returns when ln is closed.
func (r *Relay) Serve(ln net.Listener) error {
	var backoff time.Duration
	for {
		conn, err := ln.Accept()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			// Like net/http: a full file table must not stop the edge.
			backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
			slog.Warn("relay: accept failed; retrying", "error", err, "in", backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		key := addrKey(conn.RemoteAddr())
		if !r.takeAddrSlot(key) {
			r.countRefusal("too many connections from one address")
			_ = conn.Close()
			continue
		}
		go r.route(&addrConn{Conn: conn, release: func() { r.releaseAddrSlot(key) }})
	}
}

func addrKey(a net.Addr) netip.Prefix {
	ap, _ := netip.ParseAddrPort(a.String()) // an unparsable address counts as the zero block
	return edgeproto.RateLimitKey(ap.Addr())
}

// takeAddrSlot counts one more open connection from key, or reports false
// when key is at maxConnsPerAddress.
func (r *Relay) takeAddrSlot(key netip.Prefix) bool {
	r.addrMu.Lock()
	defer r.addrMu.Unlock()
	if r.addrConns[key] >= r.maxConnsPerAddress {
		return false
	}
	r.addrConns[key]++
	return true
}

func (r *Relay) releaseAddrSlot(key netip.Prefix) {
	r.addrMu.Lock()
	defer r.addrMu.Unlock()
	if r.addrConns[key]--; r.addrConns[key] <= 0 {
		delete(r.addrConns, key)
	}
}

// addrConn gives back its address's slot when it is closed, by the
// router, a splice, or the edge's HTTPS server.
type addrConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *addrConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func (r *Relay) route(conn net.Conn) {
	if err := conn.SetReadDeadline(time.Now().Add(edgeproto.ClientHelloTimeout)); err != nil {
		_ = conn.Close()
		return
	}
	sni, hello, err := peekClientHello(conn)
	if err == nil {
		err = conn.SetReadDeadline(time.Time{})
	}
	if err != nil {
		r.countRefusal("no ClientHello")
		_ = conn.Close()
		return
	}
	replay := &replayConn{Conn: conn, r: io.MultiReader(bytes.NewReader(hello), conn)}
	if strings.EqualFold(sni, r.edgeHost) {
		r.local.push(replay)
		return
	}
	serverID, ok := edgeproto.ServerIDFromHostname(sni, r.domain)
	if !ok {
		r.countRefusal("unknown host")
		_ = conn.Close()
		return
	}
	c, server, err := r.openWeb(serverID, conn.RemoteAddr().String())
	if err != nil {
		r.refusal(err)
		_ = conn.Close()
		return
	}
	r.splice(c, replay, server)
}

// openWeb opens a dashboard passthrough to serverID. The store, not the
// live registration, says whether serverID is still claimed: the
// operator may have removed or blocked it while it stayed connected.
func (r *Relay) openWeb(serverID, remote string) (*relayConn, net.Conn, error) {
	ctx, cancel := context.WithTimeout(r.ctx, directoryTimeout)
	claimed, err := r.dir.Claimed(ctx, serverID)
	cancel()
	if err != nil {
		return nil, nil, err
	}
	if !claimed {
		return nil, nil, edgeproto.RefusalUnknownServer
	}
	return r.open(r.ctx, serverID, edgeproto.KindWeb, edgeproto.Account{}, edgeproto.Device{}, remote)
}

// peekClientHello reads a TLS ClientHello from conn, at most
// edgeproto.MaxClientHelloSize bytes, and returns its SNI and every byte
// it read, so that the connection can be replayed to whoever terminates
// TLS. crypto/tls parses the hello; the handshake is abandoned as soon as
// the hello is parsed.
func peekClientHello(conn net.Conn) (string, []byte, error) {
	var read bytes.Buffer
	var sni string
	parsed := false
	errPeeked := errors.New("ClientHello read")
	peek := &replayConn{Conn: conn, r: io.TeeReader(io.LimitReader(conn, edgeproto.MaxClientHelloSize), &read), discard: true}
	err := tls.Server(peek, &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sni, parsed = hello.ServerName, true
			return nil, errPeeked
		},
	}).Handshake()
	if !parsed {
		return "", nil, fmt.Errorf("relay: read ClientHello: %w", err)
	}
	return sni, read.Bytes(), nil
}

// replayConn reads from r instead of the connection. With discard set,
// writes are dropped: the peeking handshake's alert never reaches the
// client.
type replayConn struct {
	net.Conn
	r       io.Reader
	discard bool
}

func (c *replayConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *replayConn) Write(p []byte) (int, error) {
	if c.discard {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

// localListener hands the connections Serve routes to the edge host to
// the edge's HTTPS server.
type localListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newLocalListener() *localListener {
	return &localListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *localListener) push(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.done:
		_ = c.Close()
	}
}

func (l *localListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *localListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *localListener) Addr() net.Addr { return edgeAddr{} }

type edgeAddr struct{}

func (edgeAddr) Network() string { return "relay" }
func (edgeAddr) String() string  { return "edge" }
