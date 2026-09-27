package edge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

func sessionAccount(t *testing.T, h *harness, b *browser) edgeproto.Account {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.ts.URL+"/servers", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.client.Jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	v, ok, err := h.svc.visitor(req)
	if err != nil || !ok {
		t.Fatalf("browser is not signed in: ok=%v err=%v", ok, err)
	}
	return v.Account
}

func TestSignInGitHub(t *testing.T) {
	h := newHarness(t)
	h.setGitHubUser(4242, "Octo-Cat", "octo@example.test", true)
	b := h.browser(t)
	resp := b.signIn(t, edgeproto.ProviderGitHub)
	if got := resp.Header.Get("Location"); got != "/devices" {
		t.Errorf("redirect after sign-in = %q, want /devices", got)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			session = c
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/" {
		t.Fatalf("session cookie = %+v, want __Host- cookie that is Secure, HttpOnly, SameSite=Lax, Path=/", session)
	}
	want := edgeproto.Account{Provider: "github", Subject: "4242", Login: "Octo-Cat", Email: "octo@example.test", Name: "Test User",
		IdentityAt: h.clock.Now()}
	if got := sessionAccount(t, h, b); got != want {
		t.Errorf("account = %+v, want %+v", got, want)
	}
}

func TestSignInDropsUnverifiedEmail(t *testing.T) {
	h := newHarness(t)
	h.setGitHubUser(7, "octo", "unverified@example.test", false)
	b := h.browser(t)
	b.signIn(t, edgeproto.ProviderGitHub)
	if got := sessionAccount(t, h, b).Email; got != "" {
		t.Errorf("GitHub email = %q, want none: the primary email is unverified", got)
	}

	for _, verified := range []any{false, "true", nil} {
		h.setGoogleUser("google-sub-1", "person@example.test", verified)
		g := h.browser(t)
		g.signIn(t, edgeproto.ProviderGoogle)
		if got := sessionAccount(t, h, g).Email; got != "" {
			t.Errorf("Google email with email_verified=%#v = %q, want none", verified, got)
		}
	}
	h.setGoogleUser("google-sub-1", "person@example.test", true)
	g := h.browser(t)
	g.signIn(t, edgeproto.ProviderGoogle)
	if got := sessionAccount(t, h, g).Email; got != "person@example.test" {
		t.Errorf("Google verified email = %q, want person@example.test", got)
	}
}

func TestSignInStateMismatch(t *testing.T) {
	h := newHarness(t)
	h.setGitHubUser(7, "octo", "octo@example.test", true)
	b := h.browser(t)
	resp, _ := b.get(t, "/signin/github")
	code, _ := h.provider.authorize(t, resp.Header.Get("Location"))
	resp, body := b.get(t, "/signin/github/callback?"+url.Values{"code": {code}, "state": {edgeproto.NewToken()}}.Encode())
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "does not belong to the sign-in this browser started") {
		t.Fatalf("callback with another state: %s\n%s", resp.Status, body)
	}
	if resp, _ = b.get(t, "/servers"); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("browser is signed in after a state mismatch: %s", resp.Status)
	}

	// A callback in a browser that never started a sign-in is refused too.
	other := h.browser(t)
	resp, _ = b.get(t, "/signin/github")
	code, state := h.provider.authorize(t, resp.Header.Get("Location"))
	resp, body = other.get(t, "/signin/github/callback?"+url.Values{"code": {code}, "state": {state}}.Encode())
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "no sign-in in progress") {
		t.Fatalf("callback in another browser: %s\n%s", resp.Status, body)
	}
}

func TestSignInNextStaysOnEdge(t *testing.T) {
	for _, next := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example", "javascript:alert(1)"} {
		if got := localPath(next); got != "/servers" {
			t.Errorf("localPath(%q) = %q, want /servers", next, got)
		}
	}
	if got := localPath("/authorize?server=x&state=y"); got != "/authorize?server=x&state=y" {
		t.Errorf("localPath kept %q", got)
	}
}

func TestGitHubLoginBelongsToItsCurrentHolder(t *testing.T) {
	h := newHarness(t)
	id := testServerID(t)
	if err := h.svc.RecordClaim(context.Background(), id, "srv", edgeproto.Account{Provider: "github", Subject: "999"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ReplaceDirectory(context.Background(), id, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{{
		Kind: edgeproto.EntryInvitation, Provider: edgeproto.ProviderGitHub, Login: "octo", Role: "viewer",
		ExpiresAt: h.clock.Now().Add(time.Hour),
	}}}); err != nil {
		t.Fatal(err)
	}
	h.setGitHubUser(1, "octo", "first@example.test", true)
	first := h.browser(t)
	first.signIn(t, edgeproto.ProviderGitHub)
	// The first account renamed itself on GitHub and another took "OCTO".
	h.setGitHubUser(2, "OCTO", "second@example.test", true)
	second := h.browser(t)
	second.signIn(t, edgeproto.ProviderGitHub)

	if _, err := h.svc.Admit(context.Background(), id, sessionAccount(t, h, second)); err != nil {
		t.Errorf("current holder of the login: %v", err)
	}
	stale := sessionAccount(t, h, first)
	if stale.Login != "" {
		t.Errorf("previous holder still carries login %q", stale.Login)
	}
	if _, err := h.svc.Admit(context.Background(), id, stale); !errors.Is(err, edgeproto.RefusalNotMember) {
		t.Errorf("previous holder of the login: %v, want %v", err, edgeproto.RefusalNotMember)
	}
}

func TestAccountsAreNotMergedByEmail(t *testing.T) {
	h := newHarness(t)
	h.setGitHubUser(99, "same", "same@example.test", true)
	h.setGoogleUser("99", "same@example.test", true)
	gh, g := h.browser(t), h.browser(t)
	gh.signIn(t, edgeproto.ProviderGitHub)
	g.signIn(t, edgeproto.ProviderGoogle)
	a, b := sessionAccount(t, h, gh), sessionAccount(t, h, g)
	if a.Provider == b.Provider {
		t.Fatalf("both sign-ins produced %s accounts", a.Provider)
	}

	id := testServerID(t)
	if err := h.svc.RecordClaim(context.Background(), id, "srv", a); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Admit(context.Background(), id, b); err != edgeproto.RefusalNotMember {
		t.Errorf("Google account with the owner's email and subject admitted: %v", err)
	}
}

func TestPagesSendSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	resp, _ := h.browser(t).get(t, "/signin")
	for header, want := range map[string]string{
		"X-Frame-Options": "DENY",
		"Referrer-Policy": "no-referrer",
		"Cache-Control":   "no-store",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'none'", "form-action 'self' https://*." + testDomain + " aether:"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "script-src") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP %q allows script", csp)
	}
}

func TestProviderErrorNeedsTheBrowsersState(t *testing.T) {
	h := newHarness(t)
	b := h.browser(t)
	resp, _ := b.get(t, "/signin/github")
	h.provider.authorize(t, resp.Header.Get("Location"))
	spoof := url.Values{"error": {"access_denied"}, "error_description": {"call 555-0100 to unlock your account"}}
	resp, body := b.get(t, "/signin/github/callback?"+spoof.Encode())
	if resp.StatusCode != http.StatusBadRequest || strings.Contains(body, "555-0100") {
		t.Errorf("provider error without state: %s\n%s", resp.Status, body)
	}
	resp, _ = b.get(t, "/signin/github")
	_, state := h.provider.authorize(t, resp.Header.Get("Location"))
	spoof.Set("state", state)
	resp, body = b.get(t, "/signin/github/callback?"+spoof.Encode())
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "access_denied") {
		t.Errorf("provider error with state: %s\n%s", resp.Status, body)
	}
}
