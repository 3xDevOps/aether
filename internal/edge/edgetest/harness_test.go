package edgetest

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edge"
	"github.com/3xDevOps/Aether/internal/edge/relay"
	"github.com/3xDevOps/Aether/internal/edgeagent"
	"github.com/3xDevOps/Aether/internal/edgeclient"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

const (
	serverDomain = "servers.example.test"
	// waitTimeout bounds every wait for something asynchronous: a
	// directory push, a reconnect, a connection closing.
	waitTimeout = 20 * time.Second
)

// harness is one edge, the servers enrolled with it and the clients
// signed in to it. Every request to the edge passes through proxy, which
// records the control channels and can inject messages into them, as a
// compromised or buggy edge would.
type harness struct {
	t       *testing.T
	github  *fakeGitHub
	edgeDir string
	origin  string
	proxy   *proxy

	mu        sync.Mutex
	skew      time.Duration
	frontAddr string
	backAddr  string
	node      *edgeNode
}

// edgeNode is one run of the edge.
type edgeNode struct {
	svc   *edge.Service
	relay *relay.Relay
	back  *http.Server
	front *http.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, github: newFakeGitHub(t), edgeDir: t.TempDir(),
		frontAddr: "127.0.0.1:0", backAddr: "127.0.0.1:0"}
	h.proxy = &proxy{changed: make(chan struct{})}
	h.startEdge()
	t.Cleanup(h.stopEdge)
	return h
}

// now is the edge's clock. advance moves it forward; tests use it to let
// the per-address rate limits refill, since every request comes from
// 127.0.0.1.
func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Now().Add(h.skew)
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.skew += d
}

// startEdge starts the edge and its proxy, on the same addresses as the
// previous run once there was one.
func (h *harness) startEdge() {
	t := h.t
	t.Helper()
	back, err := net.Listen("tcp", h.backAddr)
	if err != nil {
		t.Fatal(err)
	}
	front, err := net.Listen("tcp", h.frontAddr)
	if err != nil {
		t.Fatal(err)
	}
	h.backAddr, h.frontAddr = back.Addr().String(), front.Addr().String()
	h.origin = "http://" + h.frontAddr
	svc, err := edge.New(edge.Config{
		DataDir: h.edgeDir, Origin: h.origin, ServerDomain: serverDomain,
		GitHub: h.github.app(), Clock: h.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	rl, err := relay.New(context.Background(), svc.RelayConfig(0))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLink(rl)
	mux := http.NewServeMux()
	rl.Register(mux)
	mux.Handle("/", svc.Handler())
	quiet := log.New(io.Discard, "", 0)
	n := &edgeNode{
		svc: svc, relay: rl,
		back:  &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, ErrorLog: quiet},
		front: &http.Server{Handler: h.proxy.handler(h.backAddr), ReadHeaderTimeout: 10 * time.Second, ErrorLog: quiet},
	}
	go func() { _ = n.back.Serve(back) }()
	go func() { _ = n.front.Serve(front) }()
	h.mu.Lock()
	h.node = n
	h.mu.Unlock()
}

// stopEdge stops the edge cleanly: the relay drains every server first.
func (h *harness) stopEdge() {
	h.mu.Lock()
	n := h.node
	h.node = nil
	h.mu.Unlock()
	if n == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := n.relay.Shutdown(ctx); err != nil {
		h.t.Errorf("relay shutdown: %v", err)
	}
	_ = n.front.Close()
	h.proxy.closeLinks()
	_ = n.back.Close()
	if err := n.svc.Close(); err != nil {
		h.t.Errorf("edge store: %v", err)
	}
}

func (h *harness) relay() *relay.Relay {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.node.relay
}

// edgeKey reads the edge's signing key from its data directory, for tests
// that play a compromised edge.
func (h *harness) edgeKey() ed25519.PrivateKey {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.edgeDir, "edge_key"))
	if err != nil {
		h.t.Fatal(err)
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		h.t.Fatal(err)
	}
	return *raw.(*ed25519.PrivateKey)
}

// eventually polls cond until it returns nil or waitTimeout passes.
func eventually(t *testing.T, what string, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		err := cond()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: still %v after %s", what, err, waitTimeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// fakeGitHub is GitHub's OAuth and user API. It checks the PKCE verifier
// on every exchange and signs each browser in as the user the test named
// when that browser reached the authorization page.
type fakeGitHub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	codes  map[string]pendingCode
	tokens map[string]ghUser
}

type pendingCode struct {
	challenge string
	user      ghUser
}

// ghUser is a GitHub account. Its email is primary and verified.
type ghUser struct {
	ID    int64
	Login string
	Email string
}

var (
	alice = ghUser{ID: 1001, Login: "alice", Email: "alice@example.test"}
	bob   = ghUser{ID: 1002, Login: "bob", Email: "bob@example.test"}
	carol = ghUser{ID: 1003, Login: "carol", Email: "carol@example.test"}
	dave  = ghUser{ID: 1004, Login: "dave", Email: "dave@example.test"}
	erin  = ghUser{ID: 1005, Login: "erin", Email: "erin@example.test"}
)

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{codes: map[string]pendingCode{}, tokens: map[string]ghUser{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		p, ok := g.codes[r.FormValue("code")]
		delete(g.codes, r.FormValue("code"))
		token := edgeproto.NewToken()
		if ok {
			g.tokens[token] = p.user
		}
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok || !edgeproto.VerifyPKCE(p.challenge, r.FormValue("code_verifier")) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token, "token_type": "bearer"})
	})
	user := func(r *http.Request) (ghUser, bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		u, ok := g.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		return u, ok
	}
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		u, ok := user(r)
		if !ok {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": u.ID, "login": u.Login, "name": u.Login})
	})
	mux.HandleFunc("GET /user/emails", func(w http.ResponseWriter, r *http.Request) {
		u, ok := user(r)
		if !ok {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"email": u.Email, "primary": true, "verified": true}})
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) app() *edge.OAuthApp {
	return &edge.OAuthApp{
		ClientID: "fake-client-id", ClientSecret: "fake-client-secret",
		AuthURL: g.srv.URL + "/authorize", TokenURL: g.srv.URL + "/token", APIURL: g.srv.URL,
	}
}

// authorize plays user approving the sign-in at GitHub: it reads the
// authorization URL the edge redirected to and returns the callback query.
func (g *fakeGitHub) authorize(location string, user ghUser) (url.Values, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		return nil, fmt.Errorf("authorization URL %s has no S256 PKCE challenge", location)
	}
	code := edgeproto.NewToken()
	g.mu.Lock()
	g.codes[code] = pendingCode{challenge: q.Get("code_challenge"), user: user}
	g.mu.Unlock()
	return url.Values{"code": {code}, "state": {q.Get("state")}}, nil
}

// browser is a person's browser on the edge's pages.
type browser struct {
	h      *harness
	client *http.Client
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// signIn signs a new browser in to the edge as user.
func (h *harness) signIn(user ghUser) (*browser, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	b := &browser{h: h, client: &http.Client{
		Jar: jar, Timeout: waitTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	resp, _, err := b.do(http.MethodGet, "/signin/github?next=/device", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusFound {
		return nil, fmt.Errorf("start sign-in: %s", resp.Status)
	}
	q, err := h.github.authorize(resp.Header.Get("Location"), user)
	if err != nil {
		return nil, err
	}
	resp, body, err := b.do(http.MethodGet, "/signin/github/callback?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSeeOther {
		return nil, fmt.Errorf("sign-in callback: %s\n%s", resp.Status, body)
	}
	return b, nil
}

func (b *browser) do(method, path string, form url.Values) (*http.Response, string, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.h.origin+path, body)
	if err != nil {
		return nil, "", err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	data, err := io.ReadAll(resp.Body)
	return resp, string(data), err
}

// post submits a form from a signed-in page, with its CSRF token.
func (b *browser) post(path string, form url.Values) (*http.Response, string, error) {
	_, page, err := b.do(http.MethodGet, "/device", nil)
	if err != nil {
		return nil, "", err
	}
	m := csrfField.FindStringSubmatch(page)
	if m == nil {
		return nil, "", fmt.Errorf("no CSRF token on /device:\n%s", page)
	}
	form.Set("csrf", m[1])
	return b.do(http.MethodPost, path, form)
}

// approveDevice confirms the device sign-in showing userCode.
func (b *browser) approveDevice(userCode string) error {
	if resp, page, err := b.post("/device", url.Values{"user_code": {userCode}}); err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("enter code %s: %v %v\n%s", userCode, err, resp, page)
	}
	resp, page, err := b.post("/device/confirm", url.Values{"user_code": {userCode}, "decision": {"approve"}})
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(page, "is signed in") {
		return fmt.Errorf("approve code %s: %v %v\n%s", userCode, err, resp, page)
	}
	return nil
}

// client is one Aether client install: its own config directory with its
// device key and device token. dir is what cli.Dir returns when
// cli.ConfigDirEnv names its parent.
type client struct {
	user ghUser
	dir  string
	edge *edgeclient.Client
}

// login signs one new client install in as each user, as `aether login`
// does, confirming each code in that user's browser. The installs sign in
// concurrently: each waits out the edge's polling interval.
func (h *harness) login(users ...ghUser) []*client {
	t := h.t
	t.Helper()
	clients := make([]*client, len(users))
	errs := make([]error, len(users))
	var wg sync.WaitGroup
	for i, u := range users {
		dir := filepath.Join(t.TempDir(), "aether")
		ec, err := edgeclient.New(dir, h.origin)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = &client{user: u, dir: dir, edge: ec}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
			defer cancel()
			l, err := ec.StartLogin(ctx, fmt.Sprintf("%s-laptop-%d", u.Login, i))
			if err != nil {
				errs[i] = err
				return
			}
			b, err := h.signIn(u)
			if err == nil {
				err = b.approveDevice(l.UserCode)
			}
			if err != nil {
				errs[i] = err
				return
			}
			s, err := ec.Wait(ctx, l)
			if err == nil && s.Account.Subject != fmt.Sprint(u.ID) {
				err = fmt.Errorf("signed in as %+v, want %s", s.Account, u.Login)
			}
			errs[i] = err
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("login: %v", err)
	}
	return clients
}

// link is how a client links a server it reaches through the edge only.
func (h *harness) link(s *serverNode) cli.Config {
	return cli.Config{ServerID: s.id, EdgeURL: h.origin}
}

// dial connects c to the server of link with the real client dialer.
// The dialer finds the device key and token in the config directory
// cli.ConfigDirEnv names, so tests that dial never run in parallel.
func (h *harness) dial(c *client, link cli.Config) (*ssh.Client, error) {
	h.t.Setenv(cli.ConfigDirEnv, filepath.Dir(c.dir))
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	return cli.DialLinked(ctx, link, "aether")
}

func (h *harness) mustDial(c *client, s *serverNode) *ssh.Client {
	h.t.Helper()
	sc, err := h.dial(c, h.link(s))
	if err != nil {
		h.t.Fatalf("%s to server %s: %v", c.user.Login, s.id, err)
	}
	h.t.Cleanup(func() { _ = sc.Close() })
	return sc
}

// control opens the JSON-RPC control channel as c on s, over the edge.
func (h *harness) control(c *client, s *serverNode) *protocol.Client {
	t := h.t
	t.Helper()
	t.Setenv(cli.ConfigDirEnv, filepath.Dir(c.dir))
	conn, err := cli.Dial(h.link(s))
	if err != nil {
		t.Fatalf("%s to server %s: %v", c.user.Login, s.id, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctl, err := conn.Control()
	if err != nil {
		t.Fatalf("control channel: %v", err)
	}
	return ctl
}

func call[T any](t *testing.T, ctl *protocol.Client, method string, params any) T {
	t.Helper()
	var out T
	if err := ctl.Call(method, params, &out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

// claim presents code as c. The edge allows five claims a minute from one
// address, and every test request comes from 127.0.0.1, so each claim
// first moves the edge's clock past that window.
func (h *harness) claim(c *client, code string) (edgeproto.ClaimResponse, error) {
	h.advance(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	return c.edge.Claim(ctx, code)
}

// claimServer makes c the owner of s.
func (h *harness) claimServer(c *client, s *serverNode) {
	h.t.Helper()
	res, err := h.claim(c, s.claimCode(h.t, time.Now()))
	if err != nil || res.ServerID != s.id {
		h.t.Fatalf("%s claims %s: %+v %v", c.user.Login, s.id, res, err)
	}
	eventually(h.t, "claimed server online", func() error {
		if !h.relay().Online(s.id) {
			return errors.New("not online")
		}
		return nil
	})
}

// The relayed paths these tests drive never reach git, terminals or runs.
// member.remove stops the member's terminal, of which there is none.
type (
	noGit  struct{ sshd.GitTransport }
	noPTY  struct{ sshd.PTYAttacher }
	noRuns struct{ sshd.RunController }
)

func (noRuns) StopTerminal(context.Context, domain.MemberID) error { return nil }

// serverNode is one Aether server: sshd over a real store, and the edge
// agent enrolled with the harness's edge.
type serverNode struct {
	id    string
	dir   string
	edge  string
	addr  string
	db    *store.DB
	ssh   *sshd.Server
	agent *edgeagent.Agent
}

func (h *harness) newServer() *serverNode {
	t := h.t
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	hostKeyPath := filepath.Join(dir, "ssh", "host_ed25519_key")
	srv, err := sshd.New(sshd.Config{
		Addr: "127.0.0.1:0", HostKeyPath: hostKeyPath, Store: db, Bus: bus,
		Git: noGit{}, PTY: noPTY{}, Runs: noRuns{},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := sshd.LoadOrCreateHostKey(hostKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := edgeagent.New(edgeagent.Config{EdgeURL: h.origin, DataDir: dir, HostKey: hostKey, SSH: srv})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served, ran := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(served)
		if err := srv.Serve(ctx); err != nil {
			t.Errorf("sshd: %v", err)
		}
	}()
	go func() {
		defer close(ran)
		agent.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-ran
		_ = srv.Close()
		<-served
		_ = bus.Close()
		_ = db.Close()
	})
	s := &serverNode{id: agent.ServerID(), dir: dir, edge: h.origin, db: db, ssh: srv, agent: agent}
	eventually(t, "server enrolled", func() error {
		st, ok, err := s.state(t).Status()
		switch {
		case err != nil:
			return err
		case !ok || !st.Connected:
			return fmt.Errorf("agent status %+v", st)
		}
		return nil
	})
	s.addr = srv.Addr().String()
	return s
}

// state is the server's edge agent state with the harness's edge.
func (s *serverNode) state(t *testing.T) *edgeagent.State {
	t.Helper()
	st, err := edgeagent.OpenState(s.dir, s.edge)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// claimCode issues a claim code as `aether-server edge claim-code` does,
// valid from issued for edgeproto.ClaimCodeTTL.
func (s *serverNode) claimCode(t *testing.T, issued time.Time) string {
	t.Helper()
	code, _, err := s.state(t).IssueClaimCode(s.id, issued)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// proxy stands between the edge and everything that dials it. It passes
// every request through unchanged, and additionally decodes the control
// channels it carries, recording each message and letting a test inject
// messages in either direction.
type proxy struct {
	mu      sync.Mutex
	blocked bool
	links   []*controlLink
	log     []logEntry
	changed chan struct{}
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

func (p *proxy) handler(backAddr string) http.Handler {
	target := &url.URL{Scheme: "http", Host: backAddr}
	rp := &httputil.ReverseProxy{
		Rewrite:  func(r *httputil.ProxyRequest) { r.SetURL(target) },
		ErrorLog: log.New(io.Discard, "", 0),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == edgeproto.PathServerControl {
			p.serveControl(w, r, backAddr)
			return
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
			p.record(l, fromEdge, m)
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

func (p *proxy) record(l *controlLink, fromEdge bool, m edgeproto.Message) {
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
	p.log = append(p.log, logEntry{serverID: l.serverID, fromEdge: fromEdge, msg: m})
	close(p.changed)
	p.changed = make(chan struct{})
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
