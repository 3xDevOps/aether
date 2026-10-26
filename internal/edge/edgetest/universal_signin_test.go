package edgetest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
)

// postAs posts form to target from b with header, as a sandboxed consent
// page's form posts: Origin null, a cross-site fetch.
func (b *browser) postAs(t *testing.T, target string, form url.Values, header http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(page)
}

// TestUniversalSignInBoundToBrowser leaks every part of alice's universal
// sign-in to bob, signed in at the edge in another browser: the URLs she
// opened, the choose form with its assertion, and her consent form. None
// of them gets bob a code: the URLs carry no assertion, the assertion is
// bound to the first browser that brought it, and the consent form to
// that browser. An assertion in a URL is refused. Alice's own consent,
// posted from the sandboxed page's opaque origin, still gets her a code.
func TestUniversalSignInBoundToBrowser(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newConnectorClient(t)
	s1 := h.newConnectorServer(edgeproto.PolicyAccount, client, sshd.Services{})
	h.claimServer(h.login(alice)[0], s1)
	aliceBrowser, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}
	bobBrowser, err := h.signIn(bob)
	if err != nil {
		t.Fatal(err)
	}
	base := h.relayURL + edgeproto.PathUniversalMCP
	request := url.Values{"response_type": {"code"}, "client_id": {client.id}, "redirect_uri": {client.redirect},
		"code_challenge": {oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())}, "code_challenge_method": {"S256"},
		"resource": {base}, "state": {"s"}}
	authURL := base + "/authorize?" + request.Encode()

	resp, page, chooseURL := aliceBrowser.visit(t, authURL)
	action, forms := chooseForms(t, page)
	choose := forms[s1.id]
	if resp.StatusCode != http.StatusOK || choose == nil || strings.Contains(chooseURL, edgeproto.ParamAssertion) {
		t.Fatalf("alice's choose page at %s: %s\n%s", chooseURL, resp.Status, page)
	}
	if _, page, _ = bobBrowser.visit(t, chooseURL); strings.Contains(page, s1.id) {
		t.Fatalf("bob, no member of %s, opened alice's choose URL and was offered it:\n%s", s1.id, page)
	}
	consentURL := base + "/authorize?" + request.Encode() + "&" + edgeproto.ParamServer + "=" + s1.id
	if resp, page = bobBrowser.get(t, consentURL); resp.StatusCode != http.StatusOK || !strings.Contains(page, `name="code"`) {
		t.Fatalf("bob opening the consent URL: %s, want the page asking a code\n%s", resp.Status, page)
	}

	resp, page = aliceBrowser.send(t, http.MethodPost, action, choose)
	m := requestField.FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil || resp.Header.Get("Content-Security-Policy") != h.universalCSP() {
		t.Fatalf("alice's consent page: %s %v\n%s", resp.Status, resp.Header, page)
	}
	consent := url.Values{"request": {m[1]}, edgeproto.ParamServer: {s1.id}, "scope": {"write"}}
	inURL := url.Values{edgeproto.ParamAssertion: {choose.Get(edgeproto.ParamAssertion)}}
	for _, tt := range []struct {
		what, method, target string
		form                 url.Values
		want                 string
	}{
		{"alice's choose form", http.MethodPost, action, choose, "the edge&#39;s sign-in for it was already opened in another browser"},
		{"alice's consent form", http.MethodPost, action, consent,
			"this consent page was opened in another browser than the one that signed in at the edge"},
		{"alice's assertion in a URL", http.MethodGet, consentURL + "&" + inURL.Encode(), nil, "The edge put its sign-in in the URL"},
	} {
		if resp, page = bobBrowser.send(t, tt.method, tt.target, tt.form); resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, tt.want) {
			t.Fatalf("bob posting %s: %s, want 400 %q\n%s", tt.what, resp.Status, tt.want, page)
		}
	}

	resp, page = aliceBrowser.postAs(t, action, consent, http.Header{"Origin": {"null"}, "Sec-Fetch-Site": {"cross-site"}})
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc.Query().Get("code"), s1.id+".") {
		t.Fatalf("alice's allow: %s %v %v\n%s", resp.Status, loc, err, page)
	}
}

var browserBinding = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TestUniversalGuardsServerAnswers has a server answer the universal
// authorization path as a malicious one would. The edge passes a
// redirect to its own origin or the connector client, and refuses one
// anywhere else with 502, its Location dropped. Every answer limits
// forms to the same places, and the server sees the browser only as a
// binding the edge sets, never a client's own.
func TestUniversalGuardsServerAnswers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newConnectorClient(t)
	seen := make(chan string, 8)
	s := h.newMCPServer(edgeproto.PolicyAccount, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get(edgeproto.HeaderBrowser)
		http.SetCookie(w, &http.Cookie{Name: "__Host-aether_edge_browser", Value: "fixed", Path: "/"})
		if to := r.URL.Query().Get("to"); to != "" {
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<form method="post" action="https://evil.example/"><input name="password"></form>`)
	}))
	h.claimServer(h.login(alice)[0], s)
	b, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}
	at := h.relayURL + edgeproto.PathUniversalMCPAuthorize + "?" + edgeproto.ParamServer + "=" + s.id
	var binding string
	for _, tt := range []struct {
		to   string
		pass bool
	}{
		{"", true},
		{"https://evil.example/steal", false},
		{"//evil.example/steal", false},
		{"http://evil.example/callback", false},
		{"http://127.0.0.1:8080/admin", false},
		{"http://127.0.0.1:54321/callback?code=x", true},
		{"http://localhost/callback", true},
		{"https://user@" + client.host + "/callback", false},
		{"/mcp/authorize?next=1", true},
		{h.relayURL + "/mcp/authorize", true},
		{"/mcp/" + s.id + "/authorize", false},
		{h.relayURL + "/mcp/" + s.id + "/", false},
		{client.redirect + "?code=x", true},
	} {
		target := at
		if tt.to != "" {
			target += "&to=" + url.QueryEscape(tt.to)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(edgeproto.HeaderBrowser, "forged")
		resp, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		got := <-seen
		switch {
		case !browserBinding.MatchString(got) || (binding != "" && got != binding):
			t.Fatalf("to %q: the server saw browser %q, want the edge's one binding %q", tt.to, got, binding)
		case resp.Header.Get("Content-Security-Policy") != h.universalCSP() && tt.pass:
			t.Fatalf("to %q: Content-Security-Policy %q, want %q", tt.to, resp.Header.Get("Content-Security-Policy"), h.universalCSP())
		case tt.pass && tt.to != "" && (resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != tt.to):
			t.Fatalf("to %q: %s %q, want it passed", tt.to, resp.Status, resp.Header.Get("Location"))
		case !tt.pass && (resp.StatusCode != http.StatusBadGateway || resp.Header.Get("Location") != "" ||
			!strings.Contains(string(body), "the server sent the browser to")):
			t.Fatalf("to %q: %s %q %s, want 502 and no Location", tt.to, resp.Status, resp.Header.Get("Location"), body)
		}
		for _, c := range resp.Cookies() {
			// Browsers drop a __Host- cookie unless it is Secure with Path
			// /; Go's cookie jar does not check.
			if c.Value == "fixed" || c.Path != "/" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteNoneMode {
				t.Fatalf("to %q: cookie %v, want only the edge's, Secure, HttpOnly, SameSite=None and Path /", tt.to, c)
			}
		}
		if tt.to == "" && len(resp.Cookies()) != 1 {
			t.Fatalf("the first answer set cookies %v, want the edge's browser cookie", resp.Cookies())
		}
		binding = got
	}
}

// TestUniversalSignInAcrossVersions runs a current server whose control
// channel negotiated protocol 3, as one behind an edge from before
// VersionConnectorSignIn does. This edge, current, lists it as too old to
// choose and posts it no assertion. The older edge's hand-off, an
// assertion in the URL with no browser or redirect URI, still connects
// it as before, and the connector works through this edge.
func TestUniversalSignInAcrossVersions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
		if ch, ok := e.msg.(edgeproto.Challenge); ok {
			ch.Version = edgeproto.VersionUniversalMCP
			return ch, true
		}
		return e.msg, true
	})
	client := newConnectorClient(t)
	s := h.newConnectorServer(edgeproto.PolicyAccount, client, sshd.Services{})
	if v := s.agent.EdgeVersion(); v != edgeproto.VersionUniversalMCP {
		t.Fatalf("EdgeVersion = %d, want %d", v, edgeproto.VersionUniversalMCP)
	}
	al := h.login(alice)[0]
	h.claimServer(al, s)
	base := h.relayURL + edgeproto.PathUniversalMCP
	if code := call[protocol.ConnectorCodeResult](t, h.control(al, s), protocol.MethodConnectorCode, struct{}{}); code.URL != base {
		t.Fatalf("connector.code behind a protocol 3 edge = %+v, want %s", code, base)
	}
	b, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}
	verifier := oauth2.GenerateVerifier()
	request := url.Values{"response_type": {"code"}, "client_id": {client.id}, "redirect_uri": {client.redirect},
		"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "code_challenge_method": {"S256"},
		"resource": {base}, "state": {"s"}}
	_, page, _ := b.visit(t, base+"/authorize?"+request.Encode())
	if _, forms := chooseForms(t, page); forms[s.id] != nil || !strings.Contains(page, "its aether-server is too old for this URL") {
		t.Fatalf("this edge's choose page offers the protocol 3 server:\n%s", page)
	}

	now := time.Now()
	signed, err := edgeproto.SignConnectorAssertion(h.edgeKey(), edgeproto.ConnectorAssertion{Issuer: h.relayURL, ServerID: s.id,
		Nonce: edgeproto.NewConnID(), Account: alice.account(), ClientID: client.id, Challenge: request.Get("code_challenge"),
		IssuedAt: now, ExpiresAt: now.Add(edgeproto.ConnectorAssertionTTL)})
	if err != nil {
		t.Fatal(err)
	}
	handOff := base + "/authorize?" + request.Encode() + "&" + url.Values{edgeproto.ParamServer: {s.id}, edgeproto.ParamAssertion: {signed}}.Encode()
	resp, page := b.get(t, handOff)
	m := requestField.FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil || !strings.Contains(page, "</strong> as <strong>"+s.memberOf(t, alice).DisplayName+"</strong>") {
		t.Fatalf("the older edge's hand-off: %s\n%s", resp.Status, page)
	}
	resp, page = b.send(t, http.MethodPost, handOff, url.Values{"request": {m[1]}, edgeproto.ParamServer: {s.id}})
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("allow: %s %v\n%s", resp.Status, err, page)
	}
	form := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	resp, body := mcpRequest(t, http.MethodPost, base+"/token", form, strings.NewReader(url.Values{"grant_type": {"authorization_code"},
		"client_id": {client.id}, "code": {loc.Query().Get("code")}, "code_verifier": {verifier}, "redirect_uri": {client.redirect},
		"resource": {base}}.Encode()))
	var tok tokenAnswer
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &tok) != nil {
		t.Fatalf("token exchange: %s %s", resp.Status, body)
	}
	header := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}, "Authorization": {"Bearer " + tok.AccessToken}}
	if resp, body = mcpRequest(t, http.MethodPost, base, header, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list: %s %s", resp.Status, body)
	}
}

// TestUniversalLoopbackClient connects a client like Claude Code, whose
// document lists http://127.0.0.1/callback, through the universal URL:
// it returns to the loopback port it picked with a code, which redeems
// with PKCE. The same redirect is refused for a client not listed as a
// loopback client.
func TestUniversalLoopbackClient(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newLoopbackClient(t)
	s := h.newConnectorServer(edgeproto.PolicyAccount, client, sshd.Services{})
	h.claimServer(h.login(alice)[0], s)
	b, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}
	base := h.relayURL + edgeproto.PathUniversalMCP
	verifier := oauth2.GenerateVerifier()
	request := url.Values{"response_type": {"code"}, "client_id": {client.id}, "redirect_uri": {client.redirect},
		"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "code_challenge_method": {"S256"}, "resource": {base}}
	res, _ := universalConsent(t, b, base+"/authorize?"+request.Encode(), s.id, s.memberOf(t, alice).DisplayName, "", edgeproto.PolicyAccount)
	form := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	resp, body := mcpRequest(t, http.MethodPost, base+"/token", form, strings.NewReader(url.Values{"grant_type": {"authorization_code"},
		"client_id": {client.id}, "code": {res.Code}, "code_verifier": {verifier}, "redirect_uri": {client.redirect}, "resource": {base}}.Encode()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token exchange for the loopback client: %s %s", resp.Status, body)
	}

	other := newConnectorClient(t)
	s2 := h.newConnectorServer(edgeproto.PolicyAccount, other, sshd.Services{})
	h.claimServer(h.login(alice)[0], s2)
	request.Set("client_id", other.id)
	resp, page := b.get(t, h.relayURL+edgeproto.MCPPath(s2.id)+"/authorize?"+request.Encode())
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, "is not an https URL on") {
		t.Fatalf("a loopback redirect for a client not listed as one: %s\n%s", resp.Status, page)
	}
}
