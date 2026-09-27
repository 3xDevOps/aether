package servergw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/webgate"
)

const (
	testEdge     = "https://edge.example.test"
	testDomain   = "servers.example.test"
	testServerID = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherServer  = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var (
	ada   = edgeproto.Account{Provider: "github", Subject: "1001", Login: "ada"}
	grace = edgeproto.Account{Provider: "github", Subject: "1002", Login: "grace"}
	linus = edgeproto.Account{Provider: "github", Subject: "1003", Login: "linus"}
)

// The gateway paths these tests drive never reach git, terminals or runs.
type (
	noGit  struct{ sshd.GitTransport }
	noPTY  struct{ sshd.PTYAttacher }
	noRuns struct{ sshd.RunController }
)

// fakeAgent stands in for the edge agent: its web listener is a local TCP
// listener, and it redeems the codes an edge issued, each once, for the
// PKCE challenge the code was issued with.
type fakeAgent struct {
	ln net.Listener

	mu       sync.Mutex
	codes    map[string]issuedCode
	redeemed int
}

type issuedCode struct {
	challenge string
	grant     edgeproto.Grant
}

func (a *fakeAgent) ServerID() string          { return testServerID }
func (a *fakeAgent) WebListener() net.Listener { return a.ln }

func (a *fakeAgent) RedeemWebCode(_ context.Context, code, verifier string) (edgeproto.Grant, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.redeemed++
	issued, ok := a.codes[code]
	delete(a.codes, code)
	if !ok || !edgeproto.VerifyPKCE(issued.challenge, verifier) {
		return edgeproto.Grant{}, errors.New("edgeagent: https://edge.example.test refused the web sign-in code: code is unknown, used or expired")
	}
	return issued.grant, nil
}

// issue is the edge's authorize step: a one-time code for account, bound
// to the challenge the server sent, granting a sign-in to serverID.
func (a *fakeAgent) issue(challenge, serverID string, account edgeproto.Account) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	code := edgeproto.NewToken()
	now := time.Now()
	a.codes[code] = issuedCode{challenge: challenge, grant: edgeproto.Grant{
		ServerID: serverID, ConnID: edgeproto.NewConnID(), Kind: edgeproto.KindWeb, Account: account,
		DeviceID: "edge-session", IssuedAt: now, ExpiresAt: now.Add(edgeproto.GrantTTL),
	}}
	return code
}

func (a *fakeAgent) redemptions() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.redeemed
}

type edgeEnv struct {
	t     *testing.T
	db    *store.DB
	ssh   *sshd.Server
	agent *fakeAgent
	edge  *Edge
	web   *httptest.Server

	mu   sync.Mutex
	skew time.Duration
}

func newEdgeEnv(t *testing.T, cfg EdgeConfig) *edgeEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	srv, err := sshd.New(sshd.Config{
		Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(dir, "host_key"), Store: db, Bus: bus,
		Git: noGit{}, PTY: noPTY{}, Runs: noRuns{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	agent := &fakeAgent{ln: ln, codes: map[string]issuedCode{}}
	cfg.SSH, cfg.Store, cfg.Agent = srv, db, agent
	cfg.EdgeURL, cfg.ServerDomain = testEdge, testDomain
	if cfg.CertDir == "" {
		cfg.CertDir = filepath.Join(dir, "certs")
	}
	cfg.Static = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Aether</title>")}}
	env := &edgeEnv{t: t, db: db, ssh: srv, agent: agent}
	e, err := newEdge(cfg, env.now, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	env.edge = e
	env.web = httptest.NewServer(e)
	t.Cleanup(env.web.Close)
	return env
}

// now is the gateway's clock: the real one moved by skew.
func (env *edgeEnv) now() time.Time {
	env.mu.Lock()
	defer env.mu.Unlock()
	return time.Now().Add(env.skew)
}

func (env *edgeEnv) advance(d time.Duration) {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.skew += d
}

// member makes account the bound identity of a member: the first becomes
// the admin by claim, later ones join through an invitation.
func (env *edgeEnv) member(account edgeproto.Account) domain.MemberID {
	env.t.Helper()
	ctx := context.Background()
	members, err := env.db.ListMembers(ctx)
	if err != nil {
		env.t.Fatal(err)
	}
	if len(members) == 0 {
		owner, claimErr := env.ssh.ClaimByEdge(ctx, account)
		if claimErr != nil {
			env.t.Fatal(claimErr)
		}
		return owner.ID
	}
	inv := &domain.Invitation{Provider: account.Provider, Login: account.Login, Role: domain.RoleCollaborator,
		CreatedBy: members[0].ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err = env.db.CreateInvitation(ctx, inv); err != nil {
		env.t.Fatal(err)
	}
	m, err := env.db.AcceptInvitation(ctx, inv.ID,
		&domain.Identity{Provider: account.Provider, Subject: account.Subject, Login: account.Login},
		&domain.Member{DisplayName: account.Login}, time.Now())
	if err != nil {
		env.t.Fatal(err)
	}
	return m.ID
}

func (env *edgeEnv) client() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (env *edgeEnv) origin() string { return env.web.URL }

// login runs GET /auth/login and returns the response, the state cookie
// and the authorize URL it redirected to.
func (env *edgeEnv) login(query string) (*http.Response, *http.Cookie, *url.URL) {
	env.t.Helper()
	resp, err := env.client().Get(env.web.URL + "/auth/login" + query)
	if err != nil {
		env.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		return resp, nil, nil
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		env.t.Fatal(err)
	}
	return resp, cookieNamed(resp, signinCookie), loc
}

func (env *edgeEnv) callback(state, code string, cookies ...*http.Cookie) (*http.Response, string) {
	env.t.Helper()
	q := url.Values{"state": {state}, "code": {code}}
	req, _ := http.NewRequest(http.MethodGet, env.web.URL+"/auth/callback?"+q.Encode(), nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := env.client().Do(req)
	if err != nil {
		env.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

// signIn runs the whole browser sign-in for account and returns the
// callback's response and body.
func (env *edgeEnv) signIn(account edgeproto.Account) (*http.Response, string) {
	env.t.Helper()
	_, state, loc := env.login("")
	if state == nil {
		env.t.Fatal("login set no state cookie")
	}
	code := env.agent.issue(loc.Query().Get("challenge"), testServerID, account)
	return env.callback(loc.Query().Get("state"), code, state)
}

// session signs account in and returns its session cookie.
func (env *edgeEnv) session(account edgeproto.Account) *http.Cookie {
	env.t.Helper()
	resp, body := env.signIn(account)
	c := cookieNamed(resp, sessionCookie)
	if c == nil {
		env.t.Fatalf("sign-in set no session cookie: %d %s", resp.StatusCode, body)
	}
	return c
}

// call posts server.info as the dashboard does, with origin as the
// Origin header ("" for none).
func (env *edgeEnv) call(origin string, session *http.Cookie) (int, webgate.ErrorBody) {
	env.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, env.web.URL+"/api/v1/"+protocol.MethodServerInfo, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if session != nil {
		req.AddCookie(session)
	}
	resp, err := env.client().Do(req)
	if err != nil {
		env.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body webgate.ErrorBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func (env *edgeEnv) device(token string) *domain.Device {
	env.t.Helper()
	dev, err := env.db.GetDeviceByCredential(context.Background(), edgeproto.HashToken(token))
	if err != nil {
		env.t.Fatal(err)
	}
	return dev
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func setCookieLine(resp *http.Response, name string) string {
	for _, line := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			return line
		}
	}
	return ""
}

func TestEdgeWithoutSessionAsksForSignIn(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	status, body := env.call(env.origin(), nil)
	if status != http.StatusUnauthorized || body.Error == nil || string(body.Error.Data) != `{"login":"/auth/login"}` {
		t.Fatalf("no session: %d %+v, want 401 with the login location", status, body.Error)
	}
	garbage := &http.Cookie{Name: sessionCookie, Value: edgeproto.NewToken()}
	if status, body = env.call(env.origin(), garbage); status != http.StatusUnauthorized || string(body.Error.Data) != `{"login":"/auth/login"}` {
		t.Fatalf("unknown session: %d %+v, want 401 with the login location", status, body.Error)
	}
	resp, err := http.Get(env.web.URL + "/runs")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "<title>Aether</title>") {
		t.Fatalf("page load without a session: %d %q, want the dashboard shell", resp.StatusCode, page)
	}
	for header, want := range map[string]string{
		"Strict-Transport-Security": "max-age=63072000",
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "no-referrer",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", csp)
	}
}

func TestEdgeSignIn(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	env.member(ada)

	resp, state, loc := env.login("")
	if state == nil || loc == nil {
		t.Fatalf("login: %d, want a redirect with a state cookie", resp.StatusCode)
	}
	if got, want := setCookieLine(resp, signinCookie), signinCookie+"="+state.Value+"; Path=/; Max-Age=600; HttpOnly; Secure; SameSite=Lax"; got != want {
		t.Errorf("state cookie:\n got %s\nwant %s", got, want)
	}
	q := loc.Query()
	if loc.Scheme+"://"+loc.Host+loc.Path != testEdge+edgeproto.PathAuthorize || q.Get("server") != testServerID ||
		!edgeproto.ValidToken(q.Get("state")) || !edgeproto.ValidToken(q.Get("challenge")) || q.Has("return") {
		t.Fatalf("authorize URL %s", loc)
	}
	if strings.Contains(loc.String(), strings.SplitN(state.Value, ".", 2)[1]) {
		t.Fatal("the authorize URL carries the PKCE verifier")
	}

	code := env.agent.issue(q.Get("challenge"), testServerID, ada)
	resp, body := env.callback(q.Get("state"), code, state)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("callback: %d %s, want a redirect to /", resp.StatusCode, body)
	}
	session := cookieNamed(resp, sessionCookie)
	if session == nil || !edgeproto.ValidToken(session.Value) {
		t.Fatal("callback set no session cookie")
	}
	if got, want := setCookieLine(resp, sessionCookie), sessionCookie+"="+session.Value+"; Path=/; Max-Age=34560000; HttpOnly; Secure; SameSite=Strict"; got != want {
		t.Errorf("session cookie:\n got %s\nwant %s", got, want)
	}
	if got, want := setCookieLine(resp, signinCookie), signinCookie+"=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax"; got != want {
		t.Errorf("state cookie not cleared:\n got %s\nwant %s", got, want)
	}
	dev := env.device(session.Value)
	if dev.Kind != domain.DeviceBrowser || dev.Status != domain.DeviceApproved || dev.Credential == session.Value {
		t.Fatalf("browser device %+v", dev)
	}

	status, _ := env.call(env.origin(), session)
	if status != http.StatusOK {
		t.Fatalf("server.info with the session: %d", status)
	}
	req, _ := http.NewRequest(http.MethodGet, env.web.URL+"/api/v1/capabilities", nil)
	req.AddCookie(session)
	capsResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var caps protocol.GatewayCapabilities
	_ = json.NewDecoder(capsResp.Body).Decode(&caps)
	_ = capsResp.Body.Close()
	if caps.Gateway != "edge" {
		t.Fatalf("capabilities gateway = %q, want edge", caps.Gateway)
	}
}

func TestEdgeLoginAppVariant(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	if _, _, loc := env.login("?return=app"); loc == nil || loc.Query().Get("return") != "app" {
		t.Fatalf("return=app: authorize URL %v, want return=app", loc)
	}
	if resp, state, _ := env.login("?return=https://evil.example"); resp.StatusCode != http.StatusBadRequest || state != nil {
		t.Fatalf("return=https://evil.example: %d, want 400 and no state cookie", resp.StatusCode)
	}
}

func TestEdgeCallbackRefusals(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	env.member(ada)

	t.Run("state mismatch", func(t *testing.T) {
		_, state, loc := env.login("")
		code := env.agent.issue(loc.Query().Get("challenge"), testServerID, ada)
		before := env.agent.redemptions()
		resp, body := env.callback(edgeproto.NewToken(), code, state)
		if resp.StatusCode != http.StatusBadRequest || cookieNamed(resp, sessionCookie) != nil {
			t.Fatalf("%d %s, want 400 and no session", resp.StatusCode, body)
		}
		if env.agent.redemptions() != before {
			t.Fatal("the code was redeemed despite the state mismatch")
		}
	})
	t.Run("no state cookie", func(t *testing.T) {
		_, _, loc := env.login("")
		code := env.agent.issue(loc.Query().Get("challenge"), testServerID, ada)
		if resp, body := env.callback(loc.Query().Get("state"), code); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%d %s, want 400", resp.StatusCode, body)
		}
	})
	t.Run("grant for another server", func(t *testing.T) {
		_, state, loc := env.login("")
		code := env.agent.issue(loc.Query().Get("challenge"), otherServer, ada)
		resp, body := env.callback(loc.Query().Get("state"), code, state)
		if resp.StatusCode != http.StatusForbidden || cookieNamed(resp, sessionCookie) != nil || !strings.Contains(body, otherServer) {
			t.Fatalf("%d %s, want 403 naming the other server and no session", resp.StatusCode, body)
		}
	})
	t.Run("replayed code", func(t *testing.T) {
		_, state, loc := env.login("")
		code := env.agent.issue(loc.Query().Get("challenge"), testServerID, ada)
		if resp, body := env.callback(loc.Query().Get("state"), code, state); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("first use: %d %s", resp.StatusCode, body)
		}
		devices, _ := env.db.ListDevices(context.Background(), "")
		resp, body := env.callback(loc.Query().Get("state"), code, state)
		if resp.StatusCode != http.StatusForbidden || cookieNamed(resp, sessionCookie) != nil || !strings.Contains(body, "refused the web sign-in code") {
			t.Fatalf("replay: %d %s, want 403 with the edge's refusal", resp.StatusCode, body)
		}
		if after, _ := env.db.ListDevices(context.Background(), ""); len(after) != len(devices) {
			t.Fatal("the replay registered a device")
		}
	})
	t.Run("not a member", func(t *testing.T) {
		resp, body := env.signIn(grace)
		if resp.StatusCode != http.StatusForbidden || cookieNamed(resp, sessionCookie) != nil || !strings.Contains(body, "github account grace: not a member of this server") {
			t.Fatalf("%d %s, want 403 not a member", resp.StatusCode, body)
		}
	})
}

func TestEdgePendingBrowser(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	member := env.member(ada)
	ctx := context.Background()
	laptop := &domain.Device{Member: member, Kind: domain.DeviceSSH, Credential: "ssh-ed25519 AAAAfake laptop", Label: "laptop"}
	if err := env.db.RegisterDevice(ctx, laptop, true); err != nil {
		t.Fatal(err)
	}

	resp, body := env.signIn(ada)
	session := cookieNamed(resp, sessionCookie)
	if resp.StatusCode != http.StatusForbidden || session == nil {
		t.Fatalf("pending sign-in: %d %s, want 403 with a session cookie", resp.StatusCode, body)
	}
	dev := env.device(session.Value)
	if dev.Status != domain.DevicePending {
		t.Fatalf("browser device is %s, want pending", dev.Status)
	}
	for _, want := range []string{"aether device approve " + dev.ApprovalCode, "sudo aether-server device approve " + dev.ApprovalCode} {
		if !strings.Contains(body, want) {
			t.Errorf("pending page %q lacks %q", body, want)
		}
	}
	status, refusal := env.call(env.origin(), session)
	if status != http.StatusForbidden || refusal.Error == nil || string(refusal.Error.Data) != `{"approval_code":"`+dev.ApprovalCode+`"}` ||
		!strings.Contains(refusal.Error.Message, "aether device approve "+dev.ApprovalCode) {
		t.Fatalf("pending API call: %d %+v, want 403 with the approval code", status, refusal.Error)
	}
	if err := env.db.ApproveDevice(ctx, dev.ID, member); err != nil {
		t.Fatal(err)
	}
	if status, _ := env.call(env.origin(), session); status != http.StatusOK {
		t.Fatalf("approved browser: %d, want 200", status)
	}
}

func TestEdgeAutoApproveAcceptsLaterBrowser(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{DeviceAutoApprove: true})
	member := env.member(ada)
	laptop := &domain.Device{Member: member, Kind: domain.DeviceSSH, Credential: "ssh-ed25519 AAAAfake laptop", Label: "laptop"}
	if err := env.db.RegisterDevice(context.Background(), laptop, false); err != nil {
		t.Fatal(err)
	}
	if resp, body := env.signIn(ada); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in: %d %s, want accepted", resp.StatusCode, body)
	}
}

// openEvents opens the events WebSocket with session and reads the
// subscription's acknowledgement.
func (env *edgeEnv) openEvents(ctx context.Context, session *http.Cookie) *websocket.Conn {
	env.t.Helper()
	header := http.Header{"Origin": {env.origin()}, "Cookie": {session.String()}}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.web.URL, "http")+"/ws/events", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		env.t.Fatal(err)
	}
	if err := wsjson.Write(ctx, c, protocol.SubscribeRequest{}); err != nil {
		env.t.Fatal(err)
	}
	var ack protocol.SubscribeResponse
	if err := wsjson.Read(ctx, c, &ack); err != nil || !ack.OK {
		env.t.Fatalf("subscribe: %+v %v", ack, err)
	}
	return c
}

func waitClosed(t *testing.T, ctx context.Context, c *websocket.Conn) {
	t.Helper()
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(readCtx); err != nil {
			if readCtx.Err() != nil {
				t.Fatal("the WebSocket stayed open")
			}
			return
		}
	}
}

func TestEdgeRevokedSessionLosesRequestsAndSockets(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	env.member(ada)
	ctx := context.Background()

	t.Run("device revoked", func(t *testing.T) {
		session := env.session(ada)
		ws := env.openEvents(ctx, session)
		defer func() { _ = ws.CloseNow() }()
		if err := env.db.RevokeDevice(ctx, env.device(session.Value).ID); err != nil {
			t.Fatal(err)
		}
		status, body := env.call(env.origin(), session)
		if status != http.StatusUnauthorized || !strings.Contains(body.Error.Message, "revoked") {
			t.Fatalf("revoked session: %d %+v, want 401 revoked", status, body.Error)
		}
		waitClosed(t, ctx, ws)
	})
	t.Run("member removed", func(t *testing.T) {
		member := env.member(grace)
		session := env.session(grace)
		ws := env.openEvents(ctx, session)
		defer func() { _ = ws.CloseNow() }()
		if err := env.db.DeleteMember(ctx, member); err != nil {
			t.Fatal(err)
		}
		if status, _ := env.call(env.origin(), session); status != http.StatusUnauthorized {
			t.Fatalf("removed member: %d, want 401", status)
		}
		waitClosed(t, ctx, ws)
	})
	t.Run("logout", func(t *testing.T) {
		env.member(linus)
		session := env.session(linus)
		ws := env.openEvents(ctx, session)
		defer func() { _ = ws.CloseNow() }()
		req, _ := http.NewRequest(http.MethodPost, env.web.URL+"/auth/logout", nil)
		req.AddCookie(session)
		req.Header.Set("Origin", env.origin())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent ||
			setCookieLine(resp, sessionCookie) != sessionCookie+"=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict" {
			t.Fatalf("logout: %d %q", resp.StatusCode, resp.Header.Values("Set-Cookie"))
		}
		if dev := env.device(session.Value); dev.Status != domain.DeviceRevoked {
			t.Fatalf("after logout the browser device is %s", dev.Status)
		}
		waitClosed(t, ctx, ws)
	})
}

func TestEdgeSessionIdleExpiryAndTouch(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	env.member(ada)
	session := env.session(ada)
	signedIn := env.device(session.Value).CreatedAt
	env.advance(2 * time.Minute)
	if status, _ := env.call(env.origin(), session); status != http.StatusOK {
		t.Fatalf("fresh session: %d", status)
	}
	seen := env.device(session.Value).LastSeenAt
	if seen == nil || seen.Before(signedIn.Add(time.Minute)) {
		t.Fatalf("last seen %v, want it touched", seen)
	}
	env.advance(30 * time.Second)
	if status, _ := env.call(env.origin(), session); status != http.StatusOK {
		t.Fatalf("second call: %d", status)
	}
	if again := env.device(session.Value).LastSeenAt; !again.Equal(*seen) {
		t.Fatalf("last seen moved to %v within a minute", again)
	}
	env.advance(edgeproto.SessionIdle)
	status, body := env.call(env.origin(), session)
	if status != http.StatusUnauthorized || !strings.Contains(body.Error.Message, "30 days") {
		t.Fatalf("idle session: %d %+v, want 401 expired", status, body.Error)
	}
}

func TestEdgeStateChangeNeedsOrigin(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	env.member(ada)
	session := env.session(ada)

	if status, body := env.call("", session); status != http.StatusForbidden || !strings.Contains(body.Error.Message, "Origin") {
		t.Fatalf("POST without Origin: %d %+v, want 403", status, body.Error)
	}
	if status, _ := env.call("https://evil.example", session); status != http.StatusForbidden {
		t.Fatalf("POST from another origin: %d, want 403", status)
	}
	if status, _ := env.call(env.origin(), session); status != http.StatusOK {
		t.Fatalf("POST from the dashboard: %d, want 200", status)
	}

	req, _ := http.NewRequest(http.MethodGet, env.web.URL+"/ws/events", nil)
	for k, v := range map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ=="} {
		req.Header.Set(k, v)
	}
	req.AddCookie(session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("WebSocket handshake without Origin: %d, want 403", resp.StatusCode)
	}

	logout, _ := http.NewRequest(http.MethodPost, env.web.URL+"/auth/logout", nil)
	logout.AddCookie(session)
	resp, err = http.DefaultClient.Do(logout)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || env.device(session.Value).Status != domain.DeviceApproved {
		t.Fatalf("logout without Origin: %d, want 403 and the session kept", resp.StatusCode)
	}
}

// stubWhoIs answers every lookup with one tailnet identity, or fails.
type stubWhoIs struct{ err error }

func (s stubWhoIs) WhoIs(context.Context, string) (sshd.WhoIsIdentity, error) {
	if s.err != nil {
		return sshd.WhoIsIdentity{}, s.err
	}
	return sshd.WhoIsIdentity{Login: "ada@example.com", NodeID: "node-ada"}, nil
}

// The tailnet gateway identifies callers by WhoIs only: a valid edge
// session cookie neither admits a caller WhoIs refuses, nor brings the
// edge gateway's Origin requirement or headers with it.
func TestTailnetGatewayIgnoresEdgeSessions(t *testing.T) {
	env := newEdgeEnv(t, EdgeConfig{})
	env.member(ada)
	session := env.session(ada)

	whois := &stubWhoIs{}
	dir := t.TempDir()
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bus.Close() }()
	tailnetSSH, err := sshd.New(sshd.Config{
		Addr: "127.0.0.1:0", HostKeyPath: filepath.Join(dir, "host_key"), Store: env.db, Bus: bus,
		Git: noGit{}, PTY: noPTY{}, Runs: noRuns{}, WhoIs: whois,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tailnetSSH.Close() }()
	gw, err := New(Config{SSH: tailnetSSH, Static: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gw.Close() }()
	web := httptest.NewServer(gw)
	defer web.Close()

	post := func() *http.Response {
		req, _ := http.NewRequest(http.MethodPost, web.URL+"/api/v1/"+protocol.MethodServerInfo, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	resp := post()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tailnet POST without Origin: %d, want 200 as before", resp.StatusCode)
	}
	if resp.Header.Get("Strict-Transport-Security") != "" || resp.Header.Get("X-Frame-Options") != "" {
		t.Fatal("the tailnet gateway carries the edge gateway's headers")
	}
	whois.err = errors.New("tailscaled unreachable")
	if resp := post(); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("WhoIs failing with a valid edge session: %d, want 503", resp.StatusCode)
	}
}

func TestEdgeConfigRefusesBadDomain(t *testing.T) {
	for _, domain := range []string{"", "localhost", "https://servers.example.com", "Servers.example.com", "servers.example.com:443"} {
		_, err := NewEdge(EdgeConfig{SSH: &sshd.Server{}, Store: &store.DB{}, Agent: &fakeAgent{}, EdgeURL: testEdge, ServerDomain: domain, CertDir: "x"})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", domain)) {
			t.Errorf("domain %q: %v, want refused", domain, err)
		}
	}
}
