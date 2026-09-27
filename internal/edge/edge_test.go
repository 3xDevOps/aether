package edge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

// fakeProvider is an OAuth provider that serves both GitHub's and
// Google's user endpoints. It checks the PKCE verifier on every exchange.
type fakeProvider struct {
	*httptest.Server
	mu         sync.Mutex
	challenges map[string]string // code -> PKCE challenge
	githubUser map[string]any
	emails     []map[string]any
	googleInfo map[string]any
}

func newFakeProvider(t *testing.T) *fakeProvider {
	p := &fakeProvider{challenges: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		challenge, ok := p.challenges[r.FormValue("code")]
		delete(p.challenges, r.FormValue("code"))
		p.mu.Unlock()
		if !ok || !edgeproto.VerifyPKCE(challenge, r.FormValue("code_verifier")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant"}`) //nolint:errcheck // test server
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"fake-access-token","token_type":"bearer"}`) //nolint:errcheck // test server
	})
	serve := func(get func() any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer fake-access-token" {
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			json.NewEncoder(w).Encode(get()) //nolint:errcheck // test server
		}
	}
	mux.HandleFunc("GET /user", serve(func() any { return p.githubUser }))
	mux.HandleFunc("GET /user/emails", serve(func() any { return p.emails }))
	mux.HandleFunc("GET /v1/userinfo", serve(func() any { return p.googleInfo }))
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// authorize plays the person approving the sign-in at the provider: it
// reads the authorization URL the edge redirected to and returns the
// code and state the provider sends back.
func (p *fakeProvider) authorize(t *testing.T, location string) (code, state string) {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("authorization URL %s has no S256 PKCE challenge", location)
	}
	code = edgeproto.NewToken()
	p.mu.Lock()
	p.challenges[code] = q.Get("code_challenge")
	p.mu.Unlock()
	return code, q.Get("state")
}

func (p *fakeProvider) app() *OAuthApp {
	return &OAuthApp{
		ClientID: "test-client", ClientSecret: "test-secret",
		AuthURL: p.URL + "/authorize", TokenURL: p.URL + "/token", APIURL: p.URL,
	}
}

// fakeLink is the relay: servers holds the connected servers by id, with
// their names, and claim answers a claim for the one server a code names.
type fakeLink struct {
	mu       sync.Mutex
	servers  map[string]string
	claim    func(code string, a edgeproto.Account, d edgeproto.Device) error
	revoked  []string
	unenroll []string
}

func (l *fakeLink) Online(serverID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.servers[serverID]
	return ok
}

func (l *fakeLink) Claim(_ context.Context, code string, a edgeproto.Account, d edgeproto.Device) (string, string, error) {
	normalized, prefix, err := edgeproto.ParseClaimCode(code)
	if err != nil {
		return "", "", edgeproto.RefusalClaimWrong
	}
	l.mu.Lock()
	var id, name string
	for sid, n := range l.servers {
		if strings.HasPrefix(sid, prefix) {
			id, name = sid, n
		}
	}
	claim := l.claim
	l.mu.Unlock()
	if id == "" {
		return "", "", edgeproto.RefusalNotConnected
	}
	if claim == nil {
		return "", "", fmt.Errorf("fake link: no claim handler")
	}
	if err := claim(normalized, a, d); err != nil {
		return "", "", err
	}
	return id, name, nil
}

func (l *fakeLink) Unenroll(serverID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unenroll = append(l.unenroll, serverID)
}

func (l *fakeLink) RevokeDevice(deviceID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.revoked = append(l.revoked, deviceID)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const testDomain = "servers.example.test"

type harness struct {
	svc      *Service
	ts       *httptest.Server
	provider *fakeProvider
	link     *fakeLink
	clock    *fakeClock
	dataDir  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	provider := newFakeProvider(t)
	ts := httptest.NewUnstartedServer(nil)
	ts.StartTLS()
	t.Cleanup(ts.Close)
	clock := &fakeClock{t: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	dataDir := t.TempDir()
	svc, err := New(Config{
		DataDir:      dataDir,
		Origin:       ts.URL,
		ServerDomain: testDomain,
		GitHub:       provider.app(),
		Google:       provider.app(),
		Clock:        clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() }) //nolint:errcheck // test cleanup
	link := &fakeLink{}
	svc.SetLink(link)
	ts.Config.Handler = svc.Handler()
	return &harness{svc: svc, ts: ts, provider: provider, link: link, clock: clock, dataDir: dataDir}
}

// browser is an HTTP client with its own cookies that does not follow
// redirects.
type browser struct {
	h      *harness
	client *http.Client
}

func (h *harness) browser(t *testing.T) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := *h.ts.Client()
	c.Jar = jar
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &browser{h: h, client: &c}
}

func (b *browser) do(t *testing.T, method, path string, form url.Values, header http.Header) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.h.ts.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(data)
}

func (b *browser) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	return b.do(t, http.MethodGet, path, nil, nil)
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// csrf reads the CSRF token from a signed-in page.
func (b *browser) csrf(t *testing.T) string {
	t.Helper()
	_, page := b.get(t, "/servers")
	m := csrfField.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no CSRF token on /servers:\n%s", page)
	}
	return m[1]
}

func (b *browser) post(t *testing.T, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	form.Set("csrf", b.csrf(t))
	return b.do(t, http.MethodPost, path, form, nil)
}

// signIn signs the browser in with provider, whose fake returns the user
// the test set on the harness's provider.
func (b *browser) signIn(t *testing.T, provider string) *http.Response {
	t.Helper()
	resp, _ := b.get(t, "/signin/"+provider+"?next=/devices")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start sign-in: %s", resp.Status)
	}
	code, state := b.h.provider.authorize(t, resp.Header.Get("Location"))
	resp, body := b.get(t, "/signin/"+provider+"/callback?"+url.Values{"code": {code}, "state": {state}}.Encode())
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in callback: %s\n%s", resp.Status, body)
	}
	return resp
}

func (h *harness) setGitHubUser(id int64, login, email string, verified bool) {
	h.provider.mu.Lock()
	defer h.provider.mu.Unlock()
	h.provider.githubUser = map[string]any{"id": id, "login": login, "name": "Test User"}
	h.provider.emails = []map[string]any{
		{"email": "secondary@example.test", "primary": false, "verified": true},
		{"email": email, "primary": true, "verified": verified},
	}
}

func (h *harness) setGoogleUser(sub, email string, verified any) {
	h.provider.mu.Lock()
	defer h.provider.mu.Unlock()
	h.provider.googleInfo = map[string]any{"sub": sub, "email": email, "email_verified": verified, "name": "Test User"}
}

// apiCall calls the JSON API with the current protocol version.
func (h *harness) apiCall(t *testing.T, method, path, token string, in, out any) (int, string) {
	t.Helper()
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, h.ts.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(edgeproto.HeaderVersion, fmt.Sprint(edgeproto.Version))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode %s: %v\n%s", path, err, data)
		}
	}
	var e edgeproto.ErrorBody
	json.Unmarshal(data, &e) //nolint:errcheck // not every answer is an ErrorBody
	return resp.StatusCode, e.Error
}

func newDeviceKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return edgeproto.DeviceKeyLine(key)
}

// testServerID is a well-formed server id.
func testServerID(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return edgeproto.ServerID(key)
}
