package edgetest

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	edgeagent "github.com/3xDevOps/Aether/internal/edge/agent"
	edgeclient "github.com/3xDevOps/Aether/internal/edge/client"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/edge/relay"
	edge "github.com/3xDevOps/Aether/internal/edge/service"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// waitTimeout bounds every wait for something asynchronous: a directory
// push, a reconnect, a connection closing.
const waitTimeout = 20 * time.Second

// policies are the two access policies a test runs under when it holds
// for both.
var policies = []edgeproto.AccessPolicy{edgeproto.PolicyAccount, edgeproto.PolicyApprovedDevices}

// harness is one edge, the servers enrolled with it and the clients
// signed in to it. The edge serves two origins from one process, as in
// production: the sign-in origin on the host name localhost and the relay
// origin on 127.0.0.1, swapped by renameHosts. Everything that dials the edge reaches it through
// proxy, which plays the network in between and, when a test asks it
// to, a compromised edge: it holds the edge's signing key and can forge,
// replay, alter and inject control messages and grants.
type harness struct {
	t       *testing.T
	github  *fakeGitHub
	edgeDir string
	// signinURL and relayURL are the edge's origins, both served by the
	// proxy's front listener.
	signinURL string
	relayURL  string
	proxy     *proxy
	// renamed swaps the host names of the two origins.
	renamed bool
	// proxies, when set, puts the edge behind relay.Forwarded with these
	// trusted proxy networks, as --trusted-proxies does.
	proxies []netip.Prefix

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
	h.proxy = newProxy()
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
	_, port, _ := net.SplitHostPort(h.frontAddr)
	h.signinURL = "http://localhost:" + port
	h.relayURL = "http://127.0.0.1:" + port
	if h.renamed {
		h.signinURL, h.relayURL = h.relayURL, h.signinURL
	}
	svc, err := edge.New(edge.Config{
		DataDir: h.edgeDir, SigninOrigin: h.signinURL, RelayOrigin: h.relayURL,
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
	handler := svc.Handler()
	if h.proxies != nil {
		handler = rl.Forwarded(h.proxies, handler)
	}
	quiet := log.New(io.Discard, "", 0)
	n := &edgeNode{
		svc: svc, relay: rl,
		back:  &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ErrorLog: quiet},
		front: &http.Server{Handler: h.proxy.handler(h.backAddr), ReadHeaderTimeout: 10 * time.Second, ErrorLog: quiet},
	}
	go func() { _ = n.back.Serve(back) }()
	go func() { _ = n.front.Serve(front) }()
	h.mu.Lock()
	h.node = n
	h.mu.Unlock()
}

// renameHosts restarts the edge, with the same key and data, under the
// other host names: its relay origin changes, as when its operator moves
// it to a new host name.
func (h *harness) renameHosts() {
	h.stopEdge()
	h.renamed = !h.renamed
	h.startEdge()
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

// edgeKey reads the edge's signing key from its data directory: what an
// attacker holding the edge has.
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

// edgeStore opens a second handle on the edge's database, to read what
// the edge recorded.
func (h *harness) edgeStore() *edgestore.Store {
	h.t.Helper()
	st, err := edgestore.Open(filepath.Join(h.edgeDir, "edge.db"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = st.Close() })
	return st
}

// edgeOwner is the owner the edge records for serverID, nil when it has
// none.
func (h *harness) edgeOwner(serverID string) *edgeproto.AccountInfo {
	h.t.Helper()
	srv, err := h.edgeStore().Server(context.Background(), serverID)
	if err != nil {
		h.t.Fatalf("edge record of server %s: %v", serverID, err)
	}
	return srv.Owner
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
// when that browser reached the authorization page. Naming a user is all
// it takes, so it also plays a provider account someone took over.
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
	// mallory is an attacker's own GitHub account.
	mallory = ghUser{ID: 6666, Login: "mallory", Email: "mallory@example.test"}
)

// account is u as grants name it.
func (u ghUser) account() edgeproto.Account {
	return edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: fmt.Sprint(u.ID), Login: u.Login,
		Email: u.Email, Name: u.Login, IdentityAt: time.Now()}
}

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
		if !ok || s256(r.FormValue("code_verifier")) != p.challenge {
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

// s256 is the PKCE S256 challenge of verifier (RFC 7636).
func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// browser is a person's browser on the edge's sign-in pages.
type browser struct {
	h      *harness
	client *http.Client
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// signIn signs a new browser in to the edge as user. Signing in also
// refreshes the account's identity, which deleting it requires.
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
	req, err := http.NewRequest(method, b.h.signinURL+path, body)
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

// signer is the install's device key.
func (c *client) signer(t *testing.T) ssh.Signer {
	t.Helper()
	s, err := edgeclient.DeviceSigner(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// login signs one new client install in as each user, as `aether login`
// does, confirming each code in that user's browser. Installs sign in
// concurrently, loginBatch at a time: each waits out the edge's polling
// interval, and code entries from one address are rate-limited.
func (h *harness) login(users ...ghUser) []*client {
	h.t.Helper()
	var clients []*client
	for len(users) > 0 {
		n := min(len(users), loginBatch)
		clients = append(clients, h.loginBatch(len(clients), users[:n])...)
		users = users[n:]
	}
	return clients
}

const loginBatch = 4

func (h *harness) loginBatch(first int, users []ghUser) []*client {
	t := h.t
	t.Helper()
	// Sign-ins start from one address; let its budget refill.
	h.advance(5 * time.Minute)
	clients := make([]*client, len(users))
	errs := make([]error, len(users))
	var wg sync.WaitGroup
	for i, u := range users {
		dir := filepath.Join(t.TempDir(), "aether")
		ec, err := edgeclient.New(dir, h.relayURL)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = &client{user: u, dir: dir, edge: ec}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
			defer cancel()
			l, err := ec.StartLogin(ctx, fmt.Sprintf("%s-laptop-%d", u.Login, first+i))
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
	return cli.Config{ServerID: s.id, EdgeURL: h.relayURL}
}

// configDirMu serializes the client operations that read the config
// directory from cli.ConfigDirEnv, which is process-wide, so the tests
// can otherwise run in parallel.
var configDirMu sync.Mutex

// as runs f as c: with cli.ConfigDirEnv naming c's config directory,
// where the real client finds its device key and device token.
func as[T any](c *client, f func() (T, error)) (T, error) {
	configDirMu.Lock()
	defer configDirMu.Unlock()
	prev, had := os.LookupEnv(cli.ConfigDirEnv)
	_ = os.Setenv(cli.ConfigDirEnv, filepath.Dir(c.dir))
	defer func() {
		if had {
			_ = os.Setenv(cli.ConfigDirEnv, prev)
		} else {
			_ = os.Unsetenv(cli.ConfigDirEnv)
		}
	}()
	return f()
}

// dial connects c to the server of link with the real client dialer.
func (h *harness) dial(c *client, link cli.Config) (*ssh.Client, error) {
	return as(c, func() (*ssh.Client, error) {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		return cli.DialLinked(ctx, link, "aether")
	})
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

// tryControl opens the JSON-RPC control channel as c on s, over the edge.
func (h *harness) tryControl(c *client, s *serverNode) (*protocol.Client, error) {
	conn, err := as(c, func() (*cli.Conn, error) { return cli.Dial(h.link(s)) })
	if err != nil {
		return nil, err
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	return conn.Control()
}

func (h *harness) control(c *client, s *serverNode) *protocol.Client {
	h.t.Helper()
	ctl, err := h.tryControl(c, s)
	if err != nil {
		h.t.Fatalf("%s to server %s: %v", c.user.Login, s.id, err)
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

// claim links the server code names by claiming it as c, as
// `aether link --claim <code>` does. The edge allows five claim
// connections a minute from one address and one account, and every test
// request comes from 127.0.0.1, so each claim first moves the edge's
// clock past that window.
func (h *harness) claim(c *client, code string) (cli.LinkResult, error) {
	h.advance(time.Minute)
	res, err := as(c, func() (cli.LinkResult, error) {
		return cli.Link(cli.LinkOptions{EdgeURL: h.relayURL, Claim: code}, cli.Config{})
	})
	if err == nil {
		h.t.Cleanup(func() { _ = res.Conn.Close() })
	}
	return res, err
}

// claimServer makes c the owner of s with a fresh claim code.
func (h *harness) claimServer(c *client, s *serverNode) {
	h.t.Helper()
	res, err := h.claim(c, s.claimCode(h.t, time.Now()))
	if err != nil || res.Config.ServerID != s.id || res.Info.Member.Role != string(domain.RoleAdmin) {
		h.t.Fatalf("%s claims %s: %+v %v", c.user.Login, s.id, res.Info.Member, err)
	}
	eventually(h.t, "the edge records the claim", func() error {
		if !h.relay().Online(s.id) {
			return errors.New("not online")
		}
		if o, err := h.edgeStore().Server(context.Background(), s.id); err != nil || o.Owner == nil || o.Owner.Subject != fmt.Sprint(c.user.ID) {
			return fmt.Errorf("owner %+v, %v", o.Owner, err)
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
// agent enrolled with the harness's edge, under an access policy.
type serverNode struct {
	h      *harness
	id     string
	dir    string
	policy edgeproto.AccessPolicy
	addr   string
	db     *store.DB
	ssh    *sshd.Server
	agent  *edgeagent.Agent
	stop   func()
}

func (h *harness) newServer(policy edgeproto.AccessPolicy) *serverNode {
	h.t.Helper()
	s := &serverNode{h: h, dir: h.t.TempDir(), policy: policy, addr: "127.0.0.1:0"}
	s.start()
	h.t.Cleanup(func() { s.stop() })
	return s
}

// start runs sshd and the edge agent over the server's data directory,
// on the address of the previous run once there was one, and waits until
// the agent enrolled.
func (s *serverNode) start() {
	t := s.h.t
	t.Helper()
	db, err := storetest.Open(filepath.Join(s.dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	hostKeyPath := filepath.Join(s.dir, "ssh", "host_ed25519_key")
	srv, err := sshd.New(sshd.Config{
		Addr: s.addr, HostKeyPath: hostKeyPath, Store: db, Bus: bus,
		Git: noGit{}, PTY: noPTY{}, Runs: noRuns{},
		InvitesDir: filepath.Join(s.dir, "invites"), EdgeAccess: s.policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := sshd.LoadOrCreateHostKey(hostKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := edgeagent.New(edgeagent.Config{EdgeURL: s.h.relayURL, DataDir: s.dir, HostKey: hostKey, SSH: srv, AccessPolicy: s.policy})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetEdgeOwner(agent)
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
	var once sync.Once
	s.stop = func() {
		once.Do(func() {
			cancel()
			<-ran
			_ = srv.Close()
			<-served
			_ = bus.Close()
			_ = db.Close()
		})
	}
	s.id, s.db, s.ssh, s.agent = agent.ServerID(), db, srv, agent
	s.waitEnrolled()
	s.addr = srv.Addr().String()
}

// restart stops the server and starts it again under policy, as editing
// edge-access in its configuration and restarting it does.
func (s *serverNode) restart(policy edgeproto.AccessPolicy) {
	s.stop()
	s.policy = policy
	s.start()
}

func (s *serverNode) waitEnrolled() {
	eventually(s.h.t, "server enrolled", func() error {
		st, ok, err := s.state().Status()
		switch {
		case err != nil:
			return err
		case !ok || !st.Connected:
			return fmt.Errorf("agent status %+v", st)
		}
		return nil
	})
}

// state is the server's edge agent state.
func (s *serverNode) state() *edgeagent.State { return edgeagent.OpenState(s.dir) }

// claimCode issues a claim code as `aether-server edge claim-code` does,
// valid from issued for edgeproto.ClaimCodeTTL.
func (s *serverNode) claimCode(t *testing.T, issued time.Time) string {
	t.Helper()
	code, _, err := s.state().IssueClaimCode(s.id, "", issued)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// recoveryCode issues a claim code for the existing admin member admin,
// as `aether-server edge claim-code --admin <member id>` does.
func (s *serverNode) recoveryCode(t *testing.T, admin domain.MemberID) string {
	t.Helper()
	code, _, err := s.state().IssueClaimCode(s.id, string(admin), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// claimAttemptsLeft is what remains of the server's claim code.
func (s *serverNode) claimAttemptsLeft(t *testing.T) int {
	t.Helper()
	c, ok, err := s.state().ClaimCode()
	if err != nil || !ok {
		t.Fatalf("claim code: %+v %v %v", c, ok, err)
	}
	return c.AttemptsLeft
}

// console opens the server's store the way `sudo aether-server device
// approve` and `device review` do: a second process on the same database.
func (s *serverNode) console(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(s.dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// consoleApprove approves the device waiting with code on the machine,
// as `sudo aether-server device approve <code>` does.
func (s *serverNode) consoleApprove(t *testing.T, code string) {
	t.Helper()
	db := s.console(t)
	dev, err := db.GetDeviceByApprovalCode(context.Background(), code)
	if err != nil {
		t.Fatalf("device waiting with code %s: %v", code, err)
	}
	if _, err := sshd.ApproveDevice(context.Background(), db, db, dev, ""); err != nil {
		t.Fatal(err)
	}
}

// local is the in-process client the tailnet-hosted dashboard uses, acting
// as member: a tailnet identity, independent of the edge.
func (s *serverNode) local(t *testing.T, member domain.MemberID, method string, params, out any) error {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, perr := s.ssh.Local(member).Call(context.Background(), method, raw)
	if perr != nil {
		return perr
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(res, out)
}

// memberOf is the member bound to u's GitHub identity on s.
func (s *serverNode) memberOf(t *testing.T, u ghUser) *domain.Member {
	t.Helper()
	m, err := s.db.GetMemberByIdentity(context.Background(), edgeproto.ProviderGitHub, fmt.Sprint(u.ID))
	if err != nil {
		t.Fatalf("member of %s on %s: %v", u.Login, s.id, err)
	}
	return m
}

// devicesOf lists the devices of member on s.
func (s *serverNode) devicesOf(t *testing.T, member domain.MemberID) []*domain.Device {
	t.Helper()
	devs, err := s.db.ListDevices(context.Background(), member)
	if err != nil {
		t.Fatal(err)
	}
	return devs
}

// snapshot is who can do what on a server: every member with its role
// and edge identities, and every device with its status. Tests compare
// snapshots to show an attack changed nothing.
type snapshot struct {
	members map[domain.MemberID]domain.Role
	// identities maps provider/subject to its member.
	identities map[string]domain.MemberID
	// devices maps each device key to its member, identity and status.
	devices map[string]string
}

func (s *serverNode) snapshot(t *testing.T) snapshot {
	t.Helper()
	ctx := context.Background()
	out := snapshot{members: map[domain.MemberID]domain.Role{}, identities: map[string]domain.MemberID{}, devices: map[string]string{}}
	members, err := s.db.ListMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		out.members[m.ID] = m.Role
	}
	ids, err := s.db.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		out.identities[id.Provider+"/"+id.Subject] = id.Member
	}
	devs, err := s.db.ListDevices(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devs {
		out.devices[d.Credential] = fmt.Sprintf("%s %s/%s %s", d.Member, d.Provider, d.Subject, d.Status)
	}
	return out
}

// admins are the members with the admin role.
func (sn snapshot) admins() []domain.MemberID {
	var out []domain.MemberID
	for id, role := range sn.members {
		if role == domain.RoleAdmin {
			out = append(out, id)
		}
	}
	return out
}

// approvalCode is the code a pending device's refusal names.
var approvalCode = regexp.MustCompile(`aether device approve (\S+)`)

// approve approves the device code names through ctl as clients do: it
// looks the code up, then approves the device the lookup named.
func approve(t *testing.T, ctl *protocol.Client, code string) protocol.MemberDeviceResult {
	t.Helper()
	found := call[protocol.MemberDeviceLookupResult](t, ctl, protocol.MethodMemberDeviceLookup, protocol.MemberDeviceLookupParams{Code: code})
	return call[protocol.MemberDeviceResult](t, ctl, protocol.MethodMemberDeviceApprove,
		protocol.MemberDeviceApproveParams{Code: code, DeviceID: found.Device.ID})
}

// waitingCode returns the approval code in err, which must be the
// server's refusal of a device waiting for approval.
func waitingCode(t *testing.T, what string, err error) string {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: connected, want a device waiting for approval", what)
	}
	m := approvalCode.FindStringSubmatch(err.Error())
	if m == nil || !strings.Contains(err.Error(), "is waiting for approval") ||
		!strings.Contains(err.Error(), "sudo aether-server device approve "+m[1]) {
		t.Fatalf("%s: %v, want a device waiting for approval with the commands that approve it", what, err)
	}
	return m[1]
}

func wantRefusal(t *testing.T, what string, err error, want edgeproto.Refusal) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: %v, want %q", what, err, want)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// closedWithin waits for sc to close, failing past limit, and logs how
// long it took from since.
func closedWithin(t *testing.T, what string, sc *ssh.Client, since time.Time, limit time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = sc.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s: connection still open after %s", what, limit)
	}
	t.Logf("%s closed the live connection in %s", what, time.Since(since).Round(time.Millisecond))
}

// waitRole waits until c's server list shows serverID with role, or
// without serverID when role is empty: the edge has the directory the
// server pushed after a change.
func waitRole(t *testing.T, c *client, serverID, role string) {
	t.Helper()
	eventually(t, fmt.Sprintf("%s's role on %s to be %q", c.user.Login, serverID, role), func() error {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		servers, err := c.edge.Servers(ctx)
		if err != nil {
			return err
		}
		got := ""
		for _, s := range servers {
			if s.ID == serverID {
				got = s.Role
			}
		}
		if got != role {
			return fmt.Errorf("role %q", got)
		}
		return nil
	})
}

func invite(t *testing.T, ctl *protocol.Client, p protocol.MemberInvitationCreateParams) string {
	t.Helper()
	return call[protocol.MemberInvitationResult](t, ctl, protocol.MethodMemberInvitationCreate, p).Invitation.ID
}

// inviteLogin invites u by GitHub login with role and waits until the
// edge lists the server for u.
func inviteLogin(t *testing.T, ctl *protocol.Client, c *client, serverID, role string) {
	t.Helper()
	invite(t, ctl, protocol.MemberInvitationCreateParams{Login: c.user.Login, Role: role})
	waitRole(t, c, serverID, role)
}

// deviceIDOf is the id of the device on s whose key is c's device key.
func (s *serverNode) deviceIDOf(t *testing.T, c *client) domain.DeviceID {
	t.Helper()
	dev, err := s.db.GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(c.signer(t).PublicKey()))
	if err != nil {
		t.Fatalf("device of %s on %s: %v", c.user.Login, s.id, err)
	}
	return dev.ID
}

// deviceStatus is the status on s of c's device key, empty when s has
// none.
func (s *serverNode) deviceStatus(t *testing.T, c *client) domain.DeviceStatus {
	t.Helper()
	dev, err := s.db.GetDeviceByCredential(context.Background(), edgeproto.DeviceKeyLine(c.signer(t).PublicKey()))
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return dev.Status
}

// join invites c to s by GitHub login with role through ctl, an admin's
// control channel, and approves c's first device from it when s admits
// approved devices only.
func (h *harness) join(ctl *protocol.Client, c *client, s *serverNode, role string) {
	t := h.t
	t.Helper()
	inviteLogin(t, ctl, c, s.id, role)
	_, err := h.dial(c, h.link(s))
	if s.policy == edgeproto.PolicyApprovedDevices {
		approve(t, ctl, waitingCode(t, c.user.Login+"'s first device", err))
	} else if err != nil {
		t.Fatalf("%s joins %s: %v", c.user.Login, s.id, err)
	}
}

// deleteAccount deletes c's account on the edge's Account page, from a
// browser that has just signed in, which the edge requires.
func (h *harness) deleteAccount(c *client) {
	h.t.Helper()
	b, err := h.signIn(c.user)
	if err != nil {
		h.t.Fatal(err)
	}
	sum, err := c.edge.Account(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	resp, page, err := b.post("/account/delete", url.Values{"confirm": {sum.Confirm}})
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(page, "The account is deleted") {
		h.t.Fatalf("delete %s's account: %v %v\n%s", c.user.Login, err, resp, page)
	}
}

// directLink is c's link to s by its address alone.
func directLink(s *serverNode) cli.Config {
	return cli.Config{ServerID: s.id, Addr: s.addr}
}

// approveForDirect approves c's device on s through ctl, an admin's
// control channel, when it is not approved yet: the direct path accepts
// approved devices only, and its refusal shows the code.
func (h *harness) approveForDirect(ctl *protocol.Client, c *client, s *serverNode) {
	t := h.t
	t.Helper()
	if s.deviceStatus(t, c) == domain.DeviceApproved {
		return
	}
	_, err := h.dial(c, directLink(s))
	m := approvalCode.FindStringSubmatch(errString(err))
	if m == nil || !strings.Contains(err.Error(), "a direct connection accepts approved devices only") {
		t.Fatalf("%s's registered device directly: %v, want refused until approved", c.user.Login, err)
	}
	approve(t, ctl, m[1])
}
