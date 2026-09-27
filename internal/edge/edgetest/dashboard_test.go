package edgetest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/servergw"
)

// webHarness adds the browser path to a harness: the edge's public TLS
// listener, which routes by SNI, and a test CA that stands in for the ACME
// CA of every server's dashboard certificate.
type webHarness struct {
	*harness
	router string
	roots  *x509.CertPool
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
}

func newWebHarness(t *testing.T) *webHarness {
	t.Helper()
	w := &webHarness{harness: newHarness(t), roots: x509.NewCertPool()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rl := w.relay()
	go func() { _ = rl.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	w.router = ln.Addr().String()
	w.caKey, w.ca = newCert(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: "edgetest CA"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign,
	}, nil, nil)
	w.roots.AddCert(w.ca)
	return w
}

func newCert(t *testing.T, tmpl, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = big.NewInt(time.Now().UnixNano())
	tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

// dashboard is one server's dashboard as browsers reach it through the
// edge: the server's real edge gateway on its agent's web listener, with
// a certificate from the test CA in place of ACME.
type dashboard struct {
	host string
	cert *x509.Certificate
}

func (d *dashboard) url(path string) string { return "https://" + d.host + path }

// serveDashboard starts s's edge gateway under the server domain its agent
// learned from the edge's ready, as the server does.
func (w *webHarness) serveDashboard(s *serverNode) *dashboard {
	t := w.t
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	domain, err := s.agent.ServerDomain(ctx)
	if err != nil || domain != serverDomain {
		t.Fatalf("server domain from the edge = %q, %v; want %q", domain, err, serverDomain)
	}
	host := edgeproto.ServerHostname(s.id, domain)
	key, cert := newCert(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, w.ca, w.caKey)
	gw, err := servergw.NewEdge(servergw.EdgeConfig{
		SSH: s.ssh, Store: s.db, Agent: s.agent, EdgeURL: w.origin, ServerDomain: domain,
		Certificate: &tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert},
		Static:      fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Aether</title>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.Start()
	t.Cleanup(func() { _ = gw.Close() })
	return &dashboard{host: host, cert: cert}
}

// transport dials every dashboard hostname at the edge's public listener,
// as the wildcard DNS record does, and trusts the test CA.
func (w *webHarness) transport() *http.Transport {
	dialer := &net.Dialer{Timeout: waitTimeout}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, _, _ := net.SplitHostPort(addr); strings.HasSuffix(host, "."+serverDomain) {
				addr = w.router
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: w.roots, MinVersion: tls.VersionTLS12},
	}
}

// browser is a new browser signed in to the edge as user. One cookie jar
// holds the edge's session and every dashboard's cookies.
func (w *webHarness) browser(user ghUser) *browser {
	w.t.Helper()
	b, err := w.signIn(user)
	if err != nil {
		w.t.Fatal(err)
	}
	b.client.Transport = w.transport()
	return b
}

// claimInBrowser makes the browser's account the owner of s through the
// edge's Add a server page. It leaves the edge's clock alone, unlike
// harness.claim: web grants carry the edge's time, and the server allows
// only edgeproto.ClockSkew. A test claims fewer servers than the edge's
// per-address claim burst.
func (w *webHarness) claimInBrowser(b *browser, s *serverNode) {
	w.t.Helper()
	resp, page, err := b.post(edgeproto.PathAddServer, url.Values{"code": {s.claimCode(w.t, time.Now())}})
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(page, s.id) {
		w.t.Fatalf("claim %s in the browser: %v %v\n%s", s.id, err, resp, page)
	}
}

// fetch sends one request to an absolute URL, following no redirect.
func (b *browser) fetch(method, rawURL string, body io.Reader, header http.Header) (*http.Response, string, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, "", err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	data, err := io.ReadAll(resp.Body)
	return resp, string(data), err
}

// get fetches rawURL as a navigation does.
func (b *browser) get(rawURL string) (*http.Response, string) {
	b.h.t.Helper()
	resp, body, err := b.fetch(http.MethodGet, rawURL, nil, nil)
	if err != nil {
		b.h.t.Fatal(err)
	}
	return resp, body
}

// login starts a dashboard sign-in and returns the edge's authorize URL.
func (b *browser) login(d *dashboard, query string) *url.URL {
	t := b.h.t
	t.Helper()
	resp, body := b.get(d.url("/auth/login" + query))
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusSeeOther || err != nil || !strings.HasPrefix(loc.String(), b.h.origin+edgeproto.PathAuthorize+"?") {
		t.Fatalf("GET /auth/login%s = %s, Location %q\n%s", query, resp.Status, resp.Header.Get("Location"), body)
	}
	return loc
}

// authorize runs a dashboard sign-in up to the edge's redirect back: the
// dashboard's /auth/login, the edge's authorize page and its Continue
// button. It returns where the edge sends the browser, or the edge's page
// when the edge refused.
func (b *browser) authorize(d *dashboard, query string) (*url.URL, error) {
	loc := b.login(d, query)
	resp, page, err := b.fetch(http.MethodGet, loc.String(), nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authorize page: %s\n%s", resp.Status, page)
	}
	m := csrfField.FindStringSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("no CSRF token on the authorize page:\n%s", page)
	}
	form := loc.Query()
	form.Set("csrf", m[1])
	resp, page, err = b.fetch(http.MethodPost, b.h.origin+edgeproto.PathAuthorize, strings.NewReader(form.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSeeOther {
		return nil, fmt.Errorf("continue on the authorize page: %s\n%s", resp.Status, page)
	}
	return url.Parse(resp.Header.Get("Location"))
}

func (b *browser) mustAuthorize(d *dashboard, query string) *url.URL {
	b.h.t.Helper()
	cb, err := b.authorize(d, query)
	if err != nil {
		b.h.t.Fatal(err)
	}
	return cb
}

// signInTo runs the whole dashboard sign-in and returns the callback's
// response.
func (b *browser) signInTo(d *dashboard) (*http.Response, string) {
	b.h.t.Helper()
	cb := b.mustAuthorize(d, "")
	if want := d.url(edgeproto.PathAuthCallback); !strings.HasPrefix(cb.String(), want+"?") {
		b.h.t.Fatalf("the edge returned to %s, want %s", cb, want)
	}
	return b.fromEdge(cb)
}

// fromEdge follows the edge's redirect back to the dashboard as a browser
// does. The navigation is cross-site, so of the dashboard's cookies only
// the SameSite=Lax sign-in cookie goes with it; the cookie jar ignores
// SameSite and would send them all.
func (b *browser) fromEdge(cb *url.URL) (*http.Response, string) {
	t := b.h.t
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, cb.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.client.Jar.Cookies(cb) {
		if c.Name == "__Host-aether_signin" {
			req.AddCookie(c)
		}
	}
	client := &http.Client{Transport: b.client.Transport, Timeout: waitTimeout, CheckRedirect: b.client.CheckRedirect}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	b.client.Jar.SetCookies(cb, resp.Cookies())
	return resp, string(data)
}

func (b *browser) mustSignInTo(d *dashboard) {
	b.h.t.Helper()
	if resp, body := b.signInTo(d); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		b.h.t.Fatalf("sign-in callback: %s\n%s", resp.Status, body)
	}
}

// apiResult is a dashboard API answer: the status and, on failure, the
// error body.
type apiResult struct {
	status int
	body   string
	err    struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
}

// call posts one API call from the dashboard's own page, so with its
// Origin, and decodes a result into out.
func (b *browser) call(d *dashboard, method string, params, out any) apiResult {
	t := b.h.t
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	resp, body, err := b.fetch(http.MethodPost, d.url("/api/v1/"+method), bytes.NewReader(data),
		http.Header{"Content-Type": {"application/json"}, "Origin": {d.url("")}})
	if err != nil {
		t.Fatal(err)
	}
	r := apiResult{status: resp.StatusCode, body: body}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(body), &e) == nil {
			_ = json.Unmarshal(e.Error, &r.err)
		}
		return r
	}
	if out != nil {
		if err := json.Unmarshal([]byte(body), out); err != nil {
			t.Fatalf("%s: %v: %s", method, err, body)
		}
	}
	return r
}

func (b *browser) mustCall(d *dashboard, method string, params, out any) {
	b.h.t.Helper()
	if r := b.call(d, method, params, out); r.status != http.StatusOK {
		b.h.t.Fatalf("%s: %d %s", method, r.status, r.body)
	}
}

// events opens the dashboard's event stream as its page does.
func (b *browser) events(d *dashboard) *websocket.Conn {
	t := b.h.t
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	// coder/websocket refuses a client with a Timeout; the context bounds
	// the handshake instead.
	client := &http.Client{Jar: b.client.Jar, Transport: b.client.Transport}
	c, _, err := websocket.Dial(ctx, "wss://"+d.host+"/ws/events", &websocket.DialOptions{
		HTTPClient: client, HTTPHeader: http.Header{"Origin": {d.url("")}},
	})
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	if err := wsjson.Write(ctx, c, protocol.SubscribeRequest{}); err != nil {
		t.Fatal(err)
	}
	var ack protocol.SubscribeResponse
	if err := wsjson.Read(ctx, c, &ack); err != nil || !ack.OK {
		t.Fatalf("subscribe: %+v %v", ack, err)
	}
	return c
}

// waitClosed reads c until the gateway closes it.
func waitClosed(t *testing.T, what string, c *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatalf("%s: still open after %s", what, waitTimeout)
			}
			return
		}
	}
}

func wantStatus(t *testing.T, what string, resp *http.Response, body string, status int, text string) {
	t.Helper()
	if resp.StatusCode != status || !strings.Contains(body, text) {
		t.Fatalf("%s: %s %q, want %d containing %q", what, resp.Status, body, status, text)
	}
}

func (b *browser) cookie(d *dashboard, name string) *http.Cookie {
	u, _ := url.Parse(d.url("/"))
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// The whole browser path: the dashboard signs a browser in through the
// edge, the session reaches the API and the event stream, and logging out
// ends both.
func TestDashboardSignIn(t *testing.T) {
	w := newWebHarness(t)
	a := w.newServer()
	b := w.browser(alice)
	w.claimInBrowser(b, a)
	d := w.serveDashboard(a)

	if r := b.call(d, protocol.MethodMemberList, struct{}{}, nil); r.status != http.StatusUnauthorized ||
		string(r.err.Data) != `{"login":"/auth/login"}` {
		t.Fatalf("API before sign-in: %d %s, want 401 naming /auth/login", r.status, r.body)
	}
	b.mustSignInTo(d)
	if b.cookie(d, "__Host-aether_signin") != nil {
		t.Fatal("the sign-in state cookie outlived the callback")
	}
	var members protocol.MemberListResult
	b.mustCall(d, protocol.MethodMemberList, struct{}{}, &members)
	if len(members.Members) != 1 || members.Members[0].Role != "admin" {
		t.Fatalf("member.list through the edge = %+v", members.Members)
	}
	resp, body := b.get(d.url("/api/v1/capabilities"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"gateway":"edge"`) {
		t.Fatalf("capabilities: %s %s", resp.Status, body)
	}
	// A cookie credential makes a POST without Origin the one request the
	// same-origin check cannot judge; the gateway refuses it.
	resp, body, err := b.fetch(http.MethodPost, d.url("/api/v1/"+protocol.MethodMemberList), strings.NewReader("{}"),
		http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, "API call without Origin", resp, body, http.StatusForbidden, "Origin")

	events := b.events(d)
	resp, body, err = b.fetch(http.MethodPost, d.url("/auth/logout"), nil, http.Header{"Origin": {d.url("")}})
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %v %v %s", err, resp, body)
	}
	waitClosed(t, "event stream after logout", events)
	if r := b.call(d, protocol.MethodMemberList, struct{}{}, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("API after logout: %d %s, want 401", r.status, r.body)
	}
}

// The edge routes a dashboard connection by SNI without terminating TLS:
// the browser completes its handshake with the server's own certificate.
// A name that is not a claimed, connected server is closed.
func TestDashboardPassthroughBySNI(t *testing.T) {
	w := newWebHarness(t)
	a := w.newServer()
	w.claimInBrowser(w.browser(alice), a)
	d := w.serveDashboard(a)
	unclaimed := w.newServer()

	handshake := func(name string) (*tls.Conn, error) {
		raw, err := net.DialTimeout("tcp", w.router, waitTimeout)
		if err != nil {
			t.Fatal(err)
		}
		c := tls.Client(raw, &tls.Config{ServerName: name, RootCAs: w.roots, MinVersion: tls.VersionTLS12})
		_ = c.SetDeadline(time.Now().Add(waitTimeout))
		if err := c.Handshake(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	}
	c, err := handshake(d.host)
	if err != nil {
		t.Fatalf("handshake with %s through the edge: %v", d.host, err)
	}
	if got := c.ConnectionState().PeerCertificates[0]; !got.Equal(d.cert) {
		t.Fatalf("the browser saw certificate %q, not the server's own", got.Subject)
	}
	_ = c.Close()

	for _, name := range []string{
		edgeproto.ServerHostname(strings.Repeat("a", edgeproto.ServerIDLength), serverDomain),
		edgeproto.ServerHostname(unclaimed.id, serverDomain),
		"www." + serverDomain,
		"dashboard.example.org",
	} {
		if c, err := handshake(name); err == nil {
			_ = c.Close()
			t.Errorf("handshake for %s completed; the edge should close it", name)
		}
	}
}

// A sign-in code is good for one server, one browser and one use.
func TestDashboardSessionIsBoundToItsServer(t *testing.T) {
	w := newWebHarness(t)
	a, s2 := w.newServer(), w.newServer()
	b := w.browser(alice)
	w.claimInBrowser(b, a)
	w.claimInBrowser(w.browser(carol), s2)
	da, db := w.serveDashboard(a), w.serveDashboard(s2)
	b.mustSignInTo(da)

	// A member of A is not one of B: the edge refuses to sign them in there.
	if _, err := b.authorize(db, ""); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("alice's sign-in to carol's server: %v, want the edge's 403", err)
	}
	// A code minted for A, presented to B with B's own state, is refused by
	// the edge when B redeems it.
	code := b.mustAuthorize(da, "").Query().Get(edgeproto.ParamCode)
	state := b.login(db, "").Query().Get(edgeproto.ParamState)
	resp, body := b.get(db.url(edgeproto.PathAuthCallback + "?" + url.Values{"code": {code}, "state": {state}}.Encode()))
	wantStatus(t, "A's code at B", resp, body, http.StatusForbidden, "refused the web sign-in code")
	if r := b.call(db, protocol.MethodMemberList, struct{}{}, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("API on B after the refused code: %d %s", r.status, r.body)
	}
	// A's session token means nothing to B.
	session := b.cookie(da, "__Host-aether_session")
	req, _ := http.NewRequest(http.MethodPost, db.url("/api/v1/"+protocol.MethodMemberList), strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", db.url(""))
	req.Header.Set("Cookie", session.Name+"="+session.Value)
	stolen, err := (&http.Client{Transport: w.transport()}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	stolenBody, _ := io.ReadAll(stolen.Body)
	_ = stolen.Body.Close()
	wantStatus(t, "A's session cookie at B", stolen, string(stolenBody), http.StatusUnauthorized, "no longer exists on this server")

	// A used code is refused, even with a fresh state that matches.
	cb := b.mustAuthorize(da, "")
	b.get(cb.String())
	replay := cb.Query()
	replay.Set(edgeproto.ParamState, b.login(da, "").Query().Get(edgeproto.ParamState))
	resp, body = b.get(da.url(edgeproto.PathAuthCallback + "?" + replay.Encode()))
	wantStatus(t, "replayed code", resp, body, http.StatusForbidden, "refused the web sign-in code")

	// A state that is not this browser's is refused before the code is
	// redeemed.
	cb = b.mustAuthorize(da, "")
	forged := cb.Query()
	forged.Set(edgeproto.ParamState, edgeproto.NewToken())
	resp, body = b.get(da.url(edgeproto.PathAuthCallback + "?" + forged.Encode()))
	wantStatus(t, "state mismatch", resp, body, http.StatusBadRequest, "does not match")
}

// The Android app's variant: the edge returns the code to the app's link,
// and the dashboard accepts it only in the WebView holding the state
// cookie of that sign-in.
func TestDashboardAppReturn(t *testing.T) {
	w := newWebHarness(t)
	a := w.newServer()
	b := w.browser(alice)
	w.claimInBrowser(b, a)
	d := w.serveDashboard(a)

	link := b.mustAuthorize(d, "?return=app")
	if !strings.HasPrefix(link.String(), edgeproto.AppCallbackURL+"?") {
		t.Fatalf("the edge returned to %s, want %s", link, edgeproto.AppCallbackURL)
	}
	callback := d.url(edgeproto.PathAuthCallback + "?" + link.RawQuery)
	intercepted := &browser{h: w.harness, client: &http.Client{Transport: w.transport(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	resp, body := intercepted.get(callback)
	wantStatus(t, "the link opened without the state cookie", resp, body, http.StatusBadRequest, "started in another browser")
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-aether_session" {
			t.Fatal("an intercepted link obtained a session")
		}
	}

	resp, body = b.get(callback)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("the app's callback: %s\n%s", resp.Status, body)
	}
	b.mustCall(d, protocol.MethodMemberList, struct{}{}, nil)
}

// Logging out ends the browser's session, not its device: signing in
// again on the same browser needs no new approval and adds no device.
func TestDashboardSignOutKeepsTheDevice(t *testing.T) {
	w := newWebHarness(t)
	a := w.newServer()
	b := w.browser(alice)
	w.claimInBrowser(b, a)
	d := w.serveDashboard(a)
	b.mustSignInTo(d)
	var before protocol.MemberDeviceListResult
	b.mustCall(d, protocol.MethodMemberDeviceList, struct{}{}, &before)

	resp, body, err := b.fetch(http.MethodPost, d.url("/auth/logout"), nil, http.Header{"Origin": {d.url("")}})
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %v %v %s", err, resp, body)
	}
	if r := b.call(d, protocol.MethodMemberList, struct{}{}, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("API after logout: %d %s, want 401", r.status, r.body)
	}
	b.mustSignInTo(d)
	var after protocol.MemberDeviceListResult
	b.mustCall(d, protocol.MethodMemberDeviceList, struct{}{}, &after)
	// Last seen moves with every sign-in.
	for _, l := range []*protocol.MemberDeviceListResult{&before, &after} {
		for i := range l.Devices {
			l.Devices[i].LastSeenAt = ""
		}
	}
	if !reflect.DeepEqual(after.Devices, before.Devices) {
		t.Fatalf("devices after signing out and in again = %+v, want %+v", after.Devices, before.Devices)
	}
}

// A member's second browser waits for approval from their first, and a
// revoked browser loses its API access and live socket.
func TestDashboardPendingBrowserAndRevocation(t *testing.T) {
	w := newWebHarness(t)
	a := w.newServer()
	first := w.browser(alice)
	w.claimInBrowser(first, a)
	d := w.serveDashboard(a)
	first.mustSignInTo(d)
	firstEvents := first.events(d)

	second := w.browser(alice)
	if resp, body := second.signInTo(d); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("second browser's callback: %s %q, want a redirect to the dashboard", resp.Status, body)
	}
	r := second.call(d, protocol.MethodMemberList, struct{}{}, nil)
	var pending struct {
		ApprovalCode string `json:"approval_code"`
	}
	if r.status != http.StatusForbidden || json.Unmarshal(r.err.Data, &pending) != nil || pending.ApprovalCode == "" ||
		!strings.Contains(r.err.Message, "aether device approve "+pending.ApprovalCode) {
		t.Fatalf("pending browser's API call: %d %s", r.status, r.body)
	}
	var devices protocol.MemberDeviceListResult
	first.mustCall(d, protocol.MethodMemberDeviceList, struct{}{}, &devices)
	var secondID string
	for _, dev := range devices.Devices {
		if dev.Status == "pending" {
			secondID = dev.ID
		}
	}
	if secondID == "" {
		t.Fatalf("no pending device in %+v", devices.Devices)
	}
	first.mustCall(d, protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{Code: pending.ApprovalCode}, nil)
	second.mustCall(d, protocol.MethodMemberList, struct{}{}, nil)

	secondEvents := second.events(d)
	first.mustCall(d, protocol.MethodMemberDeviceRevoke, protocol.MemberDeviceRevokeParams{DeviceID: secondID}, nil)
	if r := second.call(d, protocol.MethodMemberList, struct{}{}, nil); r.status != http.StatusUnauthorized ||
		!strings.Contains(r.err.Message, "revoked") {
		t.Fatalf("revoked browser's API call: %d %s, want 401", r.status, r.body)
	}
	waitClosed(t, "revoked browser's event stream", secondEvents)
	first.mustCall(d, protocol.MethodMemberList, struct{}{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := firstEvents.Read(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the first browser's event stream: %v, want it still open", err)
	}
}

// An invited account's first browser sign-in accepts the invitation, and
// removing that member ends their session and live socket.
func TestDashboardInvitationAndMemberRemoval(t *testing.T) {
	w := newWebHarness(t)
	a := w.newServer()
	owner := w.browser(alice)
	w.claimInBrowser(owner, a)
	d := w.serveDashboard(a)
	owner.mustSignInTo(d)

	guest := w.browser(bob)
	if _, err := guest.authorize(d, ""); err == nil {
		t.Fatal("the edge signed bob in to a server he is not invited to")
	}
	owner.mustCall(d, protocol.MethodMemberInvitationCreate, protocol.MemberInvitationCreateParams{
		Provider: edgeproto.ProviderGitHub, Login: bob.Login, Role: "collaborator"}, nil)
	var cb *url.URL
	eventually(t, "the edge admits bob by his invitation", func() error {
		var err error
		cb, err = guest.authorize(d, "")
		return err
	})
	resp, body := guest.get(cb.String())
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bob's first sign-in: %s\n%s", resp.Status, body)
	}
	var members protocol.MemberListResult
	owner.mustCall(d, protocol.MethodMemberList, struct{}{}, &members)
	var bobID string
	for _, m := range members.Members {
		if m.DisplayName == bob.Login && m.Role == "collaborator" {
			bobID = m.ID
		}
	}
	if bobID == "" {
		t.Fatalf("bob did not join as a collaborator: %+v", members.Members)
	}
	guest.mustCall(d, protocol.MethodMemberList, struct{}{}, nil)
	events := guest.events(d)

	owner.mustCall(d, protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: bobID}, nil)
	if r := guest.call(d, protocol.MethodMemberList, struct{}{}, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("removed member's API call: %d %s, want 401", r.status, r.body)
	}
	waitClosed(t, "removed member's event stream", events)
}
