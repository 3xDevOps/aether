package edgetest

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// proxy stands between the edge and everything that dials it. It passes
// every request through unchanged, and additionally decodes the control
// channels it carries, recording each message. A test makes it act as a
// compromised edge: it injects messages in either direction, alters or
// drops them in flight, routes a client's connection to another server,
// and answers the data sockets of opens it forged itself.
type proxy struct {
	mu      sync.Mutex
	blocked bool
	links   []*controlLink
	log     []logEntry
	changed chan struct{}
	// tamper, when set, sees each control message before it is passed on
	// and returns the message to pass on instead, or false to drop it.
	tamper func(logEntry) (edgeproto.Message, bool)
	// route, when set, rewrites the path of every other request.
	route func(path string) string
	// forged holds the data sockets of forged opens, by connection id,
	// until the server dials them.
	forged map[string]chan net.Conn
}

type controlLink struct {
	serverID    string
	agent, edge *websocket.Conn
}

type logEntry struct {
	serverID string
	fromEdge bool
	msg      edgeproto.Message
}

func newProxy() *proxy {
	return &proxy{changed: make(chan struct{}), forged: map[string]chan net.Conn{}}
}

func (p *proxy) handler(backAddr string) http.Handler {
	target := &url.URL{Scheme: "http", Host: backAddr}
	rp := &httputil.ReverseProxy{
		// The edge picks the origin by host name, so the request keeps
		// the one the client named.
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == edgeproto.PathServerControl {
			p.serveControl(w, r, backAddr)
			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/v1/server/data/"); ok && p.serveForged(w, r, id) {
			return
		}
		p.mu.Lock()
		route := p.route
		p.mu.Unlock()
		if route != nil {
			r.URL.Path = route(r.URL.Path)
		}
		rp.ServeHTTP(w, r)
	})
}

func (p *proxy) serveControl(w http.ResponseWriter, r *http.Request, backAddr string) {
	p.mu.Lock()
	blocked := p.blocked
	p.mu.Unlock()
	if blocked {
		http.Error(w, "control channels blocked by the test", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	edgeWS, _, err := websocket.Dial(ctx, "ws://"+backAddr+edgeproto.PathServerControl,
		&websocket.DialOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	agentWS, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		_ = edgeWS.CloseNow()
		return
	}
	for _, c := range []*websocket.Conn{edgeWS, agentWS} {
		c.SetReadLimit(edgeproto.MaxControlMessageSize)
	}
	l := &controlLink{agent: agentWS, edge: edgeWS}
	p.mu.Lock()
	p.links = append(p.links, l)
	p.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { p.pump(ctx, l, edgeWS, agentWS, true); done <- struct{}{} }()
	go func() { p.pump(ctx, l, agentWS, edgeWS, false); done <- struct{}{} }()
	<-done
	_ = agentWS.CloseNow()
	_ = edgeWS.CloseNow()
}

// pump copies one direction of a control channel, recording each message
// and passing the close reason on.
func (p *proxy) pump(ctx context.Context, l *controlLink, src, dst *websocket.Conn, fromEdge bool) {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				_ = dst.Close(ce.Code, ce.Reason)
			}
			return
		}
		if m, err := edgeproto.DecodeControl(data); err == nil {
			e := p.record(l, fromEdge, m)
			p.mu.Lock()
			tamper := p.tamper
			p.mu.Unlock()
			if tamper != nil {
				out, keep := tamper(e)
				if !keep {
					continue
				}
				if data, err = edgeproto.EncodeControl(out); err != nil {
					return
				}
			}
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

func (p *proxy) record(l *controlLink, fromEdge bool, m edgeproto.Message) logEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch m := m.(type) {
	case edgeproto.Hello:
		if key, err := m.PublicKey(); err == nil {
			l.serverID = edgeproto.ServerID(key)
		}
	case edgeproto.Ready:
		l.serverID = m.ServerID
	}
	e := logEntry{serverID: l.serverID, fromEdge: fromEdge, msg: m}
	p.log = append(p.log, e)
	close(p.changed)
	p.changed = make(chan struct{})
	return e
}

// setTamper installs f as the proxy's tamper function until the test
// ends, or clears it with nil.
func (p *proxy) setTamper(t *testing.T, f func(logEntry) (edgeproto.Message, bool)) {
	p.mu.Lock()
	p.tamper = f
	p.mu.Unlock()
	t.Cleanup(func() {
		p.mu.Lock()
		p.tamper = nil
		p.mu.Unlock()
	})
}

func (p *proxy) setRoute(t *testing.T, f func(string) string) {
	p.mu.Lock()
	p.route = f
	p.mu.Unlock()
	t.Cleanup(func() {
		p.mu.Lock()
		p.route = nil
		p.mu.Unlock()
	})
}

// mark is where the log ends now; await with it sees only later messages.
func (p *proxy) mark() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.log)
}

// await returns the first message recorded at or after from that match
// accepts, waiting for it when there is none yet.
func (p *proxy) await(t *testing.T, what string, from int, match func(logEntry) bool) logEntry {
	t.Helper()
	timeout := time.After(waitTimeout)
	for {
		p.mu.Lock()
		for _, e := range p.log[from:] {
			if match(e) {
				p.mu.Unlock()
				return e
			}
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-timeout:
			t.Fatalf("no %s within %s", what, waitTimeout)
		}
	}
}

// inject sends m on the newest control channel of serverID, to the server
// when toServer is set and to the edge otherwise.
func (p *proxy) inject(t *testing.T, serverID string, m edgeproto.Message, toServer bool) {
	t.Helper()
	data, err := edgeproto.EncodeControl(m)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	var l *controlLink
	for _, c := range p.links {
		if c.serverID == serverID {
			l = c
		}
	}
	p.mu.Unlock()
	if l == nil {
		t.Fatalf("no control channel of server %s", serverID)
	}
	dst := l.edge
	if toServer {
		dst = l.agent
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := dst.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

// serveForged answers the data socket of a forged open, reporting whether
// connID was one.
func (p *proxy) serveForged(w http.ResponseWriter, r *http.Request, connID string) bool {
	p.mu.Lock()
	ch, ok := p.forged[connID]
	delete(p.forged, connID)
	p.mu.Unlock()
	if !ok {
		return false
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	ch <- closeConn{Conn: nc, cancel: cancel}
	<-ctx.Done()
	return true
}

// closeConn ends the forged data socket's handler when closed.
type closeConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c closeConn) Close() error {
	err := c.Conn.Close()
	c.cancel()
	return err
}

// cutServers disconnects every server from the edge and keeps them off it
// until restoreServers, while clients still reach the edge.
func (p *proxy) cutServers() {
	p.mu.Lock()
	p.blocked = true
	p.mu.Unlock()
	p.closeLinks()
}

func (p *proxy) restoreServers() {
	p.mu.Lock()
	p.blocked = false
	p.mu.Unlock()
}

func (p *proxy) closeLinks() {
	p.mu.Lock()
	links := p.links
	p.links = nil
	p.mu.Unlock()
	for _, l := range links {
		_ = l.agent.CloseNow()
		_ = l.edge.CloseNow()
	}
}

// forgery is what an attacker holding the edge's signing key sends a
// server: a grant it signs itself, for any account and device key.
type forgery struct {
	kind    string
	account edgeproto.Account
	key     ssh.PublicKey
}

// forge signs f's grant for server s with the edge's real key, sends the
// open on s's control channel as the edge would, and returns the server's
// refusal of the open, or the data socket the server attached for it.
func (h *harness) forge(t *testing.T, s *serverNode, f forgery) (net.Conn, error) {
	t.Helper()
	connID := edgeproto.NewConnID()
	now := time.Now()
	grant, err := edgeproto.SignGrant(h.edgeKey(), edgeproto.Grant{
		Issuer: h.relayURL, ServerID: s.id, ConnID: connID, Kind: f.kind, Account: f.account,
		DeviceID: "forged-" + connID, DeviceKey: edgeproto.DeviceKeyLine(f.key), DeviceLabel: "attacker laptop",
		IssuedAt: now, ExpiresAt: now.Add(edgeproto.GrantTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.sendOpen(t, s, edgeproto.Open{ConnID: connID, Ticket: edgeproto.NewToken(), Kind: f.kind, Grant: grant})
}

// sendOpen sends open to s as the edge and returns the data socket the
// server attaches, or its refusal.
func (h *harness) sendOpen(t *testing.T, s *serverNode, open edgeproto.Open) (net.Conn, error) {
	t.Helper()
	attached := make(chan net.Conn, 1)
	h.proxy.mu.Lock()
	h.proxy.forged[open.ConnID] = attached
	h.proxy.mu.Unlock()
	from := h.proxy.mark()
	h.proxy.inject(t, s.id, open, true)
	res := h.proxy.await(t, "the server's answer to "+open.ConnID, from, func(e logEntry) bool {
		r, ok := e.msg.(edgeproto.OpenResult)
		return ok && !e.fromEdge && r.ConnID == open.ConnID
	}).msg.(edgeproto.OpenResult)
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	select {
	case nc := <-attached:
		t.Cleanup(func() { _ = nc.Close() })
		return nc, nil
	case <-time.After(waitTimeout):
		t.Fatalf("server did not attach the data socket of %s", open.ConnID)
		return nil, nil
	}
}

// sshOver runs an SSH handshake with s over nc as user with signer,
// pinning s's host key, and returns the client or the handshake error
// with every banner the server sent.
func sshOver(nc net.Conn, s *serverNode, user string, signer ssh.Signer) (*ssh.Client, string, error) {
	var banner strings.Builder
	_ = nc.SetDeadline(time.Now().Add(waitTimeout))
	cc, chans, reqs, err := ssh.NewClientConn(nc, "edge", &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !edgeproto.HostKeyMatches(key, s.id) {
				return errors.New("host key is not server " + s.id)
			}
			return nil
		},
		BannerCallback: func(m string) error { banner.WriteString(m); return nil },
	})
	if err != nil {
		_ = nc.Close()
		return nil, banner.String(), err
	}
	_ = nc.SetDeadline(time.Time{})
	return ssh.NewClient(cc, chans, reqs), banner.String(), nil
}
