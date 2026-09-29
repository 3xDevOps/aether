package relay

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/coder/websocket"
)

// relayConn is one client connection, from the open to the end of its
// splice. Cancelling ctx closes it; the cause is the refusal a waiting
// client receives.
type relayConn struct {
	id       string
	kind     string
	serverID string
	account  edgeproto.Account
	deviceID string
	// reg is the control channel c was admitted on.
	reg      *registration
	result   chan string
	attached chan net.Conn
	ctx      context.Context
	cancel   context.CancelCauseFunc
	// ticketHash and spliced are guarded by Relay.mu. ticketHash is
	// cleared when a data socket presents a ticket, so a ticket is tried
	// once.
	ticketHash string
	spliced    bool
}

// claimGrant is a claim connection the relay opened, which its server
// may report as claimed until expires.
type claimGrant struct {
	serverID string
	account  edgeproto.Account
	expires  time.Time
}

// claimReportWindow is how long after the open a server may report a
// claim connection as claimed: the grant's lifetime, which covers the
// attach deadline and the SSH authentication that checks the code.
const claimReportWindow = edgeproto.GrantTTL

// serverRefusal is a refusal whose text the server chose in OpenResult.
type serverRefusal string

func (s serverRefusal) Error() string { return string(s) }

func bearer(req *http.Request) (string, bool) {
	return strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
}

// clientAddr is the client address of remote, a request's RemoteAddr;
// the zero AddrPort when it does not parse.
func clientAddr(remote string) netip.AddrPort {
	ap, _ := netip.ParseAddrPort(remote)
	return ap
}

// refusal counts err and returns the status and text a client receives.
func (r *Relay) refusal(err error) (int, string) {
	var ref edgeproto.Refusal
	var sref serverRefusal
	switch {
	case errors.As(err, &sref):
		r.countRefusal("refused by server")
		return http.StatusForbidden, string(sref)
	case errors.As(err, &ref):
		r.countRefusal(string(ref))
		return ref.Status(), string(ref)
	}
	r.countRefusal("internal error")
	slog.Error("relay: connection failed", "error", err)
	return http.StatusInternalServerError, "internal error"
}

func writeError(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(edgeproto.ErrorBody{Error: text})
}

func (r *Relay) refuse(w http.ResponseWriter, err error) {
	status, text := r.refusal(err)
	writeError(w, status, text)
}

// serveConnect opens one relayed connection of kind: an ssh connection
// by a member, or a claim connection to a server without an owner.
func (r *Relay) serveConnect(w http.ResponseWriter, req *http.Request, kind string) {
	w.Header().Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	if v := req.Header.Get(edgeproto.HeaderVersion); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			err = edgeproto.CheckVersion(n)
		}
		if err != nil {
			r.countRefusal("upgrade required")
			writeError(w, http.StatusUpgradeRequired, fmt.Sprintf("%s %q: %v", edgeproto.HeaderVersion, v, err))
			return
		}
	}
	token, ok := bearer(req)
	if !ok {
		r.refuse(w, edgeproto.RefusalTokenRequired)
		return
	}
	if !edgeproto.ValidToken(token) {
		r.refuse(w, edgeproto.RefusalTokenRevoked)
		return
	}
	serverID := req.PathValue("server_id")
	addr := clientAddr(req.RemoteAddr)
	ctx, cancel := context.WithTimeout(req.Context(), directoryTimeout)
	account, device, err := r.dir.Authenticate(ctx, token)
	switch {
	case err != nil:
	case device.Key == "":
		// A token without a device key cannot carry SSH.
		err = edgeproto.RefusalTokenRequired
	case !edgeproto.ValidServerID(serverID):
		err = edgeproto.RefusalUnknownServer
	case kind == edgeproto.KindClaim:
		err = r.dir.AdmitClaim(ctx, serverID, account, addr.Addr())
	default:
		err = r.dir.Admit(ctx, serverID, account)
	}
	cancel()
	if err != nil {
		r.refuse(w, err)
		return
	}

	c, open, err := r.register(kind, serverID, account, device, addr)
	var server net.Conn
	if err == nil {
		server, err = r.attachDevice(req.Context(), c, open, token)
	}
	if err != nil {
		if req.Context().Err() == nil {
			r.refuse(w, err)
		}
		return
	}
	ws, err := websocket.Accept(w, req, acceptOptions)
	if err != nil {
		c.cancel(err)
		r.drop(c)
		return
	}
	r.splice(c, websocket.NetConn(c.ctx, ws, websocket.MessageBinary), server)
}

// register admits one connection of kind from the client at addr to
// serverID and returns it with the open to send.
func (r *Relay) register(kind, serverID string, account edgeproto.Account, device edgeproto.Device,
	addr netip.AddrPort) (*relayConn, edgeproto.Open, error) {
	c := &relayConn{
		id:       edgeproto.NewConnID(),
		kind:     kind,
		serverID: serverID,
		account:  account,
		deviceID: device.ID,
		result:   make(chan string, 1),
		attached: make(chan net.Conn, 1),
	}
	ticket := edgeproto.NewToken()
	c.ticketHash = edgeproto.HashToken(ticket)
	now := time.Now()
	grant, err := edgeproto.SignGrant(r.key, edgeproto.Grant{
		Issuer:      r.origin,
		ServerID:    serverID,
		ConnID:      c.id,
		Kind:        kind,
		Account:     account,
		DeviceID:    device.ID,
		DeviceKey:   device.Key,
		DeviceLabel: device.Label,
		IssuedAt:    now,
		ExpiresAt:   now.Add(edgeproto.GrantTTL),
	})
	if err != nil {
		return nil, edgeproto.Open{}, err
	}
	msg := edgeproto.Open{ConnID: c.id, Ticket: ticket, Kind: kind, Grant: grant}
	if addr.IsValid() {
		msg.ClientAddr = addr.String()
	}
	c.ctx, c.cancel = context.WithCancelCause(r.ctx)

	r.mu.Lock()
	reg, err := r.admitLocked(c)
	if err == nil {
		c.reg = reg
		r.conns[c.id] = c
		if kind == edgeproto.KindClaim {
			for id, g := range r.claims {
				if now.After(g.expires) {
					delete(r.claims, id)
				}
			}
			r.claims[c.id] = claimGrant{serverID: serverID, account: account, expires: now.Add(claimReportWindow)}
		}
	}
	r.mu.Unlock()
	if err != nil {
		c.cancel(err)
		return nil, edgeproto.Open{}, err
	}
	return c, msg, nil
}

// attachDevice is attach for a connection of the device holding token.
// Revoking a device deletes its token and then closes the device's
// connections in r.conns. c is in r.conns already, so a revocation that
// raced serveConnect's token check is caught by that close or by this
// check.
func (r *Relay) attachDevice(ctx context.Context, c *relayConn, msg edgeproto.Open, token string) (net.Conn, error) {
	actx, cancel := context.WithTimeout(ctx, directoryTimeout)
	_, _, err := r.dir.Authenticate(actx, token)
	cancel()
	if err != nil {
		c.cancel(err)
		r.drop(c)
		return nil, err
	}
	return r.attach(ctx, c, msg)
}

// attach sends msg to c's server and returns the data socket it attaches.
func (r *Relay) attach(ctx context.Context, c *relayConn, msg edgeproto.Open) (net.Conn, error) {
	server, err := r.await(ctx, c.reg, c, msg)
	if err != nil {
		c.cancel(err)
		r.drop(c)
		return nil, err
	}
	return server, nil
}

// admitLocked returns the registration c opens on, or the limit it
// exceeds. A server that is not claimed takes claim connections only.
func (r *Relay) admitLocked(c *relayConn) (*registration, error) {
	reg := r.servers[c.serverID]
	if r.closing || reg == nil || (!reg.claimed && c.kind != edgeproto.KindClaim) {
		return nil, edgeproto.RefusalNotConnected
	}
	perServer, perDevice := 0, 0
	for _, o := range r.conns {
		if o.serverID == c.serverID {
			perServer++
		}
		if o.deviceID == c.deviceID {
			perDevice++
		}
	}
	if perServer >= edgeproto.MaxSSHConnsPerServer || perDevice >= edgeproto.MaxConnsPerDevice {
		return nil, edgeproto.RefusalConnLimit
	}
	return reg, nil
}

func (r *Relay) await(ctx context.Context, reg *registration, c *relayConn, msg edgeproto.Open) (net.Conn, error) {
	if err := reg.send(msg); err != nil {
		slog.Info("relay: open not delivered", "server", reg.id, "error", err)
		return nil, edgeproto.RefusalNotConnected
	}
	deadline := time.NewTimer(r.attachDeadline)
	defer deadline.Stop()
	for {
		select {
		case server := <-c.attached:
			return server, nil
		case text := <-c.result:
			if text != "" {
				return nil, serverRefusal(text)
			}
		case <-deadline.C:
			return nil, edgeproto.RefusalNotAttached
		case <-reg.ctx.Done():
			return nil, edgeproto.RefusalNotConnected
		case <-c.ctx.Done():
			return nil, context.Cause(c.ctx)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (r *Relay) drop(c *relayConn) {
	r.mu.Lock()
	if r.conns[c.id] == c {
		delete(r.conns, c.id)
	}
	r.mu.Unlock()
}

func (r *Relay) serveData(w http.ResponseWriter, req *http.Request) {
	ticket, ok := bearer(req)
	if !ok || !edgeproto.ValidToken(ticket) {
		writeError(w, http.StatusUnauthorized, "data socket needs its ticket")
		return
	}
	r.mu.Lock()
	c := r.conns[req.PathValue("conn_id")]
	var want string
	if c != nil {
		want = c.ticketHash
		c.ticketHash = ""
	}
	r.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(edgeproto.HashToken(ticket)), []byte(want)) != 1 {
		if want != "" {
			c.cancel(edgeproto.RefusalNotAttached)
		}
		writeError(w, http.StatusForbidden, "ticket refused")
		return
	}
	ws, err := websocket.Accept(w, req, acceptOptions)
	if err != nil {
		c.cancel(edgeproto.RefusalNotAttached)
		return
	}
	server := websocket.NetConn(c.ctx, ws, websocket.MessageBinary)
	c.attached <- server
	<-c.ctx.Done()
	_ = server.Close()
}
