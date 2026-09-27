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
}

// serverRefusal is a refusal whose text the server chose in OpenResult.
type serverRefusal string

func (s serverRefusal) Error() string { return string(s) }

func bearer(req *http.Request) (string, bool) {
	return strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
}

// clientAddr is the client address an open carries, for the server's logs
// and rate limits.
func clientAddr(remote string) string {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return ""
	}
	return ap.String()
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

	c, server, err := r.open(req.Context(), serverID, edgeproto.KindSSH, account, device, clientAddr(req.RemoteAddr))
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

// open asks serverID to attach a data socket for one connection and
// returns the attached socket.
func (r *Relay) open(ctx context.Context, serverID, kind string, account edgeproto.Account, device edgeproto.Device, addr string) (*relayConn, net.Conn, error) {
	c := &relayConn{
		id:       edgeproto.NewConnID(),
		serverID: serverID,
		kind:     kind,
		account:  account,
		deviceID: device.ID,
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
			return nil, nil, err
		}
		msg.Grant = grant
	}
	c.ctx, c.cancel = context.WithCancelCause(r.ctx)

	r.mu.Lock()
	reg, err := r.admitLocked(c)
	if err == nil {
		r.conns[c.id] = c
	}
	r.mu.Unlock()
	if err != nil {
		c.cancel(err)
		return nil, nil, err
	}
	server, err := r.await(ctx, reg, c, msg)
	if err != nil {
		c.cancel(err)
		r.drop(c)
		return nil, nil, err
	}
	return c, server, nil
}

// admitLocked returns the registration c opens on, or the limit it
// exceeds. Web passthrough is unauthenticated at the edge, so it counts
// against its own per-server pool and cannot use up the one SSH needs.
func (r *Relay) admitLocked(c *relayConn) (*registration, error) {
	reg := r.servers[c.serverID]
	if r.closing || reg == nil || !reg.claimed {
		return nil, edgeproto.RefusalNotConnected
	}
	perServer, perDevice := 0, 0
	for _, o := range r.conns {
		if o.serverID == c.serverID && o.kind == c.kind {
			perServer++
		}
		if c.kind == edgeproto.KindSSH && o.deviceID == c.deviceID {
			perDevice++
		}
	}
	if perServer >= edgeproto.MaxConnsPerServer || perDevice >= edgeproto.MaxConnsPerDevice {
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

// Claim forwards a claim attempt by account on device to the connected
// server whose id the code names, and waits for its answer. It returns the
// server's id and name when account is now its owner, and an
// edgeproto.Refusal when the edge or the server refused. The caller
// records the owner.
func (r *Relay) Claim(ctx context.Context, code string, account edgeproto.Account, device edgeproto.Device) (serverID, name string, err error) {
	normalized, prefix, err := edgeproto.ParseClaimCode(code)
	if err != nil {
		return "", "", edgeproto.RefusalClaimWrong
	}
	r.mu.Lock()
	var reg *registration
	matches := 0
	for id, g := range r.servers {
		if strings.HasPrefix(id, prefix) {
			reg = g
			matches++
		}
	}
	// Two servers sharing a prefix means one ground its host key to
	// collect the other's claim code: forward it to neither.
	if matches != 1 {
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
		return "", "", edgeproto.RefusalNotConnected
	case <-deadline.C:
		return "", "", fmt.Errorf("relay: server %s did not answer the claim within %s", reg.id, r.attachDeadline)
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	r.mu.Lock()
	reg.claimed = true
	r.mu.Unlock()
	return reg.id, reg.name, nil
}
