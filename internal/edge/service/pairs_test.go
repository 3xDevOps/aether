package edge

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// The second origin pair of a two-pair edge in tests, with its own OAuth
// app.
const (
	testSignin2 = "https://signin2.example.test"
	testRelay2  = "https://relay2.example.test"
)

func newPairsHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t, func(cfg *Config, p *fakeProvider) {
		app := p.app()
		app.ClientID, app.ClientSecret = "test-client-2", "test-secret-2"
		cfg.MorePairs = []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2, GitHub: app}}
	})
}

func TestOriginPairsMustNotShareHosts(t *testing.T) {
	app := &OAuthApp{ClientID: "id", ClientSecret: "secret"}
	for _, more := range []Pair{
		{SigninOrigin: testRelay, RelayOrigin: testRelay2, GitHub: app},
		{SigninOrigin: testSignin2, RelayOrigin: "https://SIGNIN.example.test:8443", GitHub: app},
		{SigninOrigin: testSignin2, RelayOrigin: testSignin2, GitHub: app},
	} {
		_, err := New(Config{DataDir: t.TempDir(), SigninOrigin: testSignin, RelayOrigin: testRelay, GitHub: app, MorePairs: []Pair{more}})
		if err == nil || !strings.Contains(err.Error(), "host name") {
			t.Errorf("New with the pair %s %s = %v, want an error naming the shared host name", more.SigninOrigin, more.RelayOrigin, err)
		}
	}
	_, err := New(Config{DataDir: t.TempDir(), SigninOrigin: testSignin, RelayOrigin: testRelay, GitHub: app,
		MorePairs: []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2}}})
	if err == nil || !strings.Contains(err.Error(), "no GitHub OAuth app is configured for "+testSignin2) {
		t.Errorf("New with a pair without an OAuth app = %v", err)
	}
}

// TestOriginPairsRouting checks that each request is answered in the pair
// its host belongs to, with neither pair preferred, and that a host in no
// pair gets 421 naming every origin.
func TestOriginPairsRouting(t *testing.T) {
	h := newPairsHarness(t)
	serverID := testServerID(t)
	for _, tt := range []struct {
		origin, method, path string
		status               int
		want                 string
	}{
		{"https://other.example.test", http.MethodGet, "/signin", http.StatusMisdirectedRequest,
			"this edge serves " + testSignin + ", " + testRelay + ", " + testSignin2 + " and " + testRelay2 + `, not the host "other.example.test"`},
		{testSignin2, http.MethodGet, "/signin", http.StatusOK, "Sign in"},
		{testRelay2, http.MethodGet, "/signin", http.StatusMisdirectedRequest, "/signin is served on " + testSignin2},
		{testSignin2, http.MethodGet, edgeproto.PathEdgeInfo, http.StatusMisdirectedRequest, "is served on " + testRelay2},
		{testRelay2, http.MethodGet, edgeproto.ConnectPath(serverID), http.StatusOK, "relayed"},
		{testSignin, http.MethodGet, "/signin", http.StatusOK, "Sign in"},
	} {
		b := h.browser(t)
		b.origin = tt.origin
		resp, body := b.do(t, tt.method, tt.path, nil, nil)
		var e edgeproto.ErrorBody
		if json.Unmarshal([]byte(body), &e) == nil && e.Error != "" {
			body = e.Error
		}
		if resp.StatusCode != tt.status || !strings.Contains(body, tt.want) {
			t.Errorf("%s %s%s: %s %q, want %d with %q", tt.method, tt.origin, tt.path, resp.Status, body, tt.status, tt.want)
		}
	}

	for _, p := range []struct{ signin, relay string }{{testSignin, testRelay}, {testSignin2, testRelay2}} {
		var info edgeproto.EdgeInfo
		if status, msg := h.apiCallOn(t, p.relay, http.MethodGet, edgeproto.PathEdgeInfo, "", nil, &info); status != http.StatusOK || info.SigninOrigin != p.signin {
			t.Errorf("edge info on %s: %d %s %+v, want sign-in origin %s", p.relay, status, msg, info, p.signin)
		}
		var start edgeproto.DeviceStartResponse
		if status, msg := h.apiCallOn(t, p.signin, http.MethodPost, edgeproto.PathDeviceStart, "",
			edgeproto.DeviceStartRequest{Key: newDeviceKey(t), Label: "laptop"}, &start); status != http.StatusOK || start.VerificationURI != p.signin+"/device" {
			t.Errorf("device start on %s: %d %s, verification URI %q", p.signin, status, msg, start.VerificationURI)
		}

		b := h.browser(t)
		b.origin = p.relay
		_, page := b.get(t, "/join/"+serverID+"/inv-0123456789")
		other := testRelay2
		if p.relay == testRelay2 {
			other = testRelay
		}
		page = html.UnescapeString(page)
		if !strings.Contains(page, p.relay+"/join/"+serverID+"/inv-0123456789") || !strings.Contains(page, "edge="+url.QueryEscape(p.relay)+"&") ||
			strings.Contains(page, other) {
			t.Errorf("join page on %s does not name only its own relay origin:\n%s", p.relay, page)
		}

		resp, body := b.get(t, edgeproto.PathUniversalResourceMetadata)
		var meta struct {
			Resource string `json:"resource"`
		}
		if err := json.Unmarshal([]byte(body), &meta); err != nil || resp.StatusCode != http.StatusOK || meta.Resource != p.relay+edgeproto.PathUniversalMCP {
			t.Errorf("connector metadata on %s: %s %s", p.relay, resp.Status, body)
		}
		resp, _ = b.get(t, edgeproto.PathUniversalMCPAuthorize+"?client_id=c")
		if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, p.signin+edgeproto.PathMCPChoose+"?") {
			t.Errorf("connector authorize on %s sends the browser to %q, want its own sign-in origin", p.relay, loc)
		}
	}
}

// TestOriginPairsSignInWithTheirOwnApp signs a browser in on each sign-in
// origin: each uses its own OAuth app, with its own callback and secret,
// and the session it gets is the same account's, on that origin only.
func TestOriginPairsSignInWithTheirOwnApp(t *testing.T) {
	type exchange struct{ client, secret, redirect string }
	var mu sync.Mutex
	var exchanges []exchange
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, secret, _ := r.BasicAuth()
		mu.Lock()
		exchanges = append(exchanges, exchange{client, secret, r.FormValue("redirect_uri")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"fake-access-token","token_type":"bearer"}`) //nolint:errcheck // test server
	}))
	t.Cleanup(token.Close)
	h := newHarness(t, func(cfg *Config, p *fakeProvider) {
		cfg.GitHub.TokenURL = token.URL
		app := p.app()
		app.ClientID, app.ClientSecret, app.TokenURL = "test-client-2", "test-secret-2", token.URL
		cfg.MorePairs = []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2, GitHub: app}}
	})
	h.setGitHubUser(1, "owner", "owner@example.test", true)

	for _, p := range []struct{ origin, client, secret string }{
		{testSignin2, "test-client-2", "test-secret-2"},
		{testSignin, "test-client", "test-secret"},
	} {
		mu.Lock()
		exchanges = nil
		mu.Unlock()
		b := h.browser(t)
		b.origin = p.origin
		resp, _ := b.get(t, "/signin/github?next=/servers")
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		callback := p.origin + "/signin/github/callback"
		if q := loc.Query(); q.Get("client_id") != p.client || q.Get("redirect_uri") != callback {
			t.Errorf("sign-in on %s sends GitHub client_id %q and redirect_uri %q, want %s and %s",
				p.origin, q.Get("client_id"), q.Get("redirect_uri"), p.client, callback)
		}
		resp, body := b.get(t, "/signin/github/callback?"+url.Values{"code": {"code"}, "state": {loc.Query().Get("state")}}.Encode())
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("callback on %s: %s\n%s", p.origin, resp.Status, body)
		}
		mu.Lock()
		got := exchanges
		mu.Unlock()
		if len(got) != 1 || got[0] != (exchange{p.client, p.secret, callback}) {
			t.Errorf("code exchange of the sign-in on %s: %+v, want %s with its secret and %s", p.origin, got, p.client, callback)
		}
		if resp, _ := b.get(t, "/servers"); resp.StatusCode != http.StatusOK {
			t.Errorf("/servers on %s after signing in there: %s", p.origin, resp.Status)
		}
		other := testSignin
		if p.origin == testSignin {
			other = testSignin2
		}
		b.origin = other
		if resp, _ := b.get(t, "/servers"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/signin?") {
			t.Errorf("/servers on %s after signing in on %s only: %s %v, want a sign-in", other, p.origin, resp.Status, resp.Header)
		}
	}
	if n, err := h.svc.store.HasIdentities(t.Context()); err != nil || !n {
		t.Fatalf("no account after two sign-ins: %v", err)
	}
}

// TestOriginPairsAssertionNamesTheServersOrigin opens the connector's
// choose page on each pair for a server enrolled at the other: the form
// posts to the page's own relay origin, and the assertion names the
// server's, which the server checks.
func TestOriginPairsAssertionNamesTheServersOrigin(t *testing.T) {
	h := newPairsHarness(t)
	h.setGitHubUser(1, "owner", "owner@example.test", true)
	owner := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "owner"}
	h.link.servers, h.link.enrolled = map[string]string{}, map[string]string{}
	for _, p := range []struct{ signin, relay, enrolled string }{
		{testSignin2, testRelay2, testRelay},
		{testSignin, testRelay, testRelay2},
	} {
		id := testServerID(t)
		h.claim(t, id, "workstation", owner)
		h.link.servers[id], h.link.enrolled[id] = "workstation", p.enrolled
		b := h.browser(t)
		b.origin = p.signin
		b.signIn(t)
		q := url.Values{"client_id": {"https://claude.ai/client"}, "code_challenge": {"challenge"}, "redirect_uri": {"https://claude.ai/callback"}}
		resp, page := b.get(t, edgeproto.PathMCPChoose+"?"+q.Encode())
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, `action="`+p.relay+edgeproto.PathUniversalMCPAuthorize+`"`) {
			t.Fatalf("choose page on %s: %s\n%s", p.signin, resp.Status, page)
		}
		re := regexp.MustCompile(`value="` + id + `"[\s\S]*?name="edge_assertion" value="([^"]+)"`)
		m := re.FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no assertion for %s on the choose page:\n%s", id, page)
		}
		if _, err := edgeproto.VerifyConnectorAssertion(h.svc.EdgeKey(), html.UnescapeString(m[1]), p.enrolled, id,
			q.Get("client_id"), q.Get("code_challenge"), q.Get("redirect_uri"), time.Now()); err != nil {
			t.Errorf("assertion on %s for a server enrolled at %s: %v", p.signin, p.enrolled, err)
		}
	}
}
