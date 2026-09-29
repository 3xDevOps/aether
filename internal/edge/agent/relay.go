package edgeagent

import (
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
	"unicode"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/coder/websocket"
)

// open answers one open: it verifies the grant, takes a connection slot,
// and attaches the data socket in the background. The grant must be of
// the open's kind, so a claim grant never opens a member connection and
// an ssh grant never presents a claim code. Every open gets an
// open_result.
func (a *Agent) open(s *session, m edgeproto.Open) {
	now := time.Now()
	grant, err := edgeproto.VerifyGrant(s.edgeKey, m.Grant,
		edgeproto.GrantScope{Issuer: a.origin, ServerID: a.serverID, ConnID: m.ConnID, Kind: m.Kind}, now)
	if err == nil {
		err = a.firstUse(m.ConnID, now)
	}
	if err != nil {
		slog.Warn("edge: refused relayed connection", "conn", m.ConnID, "client", m.ClientAddr, "error", err)
		a.reply(s, edgeproto.OpenResult{ConnID: m.ConnID, Error: replyText(err)})
		return
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
// sshd. It holds the slot open took until the connection closes.
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
	slog.Info("edge: relayed connection", "conn", m.ConnID, "kind", grant.Kind, "client", m.ClientAddr,
		"provider", grant.Account.Provider, "subject", grant.Account.Subject, "device", grant.DeviceID)
	if grant.Kind == edgeproto.KindClaim {
		a.cfg.SSH.ServeEdgeClaim(ctx, rc, grant, a.claimAttempt(grant))
	} else {
		a.cfg.SSH.ServeEdgeConn(ctx, rc, grant)
	}
	_ = rc.Close()
}

// revokeDevice closes the relayed connections of a device whose token the
// edge revoked. The device keeps its status on this server, so its direct
// connections are unaffected; `aether device revoke` ends those.
func (a *Agent) revokeDevice(deviceID string) {
	a.mu.Lock()
	var hit []*relayConn
	for rc := range a.conns {
		if rc.deviceID == deviceID {
			hit = append(hit, rc)
		}
	}
	a.mu.Unlock()
	for _, rc := range hit {
		_ = rc.Close()
	}
	slog.Info("edge: device token revoked", "device", deviceID, "closed", len(hit))
}

// relayConn is one relayed connection. Its RemoteAddr is never the
// edge-asserted client address.
type relayConn struct {
	net.Conn
	deviceID string
	release  func()
	once     sync.Once
}

func (rc *relayConn) Close() error {
	err := rc.Conn.Close()
	rc.once.Do(rc.release)
	return err
}

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
