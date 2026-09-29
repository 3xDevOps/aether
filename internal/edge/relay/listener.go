package relay

import (
	"net"
	"net/netip"
	"sync"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// Listener wraps the public listener ln for the edge's HTTPS server. An
// address block (edgeproto.RateLimitKey) holds at most maxConnsPerAddress
// open connections; one more is closed as soon as it is accepted.
func (r *Relay) Listener(ln net.Listener) net.Listener {
	return &limitListener{Listener: ln, r: r}
}

type limitListener struct {
	net.Listener
	r *Relay
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		key := addrKey(conn.RemoteAddr())
		if l.r.takeAddrSlot(key) {
			return &addrConn{Conn: conn, release: func() { l.r.releaseAddrSlot(key) }}, nil
		}
		l.r.countRefusal("too many connections from one address")
		_ = conn.Close()
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

// addrConn gives back its address's slot when it is closed.
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
