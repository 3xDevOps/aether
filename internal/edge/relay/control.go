package relay

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/coder/websocket"
)

// Reasons a control channel closes, sent to the server as the close
// reason.
var (
	errReplaced      = errors.New("replaced by a newer connection of this server")
	errUnclaimed     = fmt.Errorf("unclaimed for %s: an unclaimed server is relayed only claim connections, for this long", edgeproto.UnclaimedTTL)
	errDraining      = errors.New("edge is shutting down")
	errUnenrolled    = errors.New("server is no longer enrolled at this edge")
	errClaimRecorded = errors.New("claim recorded after this connection enrolled; reconnect to learn it")
)

// registration is one authenticated control channel.
type registration struct {
	id     string
	name   string
	policy edgeproto.AccessPolicy
	addr   netip.Prefix
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelCauseFunc
	// turn holds the one throttled read this server's connections may
	// have waiting.
	turn chan struct{}
	// claimed, directory and hasDirectory are guarded by Relay.mu.
	// directory is the latest directory an unclaimed server pushed: it
	// pushes one as it accepts a claim, which may arrive before the edge
	// recorded the claim.
	claimed      bool
	directory    []edgeproto.DirectoryEntry
	hasDirectory bool
}

func (g *registration) send(m edgeproto.Message) error {
	ctx, cancel := context.WithTimeout(g.ctx, writeTimeout)
	defer cancel()
	if err := writeControl(ctx, g.ws, m); err != nil {
		return fmt.Errorf("relay: send to server %s: %w", g.id, err)
	}
	return nil
}

func writeControl(ctx context.Context, ws *websocket.Conn, m edgeproto.Message) error {
	data, err := edgeproto.EncodeControl(m)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, data)
}

func readControl(ctx context.Context, ws *websocket.Conn) (edgeproto.Message, error) {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	return edgeproto.DecodeControl(data)
}

// closeReason fits err into a WebSocket close frame.
func closeReason(err error) string {
	const maxCloseReason = 123
	s := err.Error()
	if len(s) > maxCloseReason {
		s = s[:maxCloseReason]
	}
	return s
}

func (r *Relay) serveControl(w http.ResponseWriter, req *http.Request) {
	remote, err := netip.ParseAddrPort(req.RemoteAddr)
	if err != nil {
		http.Error(w, fmt.Sprintf("relay: client address %q: %v", req.RemoteAddr, err), http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		writeError(w, http.StatusServiceUnavailable, errDraining.Error())
		return
	}
	ws, err := websocket.Accept(w, req, acceptOptions)
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(edgeproto.MaxControlMessageSize)
	reg, err := r.enroll(ws, edgeproto.RateLimitKey(remote.Addr()))
	if err != nil {
		r.countRefusal("enrollment refused")
		slog.Info("relay: enrollment refused", "client", remote, "error", err)
		_ = ws.Close(websocket.StatusPolicyViolation, closeReason(err))
		return
	}
	r.serveRegistration(reg)
}

// enroll runs the challenge and hello exchange and registers the server.
func (r *Relay) enroll(ws *websocket.Conn, addr netip.Prefix) (*registration, error) {
	ctx, cancel := context.WithTimeout(r.ctx, handshakeTimeout)
	defer cancel()
	nonce := make([]byte, edgeproto.NonceSize)
	_, _ = rand.Read(nonce) // crypto/rand.Read never returns an error.
	if err := writeControl(ctx, ws, edgeproto.Challenge{Version: edgeproto.Version, Nonce: nonce, Origin: r.origin}); err != nil {
		return nil, fmt.Errorf("relay: send challenge: %w", err)
	}
	m, err := readControl(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("relay: read hello: %w", err)
	}
	hello, ok := m.(edgeproto.Hello)
	if !ok {
		return nil, fmt.Errorf("relay: expected hello, got %T", m)
	}
	if err = edgeproto.CheckVersion(hello.Version); err != nil {
		return nil, err
	}
	hostKey, err := hello.PublicKey()
	if err != nil {
		return nil, err
	}
	id, err := edgeproto.VerifyEnrollment(hostKey, r.origin, nonce, hello.Signature)
	if err != nil {
		return nil, err
	}
	policy, err := edgeproto.ParseAccessPolicy(string(hello.AccessPolicy))
	if err != nil {
		return nil, err
	}
	claimed, err := r.dir.Enroll(ctx, id, hello.Name, policy)
	var refusal edgeproto.Refusal
	if errors.As(err, &refusal) {
		return nil, refusal
	}
	if err != nil {
		return nil, fmt.Errorf("relay: claim state of %s: %w", id, err)
	}

	reg := &registration{id: id, name: hello.Name, policy: policy, addr: addr, ws: ws, claimed: claimed, turn: make(chan struct{}, 1)}
	reg.ctx, reg.cancel = context.WithCancelCause(r.ctx)
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return nil, errDraining
	}
	if !claimed {
		if err := r.admitUnclaimedLocked(addr, id); err != nil {
			r.mu.Unlock()
			return nil, err
		}
	}
	old := r.servers[id]
	r.servers[id] = reg
	r.mu.Unlock()
	if old != nil {
		old.cancel(errReplaced)
	}

	state := edgeproto.StateUnclaimed
	if claimed {
		state = edgeproto.StateClaimed
	}
	if err := reg.send(edgeproto.Ready{ServerID: id, State: state, EdgeKey: r.pub}); err != nil {
		r.unregister(reg, err)
		return nil, err
	}
	return reg, nil
}

// admitUnclaimedLocked refuses one more unclaimed registration from the
// client block addr when a block it lies in (edgeproto.RateLimitKeys), or
// the edge, holds its limit. A registration for serverID does not count:
// the new one would replace it.
func (r *Relay) admitUnclaimedLocked(addr netip.Prefix, serverID string) error {
	blocks := edgeproto.RateLimitKeys(addr.Addr())
	inBlock := make([]int, len(blocks))
	total := 0
	for id, reg := range r.servers {
		if id == serverID || reg.claimed {
			continue
		}
		total++
		for i, b := range blocks {
			if b.Contains(reg.addr.Addr()) {
				inBlock[i]++
			}
		}
	}
	for i, b := range blocks {
		if limit := edgeproto.MaxUnclaimedPerAddress * edgeproto.RateLimitScale(b); inBlock[i] >= limit {
			return fmt.Errorf("relay: %d unclaimed servers are already connected from %s; claim one first", limit, b)
		}
	}
	if total >= r.maxUnclaimed {
		return fmt.Errorf("relay: this edge already holds its limit of %d unclaimed servers", r.maxUnclaimed)
	}
	return nil
}

func (r *Relay) unregister(reg *registration, cause error) {
	reg.cancel(cause)
	r.mu.Lock()
	if r.servers[reg.id] == reg {
		delete(r.servers, reg.id)
	}
	r.mu.Unlock()
}

// serveRegistration reads the server's messages until the control channel
// closes. Cancelling reg.ctx closes it with the cause as the reason.
func (r *Relay) serveRegistration(reg *registration) {
	context.AfterFunc(reg.ctx, func() {
		_ = reg.ws.Close(websocket.StatusNormalClosure, closeReason(context.Cause(reg.ctx)))
	})
	ttl := time.AfterFunc(r.unclaimedTTL, func() {
		r.mu.Lock()
		claimed := reg.claimed
		r.mu.Unlock()
		if !claimed {
			reg.cancel(errUnclaimed)
		}
	})
	defer ttl.Stop()
	go r.keepAlive(reg)
	go r.deliverDeletions(reg)

	for {
		ctx, cancel := context.WithTimeout(context.Background(), r.idleTimeout)
		m, err := readControl(ctx, reg.ws)
		cancel()
		if errors.Is(err, edgeproto.ErrUnknownMessage) {
			continue
		}
		if err == nil {
			err = r.handle(reg, m)
		}
		if err != nil {
			if reg.ctx.Err() == nil {
				slog.Info("relay: control channel closed", "server", reg.id, "error", err)
			}
			r.unregister(reg, err)
			// Close here too: serveControl drops the connection once this
			// returns, which would cut short the close that carries the
			// reason, such as why an ownership report was refused.
			_ = reg.ws.Close(websocket.StatusNormalClosure, closeReason(context.Cause(reg.ctx)))
			return
		}
	}
}

func (r *Relay) keepAlive(reg *registration) {
	t := time.NewTicker(r.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-reg.ctx.Done():
			return
		case <-t.C:
			if err := reg.send(edgeproto.Ping{}); err != nil {
				reg.cancel(err)
				return
			}
		}
	}
}

func (r *Relay) handle(reg *registration, m edgeproto.Message) error {
	switch m := m.(type) {
	case edgeproto.Ping:
		return reg.send(edgeproto.Pong{})
	case edgeproto.Pong:
		return nil
	case edgeproto.OpenResult:
		r.mu.Lock()
		c := r.conns[m.ConnID]
		r.mu.Unlock()
		if c != nil && c.serverID == reg.id {
			select {
			case c.result <- m.Error:
			default:
			}
		}
		return nil
	case edgeproto.Directory:
		r.replaceDirectory(reg, m)
		return nil
	case edgeproto.Claimed:
		return r.claimReported(reg, m)
	case edgeproto.OwnerTransferred:
		return r.transferReported(reg, m)
	case edgeproto.Ownerless:
		if !r.claimed(reg) {
			return nil
		}
		return r.recordOwner(reg, func(ctx context.Context) error { return r.dir.DropOwner(ctx, reg.id) })
	case edgeproto.AccountDeletionApplied:
		r.deletionApplied(reg, edgeproto.AccountDeleted(m))
		return nil
	case edgeproto.Unenroll:
		return r.leave(reg)
	}
	return fmt.Errorf("relay: unexpected %T from server", m)
}

// reportRefused is an ownership report the edge did not record. A
// transfer is answered with it. Any other report closes the control
// channel with it as the reason; the server, reconnecting, learns from
// ready whether the edge holds it as claimed.
func reportRefused(format string, args ...any) error {
	return fmt.Errorf("ownership report refused: "+format, args...)
}

// claimReported records the owner a server reported after a claim
// connection presented its claim code. Only a claim connection the relay
// opened to this server within claimReportWindow counts, and only for the
// account that connection was opened for: a server cannot name an owner
// of its choosing, or report a claim of another server.
func (r *Relay) claimReported(reg *registration, m edgeproto.Claimed) error {
	r.mu.Lock()
	g, ok := r.claims[m.ConnID]
	if ok && g.serverID == reg.id {
		delete(r.claims, m.ConnID)
	}
	r.mu.Unlock()
	switch {
	case !ok || time.Now().After(g.expires):
		return reportRefused("connection %s is not a claim connection this edge opened in the last %s", m.ConnID, claimReportWindow)
	case g.serverID != reg.id:
		return reportRefused("connection %s is not to this server", m.ConnID)
	case m.Owner != edgeproto.AccountPrincipal(g.account):
		return reportRefused("the owner is not the account connection %s was opened for", m.ConnID)
	}
	return r.recordOwner(reg, func(ctx context.Context) error {
		return r.dir.RecordClaim(ctx, reg.id, reg.name, reg.policy, g.account)
	})
}

// transferReported records the owner a server reported after an admin
// transferred it, and answers with the outcome: the server made the
// transfer only on the condition that the edge records it.
func (r *Relay) transferReported(reg *registration, m edgeproto.OwnerTransferred) error {
	err := reportRefused("this server was never claimed at this edge; claim it first")
	if r.claimed(reg) {
		err = r.recordOwner(reg, func(ctx context.Context) error { return r.dir.TransferOwner(ctx, reg.id, m.Owner) })
	}
	res := edgeproto.OwnerTransferResult{Owner: m.Owner}
	if err != nil {
		res.Error = err.Error()
	}
	return reg.send(res)
}

// recordOwner runs record and, once it succeeded, treats reg as claimed.
// A refusal's text becomes the report's refusal reason; any other failure
// is logged and reported as internal.
func (r *Relay) recordOwner(reg *registration, record func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(reg.ctx, directoryTimeout)
	err := record(ctx)
	cancel()
	var refusal edgeproto.Refusal
	switch {
	case errors.As(err, &refusal):
		return reportRefused("%s", refusal)
	case err != nil:
		slog.Error("relay: ownership report not recorded", "server", reg.id, "error", err)
		return reportRefused("internal error")
	}
	r.mu.Lock()
	if !reg.claimed {
		reg.claimed = true
		if reg.hasDirectory {
			r.queueDirectoryLocked(reg, reg.directory)
			reg.directory, reg.hasDirectory = nil, false
		}
	}
	cur := r.servers[reg.id]
	stale := cur != nil && cur != reg && !cur.claimed
	r.mu.Unlock()
	if stale {
		// The server enrolled again before this report was recorded and
		// was told it is unclaimed.
		cur.cancel(errClaimRecorded)
	}
	return nil
}

func (r *Relay) claimed(reg *registration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return reg.claimed
}

// deliverDeletions sends reg's server the account deletions it is owed.
// Each stays owed until the server answers
// edgeproto.AccountDeletionApplied: a write reaching the socket does not
// mean the server applied it.
func (r *Relay) deliverDeletions(reg *registration) {
	ctx, cancel := context.WithTimeout(reg.ctx, directoryTimeout)
	defer cancel()
	owed, err := r.dir.PendingDeletions(ctx, reg.id)
	if err != nil {
		slog.Warn("relay: account deletions not read; sent at the next enrollment", "server", reg.id, "error", err)
		return
	}
	for _, d := range owed {
		if err := reg.send(d); err != nil {
			slog.Info("relay: account deletion not delivered; sent at the next enrollment", "server", reg.id, "error", err)
			return
		}
	}
}

// deletionApplied forgets the deletion reg's server applied.
func (r *Relay) deletionApplied(reg *registration, d edgeproto.AccountDeleted) {
	ctx, cancel := context.WithTimeout(reg.ctx, directoryTimeout)
	defer cancel()
	if err := r.dir.DeletionApplied(ctx, reg.id, d); err != nil {
		slog.Warn("relay: applied account deletion not forgotten; sent again at the next enrollment", "server", reg.id, "error", err)
	}
}

// directoryWrite is a claimed server's latest directory push waiting to
// be stored.
type directoryWrite struct {
	reg     *registration
	entries []edgeproto.DirectoryEntry
	pending bool
}

func (r *Relay) replaceDirectory(reg *registration, m edgeproto.Directory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reg.claimed {
		r.queueDirectoryLocked(reg, m.Entries)
		return
	}
	reg.directory, reg.hasDirectory = m.Entries, true
}

func (r *Relay) queueDirectoryLocked(reg *registration, entries []edgeproto.DirectoryEntry) {
	w := r.directories[reg.id]
	if w == nil {
		w = &directoryWrite{}
		r.directories[reg.id] = w
		go r.storeDirectories(reg.id, w)
	}
	w.reg, w.entries, w.pending = reg, entries, true
}

// storeDirectories stores serverID's latest pushed directory at most once
// per directoryInterval, and ends after an interval with no push. It is
// keyed by server id, not by control channel, so reconnecting does not
// skip the wait.
func (r *Relay) storeDirectories(serverID string, w *directoryWrite) {
	for {
		r.mu.Lock()
		if !w.pending {
			delete(r.directories, serverID)
			r.mu.Unlock()
			return
		}
		reg, entries := w.reg, w.entries
		w.pending, w.entries = false, nil
		r.mu.Unlock()
		if err := r.storeDirectory(reg, entries); err != nil && reg.ctx.Err() == nil {
			slog.Warn("relay: directory not stored; closing the control channel", "server", serverID, "error", err)
			reg.cancel(err)
		}
		if !sleep(r.ctx, r.directoryInterval) {
			return
		}
	}
}

func (r *Relay) storeDirectory(reg *registration, entries []edgeproto.DirectoryEntry) error {
	ctx, cancel := context.WithTimeout(reg.ctx, directoryTimeout)
	defer cancel()
	if err := r.dir.ReplaceDirectory(ctx, reg.id, entries); err != nil {
		return fmt.Errorf("relay: store directory of %s: %w", reg.id, err)
	}
	now := time.Now()
	// A claim connection is admitted by the server's claim code, not by
	// the directory, which may predate the claim it makes.
	r.closeConns(func(c *relayConn) bool {
		if c.serverID != reg.id || c.kind == edgeproto.KindClaim {
			return false
		}
		for _, e := range entries {
			if e.Matches(c.account, now) {
				return false
			}
		}
		return true
	}, edgeproto.RefusalNotMember)
	return nil
}

func (r *Relay) leave(reg *registration) error {
	r.CloseServer(reg.id)
	if !r.claimed(reg) {
		return errUnenrolled
	}
	ctx, cancel := context.WithTimeout(reg.ctx, directoryTimeout)
	defer cancel()
	if err := r.dir.Unenroll(ctx, reg.id); err != nil {
		return fmt.Errorf("relay: forget server %s: %w", reg.id, err)
	}
	return errUnenrolled
}
