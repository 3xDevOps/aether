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

	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/coder/websocket"
)

// relayConn is one client connection, from the open to the end of its
// splice. Cancelling ctx closes it; the cause is the refusal a waiting
// client receives.
type relayConn struct {
	id       string
	serverID string
	kind     string
	account  edgeproto.Account
	deviceID string
	// addr is the client's edgeproto.RateLimitKey block.
	addr netip.Prefix
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

type pendingClaim struct {
	serverID string
	result   chan string
	// directory is the latest directory the server pushed while the claim
	// was pending, guarded by Relay.mu. The server pushes one as it
	// accepts, before the edge has recorded the claim.
	directory    []edgeproto.DirectoryEntry
	hasDirectory bool
}

// serverRefusal is a refusal whose text the server chose in OpenResult.
type serverRefusal string

func (s serverRefusal) Error() string { return string(s) }

func bearer(req *http.Request) (string, bool) {
	return strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
}

// clientAddr is the client address an open carries, for the server's logs
// and rate limits, and the edgeproto.RateLimitKey block it counts
// against. An unparsable address counts as the zero block.
func clientAddr(remote string) (string, netip.Prefix) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return "", netip.Prefix{}
	}
	return ap.String(), edgeproto.RateLimitKey(ap.Addr())
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

func (r *Relay) serveConnect(w http.ResponseWriter, req *http.Request) {
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
	ctx, cancel := context.WithTimeout(req.Context(), directoryTimeout)
	account, device, err := r.dir.Authenticate(ctx, token)
	switch {
	case err != nil:
	case device.Key == "":
		// A browser's token has no device key and cannot carry SSH.
		err = edgeproto.RefusalTokenRequired
	case !edgeproto.ValidServerID(serverID):
		err = edgeproto.RefusalUnknownServer
	default:
		err = r.dir.Admit(ctx, serverID, account)
	}
	cancel()
	if err != nil {
		r.refuse(w, err)
		return
	}

	c, open, err := r.register(serverID, edgeproto.KindSSH, account, device, req.RemoteAddr)
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

// open asks serverID to attach a data socket for one connection from the
// client at remote and returns the attached socket.
func (r *Relay) open(ctx context.Context, serverID, kind string, account edgeproto.Account, device edgeproto.Device, remote string) (*relayConn, net.Conn, error) {
	c, msg, err := r.register(serverID, kind, account, device, remote)
	if err != nil {
		return nil, nil, err
	}
	server, err := r.attach(ctx, c, msg)
	if err != nil {
		return nil, nil, err
	}
	return c, server, nil
}

// register admits one connection from the client at remote to serverID
// and returns it with the open to send.
func (r *Relay) register(serverID, kind string, account edgeproto.Account, device edgeproto.Device, remote string) (*relayConn, edgeproto.Open, error) {
	addr, block := clientAddr(remote)
	c := &relayConn{
		id:       edgeproto.NewConnID(),
		serverID: serverID,
		kind:     kind,
		account:  account,
		deviceID: device.ID,
		addr:     block,
		result:   make(chan string, 1),
		attached: make(chan net.Conn, 1),
	}
	ticket := edgeproto.NewToken()
	c.ticketHash = edgeproto.HashToken(ticket)
	msg := edgeproto.Open{ConnID: c.id, Ticket: ticket, Kind: kind, ClientAddr: addr}
	if kind == edgeproto.KindSSH {
		now := time.Now()
		grant, err := edgeproto.SignGrant(r.key, edgeproto.Grant{
			ServerID:    serverID,
			ConnID:      c.id,
			Kind:        edgeproto.KindSSH,
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
		msg.Grant = grant
	}
	c.ctx, c.cancel = context.WithCancelCause(r.ctx)

	r.mu.Lock()
	reg, err := r.admitLocked(c)
	if err == nil {
		c.reg = reg
		r.conns[c.id] = c
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
// exceeds. SSH and dashboard passthrough have separate budgets per server,
// so dashboard connections, which need no sign-in at the edge, cannot use
// up the ones SSH needs; one client address block holds only a share of
// the dashboard budget.
func (r *Relay) admitLocked(c *relayConn) (*registration, error) {
	reg := r.servers[c.serverID]
	if r.closing || reg == nil || !reg.claimed {
		return nil, edgeproto.RefusalNotConnected
	}
	perServer, perDevice, perAddr := 0, 0, 0
	for _, o := range r.conns {
		if o.kind != c.kind {
			continue
		}
		if o.serverID == c.serverID {
			perServer++
			if o.addr == c.addr {
				perAddr++
			}
		}
		if o.deviceID == c.deviceID {
			perDevice++
		}
	}
	full := false
	switch c.kind {
	case edgeproto.KindSSH:
		full = perServer >= edgeproto.MaxSSHConnsPerServer || perDevice >= edgeproto.MaxConnsPerDevice
	case edgeproto.KindWeb:
		full = perServer >= edgeproto.MaxWebConnsPerServer || perAddr >= edgeproto.MaxWebConnsPerAddress
	}
	if full {
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

// ClaimUnsettledError reports a claim attempt that ended with the server
// possibly holding an owner the edge did not record: the server accepted
// it but recording failed, or the server's answer never arrived. The
// relay has closed that server's control channel. When the server
// reconnects, the edge reports it unclaimed and the server drops the
// owner, so a new claim starts over.
type ClaimUnsettledError struct {
	ServerID string
	Err      error
}

func (e *ClaimUnsettledError) Error() string {
	return fmt.Sprintf("the claim of server %s did not complete: %v. The edge disconnected the server so that it "+
		"drops this claim when it reconnects; then claim it again, with a new code from "+
		"`sudo aether-server edge claim-code` if this one is refused", e.ServerID, e.Err)
}

func (e *ClaimUnsettledError) Unwrap() error { return e.Err }

// Reasons a claim leaves the server's control channel closed.
var (
	errClaimUnsettled = errors.New("claim did not complete; reconnect to learn this server's claim state")
	errClaimRecorded  = errors.New("claim recorded after this connection enrolled; reconnect to learn it")
)

// Claim forwards a claim attempt by account on device to the connected
// server whose id the code names and waits for its answer. When the server
// accepts, Claim calls record with the server's id and name to record
// account as its owner, and marks the server claimed only once record
// succeeds. It returns the server's id and name then, an edgeproto.Refusal
// when the edge or the server refused, and a *ClaimUnsettledError when the
// server's answer did not arrive or record failed.
//
// Once the claim is sent, the attempt no longer follows ctx's
// cancellation: the server may accept at any moment, so its answer is
// awaited, and recorded, even if the person has gone.
func (r *Relay) Claim(ctx context.Context, code string, account edgeproto.Account, device edgeproto.Device,
	record func(ctx context.Context, serverID, name string) error) (serverID, name string, err error) {
	normalized, serverID, err := edgeproto.ParseClaimCode(code)
	if err != nil {
		return "", "", edgeproto.RefusalClaimWrong
	}
	r.mu.Lock()
	reg := r.servers[serverID]
	if reg == nil {
		r.mu.Unlock()
		return "", "", edgeproto.RefusalNotConnected
	}
	if reg.claimed {
		r.mu.Unlock()
		return "", "", edgeproto.RefusalClaimed
	}
	id := edgeproto.NewConnID()
	p := &pendingClaim{serverID: reg.id, result: make(chan string, 1)}
	r.claims[id] = p
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.claims, id)
		r.mu.Unlock()
	}()

	now := time.Now()
	grant, err := edgeproto.SignGrant(r.key, edgeproto.Grant{
		ServerID:    reg.id,
		ConnID:      id,
		Kind:        edgeproto.KindClaim,
		Account:     account,
		DeviceID:    device.ID,
		DeviceKey:   device.Key,
		DeviceLabel: device.Label,
		IssuedAt:    now,
		ExpiresAt:   now.Add(edgeproto.GrantTTL),
	})
	if err != nil {
		return "", "", err
	}
	if err = reg.send(edgeproto.Claim{ID: id, Code: normalized, Grant: grant}); err != nil {
		slog.Info("relay: claim not delivered", "server", reg.id, "error", err)
		return "", "", edgeproto.RefusalNotConnected
	}
	deadline := time.NewTimer(r.attachDeadline)
	defer deadline.Stop()
	select {
	case text := <-p.result:
		if text != "" {
			return "", "", edgeproto.Refusal(text)
		}
	case <-reg.ctx.Done():
		return "", "", r.unsettle(reg, errors.New("the server disconnected before it answered"))
	case <-deadline.C:
		return "", "", r.unsettle(reg, fmt.Errorf("the server did not answer within %s", r.attachDeadline))
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directoryTimeout)
	err = record(rctx, reg.id, reg.name)
	cancel()
	if err != nil {
		return "", "", r.unsettle(reg, fmt.Errorf("the server accepted it, but the edge could not record you as its owner: %w", err))
	}
	r.mu.Lock()
	cur := r.servers[reg.id]
	if cur == reg {
		reg.claimed = true
		if p.hasDirectory {
			r.queueDirectoryLocked(reg, p.directory)
		}
	}
	r.mu.Unlock()
	if cur != nil && cur != reg {
		// It enrolled again before the owner was recorded and was told
		// it is unclaimed.
		cur.cancel(errClaimRecorded)
	}
	return reg.id, reg.name, nil
}

// unsettle closes reg's control channel after a claim that may have left
// the server with an owner the edge did not record, and returns the
// *ClaimUnsettledError for cause.
func (r *Relay) unsettle(reg *registration, cause error) error {
	slog.Warn("relay: claim did not complete; disconnecting the server", "server", reg.id, "error", cause)
	reg.cancel(errClaimUnsettled)
	return &ClaimUnsettledError{ServerID: reg.id, Err: cause}
}
