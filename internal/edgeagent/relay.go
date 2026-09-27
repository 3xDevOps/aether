package edgeagent

import (
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
	"unicode"

	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/coder/websocket"
)

// open answers one open: it verifies an ssh open's grant, takes a
// connection slot, and attaches the data socket in the background. Every
// open gets an open_result.
func (a *Agent) open(s *session, m edgeproto.Open) {
	var grant edgeproto.Grant
	if m.Kind == edgeproto.KindSSH {
		now := time.Now()
		g, err := edgeproto.VerifyGrant(s.edgeKey, m.Grant,
			edgeproto.GrantScope{ServerID: a.serverID, ConnID: m.ConnID, Kind: edgeproto.KindSSH}, now)
		if err == nil {
			err = a.firstUse(m.ConnID, now)
		}
		if err != nil {
			slog.Warn("edge: refused relayed connection", "conn", m.ConnID, "client", m.ClientAddr, "error", err)
			a.reply(s, edgeproto.OpenResult{ConnID: m.ConnID, Error: replyText(err)})
			return
		}
		grant = g
	}
	select {
	case a.slots <- struct{}{}:
	default:
		a.reply(s, edgeproto.OpenResult{ConnID: m.ConnID, Error: string(edgeproto.RefusalConnLimit)})
		return
	}
	a.reply(s, edgeproto.OpenResult{ConnID: m.ConnID})
	go a.attach(m, grant)
}

func (a *Agent) reply(s *session, m edgeproto.Message) {
	if err := s.send(m); err != nil {
		slog.Warn("edge: reply", "error", err)
	}
}

// attach dials the data socket of one open and hands the connection to
// sshd or to the web listener. It holds the slot open took until the
// connection closes.
func (a *Agent) attach(m edgeproto.Open, grant edgeproto.Grant) {
	a.mu.Lock()
	ctx := a.runCtx
	a.mu.Unlock()
	header := http.Header{"Authorization": {"Bearer " + m.Ticket}}
	c, cancel, err := dial(ctx, a.origin+edgeproto.DataPath(m.ConnID), header, a.attachDeadline)
	if err != nil {
		<-a.slots
		slog.Warn("edge: attach data socket", "conn", m.ConnID, "error", err)
		return
	}
	rc := &relayConn{
		Conn:     websocket.NetConn(ctx, c, websocket.MessageBinary),
		deviceID: grant.DeviceID,
		key:      grant.DeviceKey,
	}
	rc.release = func() {
		cancel()
		a.mu.Lock()
		delete(a.conns, rc)
		a.mu.Unlock()
		<-a.slots
	}
	a.mu.Lock()
	a.conns[rc] = struct{}{}
	a.mu.Unlock()
	if ctx.Err() != nil {
		// Run returned between the dial and the registration above, so
		// stop never saw this connection.
		_ = rc.Close()
		return
	}
	slog.Info("edge: relayed connection", "conn", m.ConnID, "kind", m.Kind, "client", m.ClientAddr,
		"provider", grant.Account.Provider, "subject", grant.Account.Subject, "device", grant.DeviceID)
	if m.Kind == edgeproto.KindWeb {
		a.web.deliver(rc, a.attachDeadline)
		return
	}
	a.cfg.SSH.ServeEdgeConn(ctx, rc, grant)
	_ = rc.Close()
}

// revokeDevice closes the live connections of a device whose token the
// edge revoked, and tells sshd the device keys they used.
func (a *Agent) revokeDevice(deviceID string) {
	a.mu.Lock()
	var hit []*relayConn
	for rc := range a.conns {
		if rc.deviceID == deviceID {
			hit = append(hit, rc)
		}
	}
	a.mu.Unlock()
	keys := map[string]bool{}
	for _, rc := range hit {
		_ = rc.Close()
		keys[rc.key] = true
	}
	for key := range keys {
		a.cfg.SSH.CloseEdgeDevice(key)
	}
	slog.Info("edge: device token revoked", "device", deviceID, "closed", len(hit))
}

// relayConn is one relayed connection. Its RemoteAddr is never the
// edge-asserted client address.
type relayConn struct {
	net.Conn
	deviceID, key string
	release       func()
	once          sync.Once
}

func (rc *relayConn) Close() error {
	err := rc.Conn.Close()
	rc.once.Do(rc.release)
	return err
}

// webListener hands passed-through dashboard connections to the gateway.
type webListener struct {
	addr   edgeAddr
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newWebListener(origin string) *webListener {
	return &webListener{addr: edgeAddr(origin), conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *webListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *webListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *webListener) Addr() net.Addr { return l.addr }

// deliver waits up to timeout for Accept to take c, and closes it
// otherwise.
func (l *webListener) deliver(c net.Conn, timeout time.Duration) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case l.conns <- c:
	case <-l.closed:
		_ = c.Close()
	case <-t.C:
		_ = c.Close()
	}
}

type edgeAddr string

func (edgeAddr) Network() string  { return "edge" }
func (a edgeAddr) String() string { return string(a) }

// replyText turns err into text the wire accepts in an error field:
// printable and at most 1024 bytes.
func replyText(err error) string {
	var b []rune
	n := 0
	for _, r := range err.Error() {
		if unicode.IsControl(r) {
			r = ' '
		}
		if n += len(string(r)); n > 1024 {
			break
		}
		b = append(b, r)
	}
	return string(b)
}
