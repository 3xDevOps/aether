package edge

import (
	"crypto/ed25519"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edgestore "github.com/3xDevOps/Aether/internal/edge/store"
	"golang.org/x/crypto/ssh"
)

// galleryState is one page in one state, rendered from fixture data.
type galleryState struct {
	name, page string
	status     int
	view       view
}

func fixtureKey(t *testing.T, seed byte) ssh.PublicKey {
	t.Helper()
	s := make([]byte, ed25519.SeedSize)
	s[0] = seed
	key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(s).Public())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// galleryStates is every page the edge renders, in every state a person
// can meet, with fixture data and no store.
func galleryStates(t *testing.T) []galleryState {
	t.Helper()
	at := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	me := edgeproto.AccountInfo{ID: "acct_" + strings.Repeat("a", 26), Account: edgeproto.Account{
		Provider: "github", Subject: "1001", Login: "octo-dev", Email: "octo@example.com",
	}}
	long := edgeproto.AccountInfo{ID: "acct_" + strings.Repeat("b", 26), Account: edgeproto.Account{
		Provider: "github", Subject: "1002", Email: "a.very.long.address.for.a.person@subdomain.example.com",
	}}
	id := func(seed byte) string { return edgeproto.ServerID(fixtureKey(t, seed)) }
	srv := func(seed byte, name, role string, online bool, policy edgeproto.AccessPolicy, kind edgeproto.ServerKind) edgeproto.ServerInfo {
		return edgeproto.ServerInfo{ID: id(seed), Name: name, Role: role, Online: online, AccessPolicy: policy, Kind: kind}
	}
	moss := srv(1, "moss", "owner", true, edgeproto.PolicyAccount, edgeproto.ServerSelfHosted)
	fern := srv(2, "fern", "admin", false, edgeproto.PolicyApprovedDevices, edgeproto.ServerSelfHosted)
	hosted := srv(3, "team-build-server-in-the-frankfurt-region-number-two", "member", true, edgeproto.PolicyApprovedDevices, edgeproto.ServerHosted)
	rows := []serverRow{{ServerInfo: moss, Owner: true}, {ServerInfo: fern, Member: true}, {ServerInfo: hosted, Member: true}}

	devices := []edgestore.Device{
		{ID: "dev-1", Key: edgeproto.DeviceKeyLine(fixtureKey(t, 4)), Label: "laptop", CreatedAt: at.Add(-72 * time.Hour), LastUsedAt: at},
		{ID: "dev-2", Key: edgeproto.DeviceKeyLine(fixtureKey(t, 5)), Label: "build-runner-07.internal.example.com (linux/arm64)", CreatedAt: at.Add(-30 * 24 * time.Hour), LastUsedAt: at.Add(-26 * time.Hour)},
	}
	confirm := deviceConfirm{
		UserCode: "BCDF-GHJK", Label: "laptop", Fingerprint: keyFingerprint(edgeproto.DeviceKeyLine(fixtureKey(t, 6))),
		Started: 42 * time.Second, From: "203.0.113.7", Here: "203.0.113.7",
	}
	elsewhere := confirm
	elsewhere.From, elsewhere.Here = "198.51.100.23", "2001:db8:4:12::1f"

	fields := []field{
		{"client_id", "https://chatgpt.com/connector/oauth/abc"}, {"code_challenge", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		{"redirect_uri", "https://chatgpt.com/connector_platform_oauth_redirect"}, {"state", "s1"},
	}
	choose := chooseData{
		Action: "https://relay.example.test" + edgeproto.PathUniversalMCPAuthorize, Client: "chatgpt.com", Fields: fields,
		Ready: []choice{{ServerInfo: moss, Assertion: "fixture"}, {ServerInfo: hosted, Assertion: "fixture"}},
		Other: []choice{{ServerInfo: fern, Reason: "not connected to this edge"}},
	}
	noneReady := chooseData{Action: choose.Action, Client: "claude.ai", Fields: fields,
		Other: []choice{{ServerInfo: fern, Reason: "it runs a Cloudfield version without the universal connector; update it"}}}

	sum := edgeproto.AccountSummary{Account: me, Owned: []edgeproto.ServerInfo{moss}, Member: []edgeproto.ServerInfo{fern, hosted}, Confirm: confirmText(me)}
	empty := edgeproto.AccountSummary{Account: long, Owned: []edgeproto.ServerInfo{}, Member: []edgeproto.ServerInfo{}, Confirm: confirmText(long)}
	acct := func(s edgeproto.AccountSummary, fresh bool) accountPage {
		return accountPage{AccountSummary: s, Fresh: fresh, Reauth: "/signin/github?next=%2Faccount", Window: reauthWindow, Transfer: transferCommand}
	}

	in := func(a edgeproto.AccountInfo, title string, data any) view {
		return view{Title: title, Account: &a, CSRF: "fixture-csrf", Data: data}
	}
	out := func(title string, data any) view { return view{Title: title, Data: data} }
	withErr := func(v view, msg string) view { v.Error = msg; return v }
	join := struct {
		App  template.URL
		Link string
	}{template.URL("aether://join?edge=https%3A%2F%2Frelay.example.test&server=" + moss.ID + "&invitation=inv-abcdefghjk"),
		"https://relay.example.test/join/" + moss.ID + "/inv-abcdefghjk"}

	return []galleryState{
		{"signin", "signin", 200, out("Sign in", signinData{GitHub: "/signin/github?next=%2Fservers"})},
		{"signin-reviewer", "signin", 200, out("Sign in", signinData{GitHub: "/signin/github?next=%2Fconnectors%2Fchoose", Reviewer: "/connectors/reviewer?state=s1"})},
		{"reviewer", "reviewer", 200, out("Reviewer sign-in", reviewerData{Fields: fields})},
		{"choose", "choose", 200, in(me, "Choose a server", choose)},
		{"choose-none", "choose", 200, in(long, "Choose a server", noneReady)},
		{"servers", "servers", 200, in(me, "Servers", rows)},
		{"servers-empty", "servers", 200, in(long, "Servers", []serverRow(nil))},
		{"devices", "devices", 200, in(me, "Devices", devices)},
		{"devices-empty", "devices", 200, in(me, "Devices", []edgestore.Device(nil))},
		{"device", "device", 200, in(me, "Sign in a device", nil)},
		{"device-bad-code", "device", 400, withErr(in(me, "Sign in a device", nil), "a code has 8 letters, like BCDF-GHJK; check what your terminal shows")},
		{"device-expired", "device", 404, withErr(in(me, "Sign in a device", nil), "no sign-in is waiting for code BCDFGHJK; codes expire after 10m0s, so run aether login again")},
		{"device-confirm", "device_confirm", 200, in(me, "Confirm this device", confirm)},
		{"device-confirm-elsewhere", "device_confirm", 200, in(long, "Confirm this device", elsewhere)},
		{"device-approved", "device_done", 200, in(me, "Device signed in", struct {
			Approved bool
			Label    string
		}{true, "laptop"})},
		{"device-denied", "device_done", 200, in(me, "Device denied", struct {
			Approved bool
			Label    string
		}{false, "laptop"})},
		{"account", "account_page", 200, in(me, "Account", acct(sum, true))},
		{"account-reauth", "account_page", 200, in(me, "Account", acct(sum, false))},
		{"account-empty", "account_page", 200, in(long, "Account", acct(empty, true))},
		{"account-wrong-confirm", "account_page", 400, withErr(in(me, "Account", acct(sum, true)), "the confirmation does not name this account: type octo-dev")},
		{"account-deleted", "account_deleted", 200, out("Account deleted", sum)},
		{"join", "join", 200, out("Join a Cloudfield server", join)},
		{"error-csrf", "error", 403, withErr(in(me, http.StatusText(http.StatusForbidden), nil), "this form did not come from this edge page or your session changed; reload the page and try again")},
		{"error-github", "error", 403, withErr(out(http.StatusText(http.StatusForbidden), nil), "GitHub sign-in failed: access_denied The user has denied your application access.")},
		{"error-server", "error", 500, withErr(out(http.StatusText(500), nil), "edge: store: database is locked")},
	}
}

func renderState(t *testing.T, s *Service, st galleryState) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.render(w, st.status, st.page, st.view)
	return w
}

// externalRef finds a URL that would make a browser load something from
// another host: an absolute or protocol-relative src, a link or a CSS url.
var externalRef = regexp.MustCompile(`(?i)(?:\bsrc\s*=\s*["']?|<link[^>]*\bhref\s*=\s*["']?|url\(\s*["']?|@import\s+["']?)(?:[a-z][a-z0-9+.-]*:|//)`)

func TestEveryPageRenders(t *testing.T) {
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{pages: pages}
	for _, st := range galleryStates(t) {
		t.Run(st.name, func(t *testing.T) {
			w := renderState(t, s, st)
			body := w.Body.String()
			if w.Code != st.status {
				t.Fatalf("status %d, want %d: %s", w.Code, st.status, body)
			}
			if n := strings.Count(body, "<h1"); n != 1 {
				t.Errorf("%d h1 elements, want 1", n)
			}
			if st.view.Error != "" && !strings.Contains(body, html.EscapeString(st.view.Error)) {
				t.Errorf("page does not show the error %q", st.view.Error)
			}
			if m := externalRef.FindString(body); m != "" {
				t.Errorf("page loads from another host: %q", m)
			}
			if strings.Contains(body, "style=") || strings.Contains(body, "<style") {
				t.Error("page carries an inline style, which its CSP refuses")
			}
		})
	}
}

func TestAssetsLoadNothingFromAnotherHost(t *testing.T) {
	err := fs.WalkDir(templateFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, ".woff2") {
			return err
		}
		b, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}
		if m := externalRef.FindString(string(b)); m != "" {
			t.Errorf("%s loads from another host: %q", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPageGallery serves every page state at EDGE_GALLERY_ADDR, with the
// headers the edge sends, until the test is stopped:
//
//	EDGE_GALLERY_ADDR=127.0.0.1:8790 go test -run TestPageGallery -timeout 0 ./internal/edge/service
func TestPageGallery(t *testing.T) {
	addr := os.Getenv("EDGE_GALLERY_ADDR")
	if addr == "" {
		t.Skip("EDGE_GALLERY_ADDR is not set")
	}
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{pages: pages, pairs: []*pair{{signin: "http://" + addr}}}
	states := galleryStates(t)
	mux := http.NewServeMux()
	registerAssets(mux)
	mux.HandleFunc("GET /join.js", serveJoinScript)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("<!doctype html><title>Edge pages</title><ul>")
		for _, st := range states {
			fmt.Fprintf(&b, `<li><a href="/g/%s">%s</a>`, st.name, st.name)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(b.String())) //nolint:errcheck // a test page
	})
	for _, st := range states {
		csp := pageCSP
		switch st.page {
		case "join":
			csp += "; script-src 'self'"
		case "choose":
			csp += " https://relay.example.test"
		}
		mux.Handle("GET /g/"+st.name, s.pageHeaders(csp, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.render(w, st.status, st.page, st.view)
		})))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("serving %d page states at http://%s/", len(states), ln.Addr())
	t.Fatal(http.Serve(ln, mux))
}
