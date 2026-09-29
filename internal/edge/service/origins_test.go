package edge

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

func TestOriginsMustBeTwoHosts(t *testing.T) {
	for _, tt := range []struct {
		signin, relay, want string
	}{
		{testSignin, testSignin, "must have different host names"},
		{"https://edge.example.test:8443", "https://edge.example.test", "must have different host names"},
		{"https://EDGE.example.test", "https://edge.example.test", "must have different host names"},
		{"", testRelay, "sign-in origin"},
		{testSignin, "https://relay.example.test/path", "relay origin"},
		{"http://signin.example.test", testRelay, "must use https"},
	} {
		_, err := New(Config{DataDir: t.TempDir(), SigninOrigin: tt.signin, RelayOrigin: tt.relay,
			GitHub: &OAuthApp{ClientID: "id", ClientSecret: "secret"}})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("New(%q, %q) = %v, want an error containing %q", tt.signin, tt.relay, err, tt.want)
		}
	}
}

// TestEachOriginServesOnlyItsOwnPaths checks that no page, cookie or
// client API answers on the relay host, and no relay endpoint on the
// sign-in host.
func TestEachOriginServesOnlyItsOwnPaths(t *testing.T) {
	h := newHarness(t)
	serverID := testServerID(t)
	for _, tt := range []struct {
		origin, method, path string
		status               int
		want                 string
	}{
		{testRelay, http.MethodGet, "/signin", http.StatusMisdirectedRequest, "/signin is served on " + testSignin},
		{testRelay, http.MethodGet, "/signin/github", http.StatusMisdirectedRequest, "is served on " + testSignin},
		{testRelay, http.MethodGet, "/signin/github/callback?code=x&state=y", http.StatusMisdirectedRequest, "is served on " + testSignin},
		{testRelay, http.MethodGet, "/servers", http.StatusMisdirectedRequest, "is served on " + testSignin},
		{testRelay, http.MethodPost, "/account/delete", http.StatusMisdirectedRequest, "is served on " + testSignin},
		{testRelay, http.MethodPost, edgeproto.PathDeviceStart, http.StatusMisdirectedRequest, "is served on " + testSignin},
		{testRelay, http.MethodGet, edgeproto.PathServers, http.StatusMisdirectedRequest, "is served on " + testSignin},
		{testSignin, http.MethodGet, edgeproto.ConnectPath(serverID), http.StatusMisdirectedRequest, "is served on " + testRelay},
		{testSignin, http.MethodGet, edgeproto.PathEdgeInfo, http.StatusMisdirectedRequest, "is served on " + testRelay},
		{testRelay, http.MethodGet, edgeproto.ConnectPath(serverID), http.StatusOK, "relayed"},
		{testRelay, http.MethodGet, "/nothing-here", http.StatusNotFound, "404 page not found"},
		{"https://other.example.test", http.MethodGet, "/signin", http.StatusMisdirectedRequest,
			"this edge serves " + testSignin + " and " + testRelay + `, not the host "other.example.test"`},
		{testSignin, http.MethodGet, "/healthz", http.StatusOK, "ok"},
		{testRelay, http.MethodGet, "/healthz", http.StatusOK, "ok"},
		{"https://other.example.test", http.MethodGet, "/healthz", http.StatusOK, "ok"},
	} {
		b := h.browser(t)
		b.origin = tt.origin
		resp, body := b.do(t, tt.method, tt.path, url.Values{}, nil)
		var e edgeproto.ErrorBody
		if json.Unmarshal([]byte(body), &e) == nil && e.Error != "" {
			body = e.Error
		}
		if resp.StatusCode != tt.status || !strings.Contains(body, tt.want) {
			t.Errorf("%s %s%s: %s %q, want %d with %q", tt.method, tt.origin, tt.path, resp.Status, body, tt.status, tt.want)
		}
		if len(b.setCookies) != 0 {
			t.Errorf("%s %s%s set cookies %q", tt.method, tt.origin, tt.path, b.setCookies)
		}
	}
}

// TestOAuthCallbacksAreOnTheSigninOrigin checks the redirect address each
// provider is sent, which must match the one registered with it.
func TestOAuthCallbacksAreOnTheSigninOrigin(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{edgeproto.ProviderGitHub, edgeproto.ProviderGoogle} {
		resp, _ := h.browser(t).get(t, "/signin/"+p)
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := loc.Query().Get("redirect_uri"), testSignin+"/signin/"+p+"/callback"; got != want {
			t.Errorf("%s redirect_uri = %q, want %q", p, got, want)
		}
	}
}

// TestCookiesAreHostOnly collects every cookie the sign-in origin sets
// through a sign-in, a device approval and a sign-out. Each must be bound
// to the sign-in host alone: the __Host- prefix, which browsers refuse
// with a Domain attribute, Secure, and Path=/. A cookie with a Domain
// would reach every host under that domain.
func TestCookiesAreHostOnly(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	approvedDevice(t, h, b)
	b.get(t, "/account")
	b.post(t, "/signout", url.Values{})
	if len(b.setCookies) < 3 {
		t.Fatalf("only %d Set-Cookie lines: %q", len(b.setCookies), b.setCookies)
	}
	for _, line := range b.setCookies {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("Set-Cookie %q: %v", line, err)
		}
		if !strings.HasPrefix(c.Name, "__Host-") || c.Domain != "" || strings.Contains(strings.ToLower(line), "domain=") ||
			!c.Secure || c.Path != "/" || !c.HttpOnly {
			t.Errorf("Set-Cookie %q is not a host-only __Host- cookie", line)
		}
	}
}
