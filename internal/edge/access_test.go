package edge

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/edgeproto"
)

func TestAdmissionMatrix(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.clock.Now()
	owner := edgeproto.Account{Provider: "github", Subject: "1", Login: "owner"}
	member := edgeproto.Account{Provider: "google", Subject: "g-2", Email: "member@example.test"}
	loginInvitee := edgeproto.Account{Provider: "github", Subject: "3", Login: "Invitee"}
	emailInvitee := edgeproto.Account{Provider: "google", Subject: "g-4", Email: "Invited@Example.test"}
	unverified := edgeproto.Account{Provider: "google", Subject: "g-5"}
	kelvin := edgeproto.Account{Provider: "github", Subject: "6", Login: "Kelvin"}
	sameSubjectOtherProvider := edgeproto.Account{Provider: "google", Subject: "1"}
	expiredInvitee := edgeproto.Account{Provider: "github", Subject: "7", Login: "late"}

	id := testServerID(t)
	other := testServerID(t)
	if err := h.svc.RecordClaim(ctx, id, "alpha", owner); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RecordClaim(ctx, other, "beta", edgeproto.Account{Provider: "github", Subject: "99"}); err != nil {
		t.Fatal(err)
	}
	if role, err := h.svc.Admit(ctx, id, owner); err != nil || role != "admin" {
		t.Fatalf("owner before any directory = %q, %v; want admin", role, err)
	}
	if err := h.svc.ReplaceDirectory(ctx, id, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: "github", Subject: "1", Role: "admin"},
		{Kind: edgeproto.EntryMember, Provider: "google", Subject: "g-2", Role: "collaborator"},
		{Kind: edgeproto.EntryInvitation, Provider: "github", Login: "invitee", Role: "viewer", ExpiresAt: now.Add(time.Hour)},
		{Kind: edgeproto.EntryInvitation, Email: "invited@example.test", Role: "collaborator", ExpiresAt: now.Add(time.Hour)},
		{Kind: edgeproto.EntryInvitation, Provider: "github", Login: "kelvin", Role: "viewer", ExpiresAt: now.Add(time.Hour)},
		{Kind: edgeproto.EntryInvitation, Provider: "github", Login: "late", Role: "viewer", ExpiresAt: now.Add(-time.Second)},
	}}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		server  string
		account edgeproto.Account
		role    string
		err     error
	}{
		{"owner", id, owner, "admin", nil},
		{"member", id, member, "collaborator", nil},
		{"login invitation, any ASCII case", id, loginInvitee, "viewer", nil},
		{"email invitation, verified email", id, emailInvitee, "collaborator", nil},
		{"account without a verified email", id, unverified, "", edgeproto.RefusalNotMember},
		{"Kelvin sign does not fold to k", id, kelvin, "", edgeproto.RefusalNotMember},
		{"same subject, other provider", id, sameSubjectOtherProvider, "", edgeproto.RefusalNotMember},
		{"expired invitation", id, expiredInvitee, "", edgeproto.RefusalNotMember},
		{"member of another server", other, member, "", edgeproto.RefusalNotMember},
		{"unknown server", testServerID(t), owner, "", edgeproto.RefusalUnknownServer},
	} {
		role, err := h.svc.Admit(ctx, tc.server, tc.account)
		if role != tc.role || !errors.Is(err, tc.err) {
			t.Errorf("%s: Admit = %q, %v; want %q, %v", tc.name, role, err, tc.role, tc.err)
		}
	}

	// A directory push replaces the previous one.
	if err := h.svc.ReplaceDirectory(ctx, id, edgeproto.Directory{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Admit(ctx, id, member); err != edgeproto.RefusalNotMember {
		t.Errorf("member removed from the directory: Admit err = %v", err)
	}
	if err := h.svc.ReplaceDirectory(ctx, testServerID(t), edgeproto.Directory{}); err == nil {
		t.Error("directory for an unclaimed server accepted")
	}
}

func TestClaim(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	id := testServerID(t)
	code, err := edgeproto.NewClaimCode(id)
	if err != nil {
		t.Fatal(err)
	}
	h.link.servers = map[string]string{id: "workstation", testServerID(t): "other"}
	h.link.claim = func(presented string, a edgeproto.Account, d edgeproto.Device) error {
		if a.Login != "owner" || d.Key != "" || !edgeproto.ClaimCodeEqual(code, presented) {
			return edgeproto.RefusalClaimWrong
		}
		return nil
	}

	resp, page := b.post(t, "/servers/add", url.Values{"code": {code[:9] + "aaaaaaaaaaaaaaaa"}})
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, string(edgeproto.RefusalClaimWrong)) {
		t.Errorf("wrong claim code: %s\n%s", resp.Status, page)
	}
	resp, page = b.post(t, "/servers/add", url.Values{"code": {" " + strings.ToUpper(code) + " "}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "workstation") {
		t.Fatalf("claim: %s\n%s", resp.Status, page)
	}
	if _, page = b.get(t, "/servers"); !strings.Contains(page, id) || !strings.Contains(page, "online") {
		t.Errorf("claimed server missing from the servers page:\n%s", page)
	}
}

func TestClaimAPIIsRateLimited(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	var status int
	for range 6 {
		resp, _ := b.post(t, "/servers/add", url.Values{"code": {"not-a-code"}})
		status = resp.StatusCode
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("6th claim attempt: %d, want 429", status)
	}
}

func TestCSRF(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	h.setGitHubUser(2, "other", "other@example.test", true)
	other := h.browser(t)
	other.signIn(t, edgeproto.ProviderGitHub)

	for name, token := range map[string]string{"no token": "", "another session's token": other.csrf(t)} {
		resp, _ := b.do(t, http.MethodPost, "/signout", url.Values{"csrf": {token}}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("sign out with %s: %s, want 403", name, resp.Status)
		}
	}
	resp, _ := b.do(t, http.MethodPost, "/signout", url.Values{"csrf": {b.csrf(t)}},
		http.Header{"Sec-Fetch-Site": {"cross-site"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site sign out with a valid token: %s, want 403", resp.Status)
	}
	if resp, _ := b.get(t, "/servers"); resp.StatusCode != http.StatusOK {
		t.Fatalf("browser was signed out by a refused request: %s", resp.Status)
	}
	if resp, _ := b.post(t, "/signout", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("sign out: %s", resp.Status)
	}
	if resp, _ := b.get(t, "/servers"); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("signed-out browser reached /servers: %s", resp.Status)
	}
}

// webSignIn runs the authorize flow for server id with challenge and
// returns the redirect the edge answered with.
func webSignIn(t *testing.T, b *browser, id, challenge string, app bool) *url.URL {
	t.Helper()
	form := url.Values{"server": {id}, "state": {"server-state-1"}, "challenge": {challenge}}
	if app {
		form.Set("return", "app")
	}
	resp, page := b.get(t, "/authorize?"+form.Encode())
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, id) {
		t.Fatalf("authorize page: %s\n%s", resp.Status, page)
	}
	resp, page = b.post(t, "/authorize", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorize: %s\n%s", resp.Status, page)
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestWebSignInCode(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	ctx := context.Background()
	owner := edgeproto.Account{Provider: "github", Subject: "1"}
	id, other := testServerID(t), testServerID(t)
	for _, srv := range []string{id, other} {
		if err := h.svc.RecordClaim(ctx, srv, "srv", owner); err != nil {
			t.Fatal(err)
		}
	}
	verifier := edgeproto.NewVerifier()
	challenge := edgeproto.PKCEChallenge(verifier)

	u := webSignIn(t, b, id, challenge, false)
	if u.Scheme != "https" || u.Host != id+"."+testDomain || u.Path != edgeproto.PathAuthCallback ||
		u.Query().Get("state") != "server-state-1" {
		t.Fatalf("callback = %s", u)
	}
	code := u.Query().Get("code")

	// Another server cannot use the code, and trying uses it up.
	redeem := edgeproto.WebRedeem{ID: edgeproto.NewConnID(), Code: code, Verifier: verifier}
	if res := h.svc.RedeemWebCode(ctx, other, redeem); !strings.Contains(res.Error, "another server") {
		t.Errorf("redeem at another server = %+v", res)
	}
	if res := h.svc.RedeemWebCode(ctx, id, redeem); !strings.Contains(res.Error, "unknown or already used") {
		t.Errorf("redeem after another server tried = %+v", res)
	}

	// A wrong verifier fails and uses the code up.
	code = webSignIn(t, b, id, challenge, false).Query().Get("code")
	wrong := edgeproto.WebRedeem{ID: edgeproto.NewConnID(), Code: code, Verifier: edgeproto.NewVerifier()}
	if res := h.svc.RedeemWebCode(ctx, id, wrong); !strings.Contains(res.Error, "verifier does not match") {
		t.Errorf("redeem with a wrong verifier = %+v", res)
	}
	if res := h.svc.RedeemWebCode(ctx, id, edgeproto.WebRedeem{ID: wrong.ID, Code: code, Verifier: verifier}); res.Grant != "" {
		t.Error("code redeemed after a wrong verifier used it")
	}

	// The right server with the right verifier gets a web grant, once.
	code = webSignIn(t, b, id, challenge, false).Query().Get("code")
	ok := edgeproto.WebRedeem{ID: edgeproto.NewConnID(), Code: code, Verifier: verifier}
	res := h.svc.RedeemWebCode(ctx, id, ok)
	if res.Error != "" || res.ID != ok.ID {
		t.Fatalf("redeem = %+v", res)
	}
	g, err := edgeproto.VerifyGrant(h.svc.EdgeKey(), res.Grant,
		edgeproto.GrantScope{ServerID: id, ConnID: ok.ID, Kind: edgeproto.KindWeb}, h.clock.Now())
	if err != nil || g.Account.Login != "owner" {
		t.Fatalf("web grant = %+v, %v", g, err)
	}
	if res := h.svc.RedeemWebCode(ctx, id, ok); res.Grant != "" {
		t.Error("code redeemed twice")
	}

	// A code expires.
	code = webSignIn(t, b, id, challenge, false).Query().Get("code")
	h.clock.Advance(webCodeTTL)
	if res := h.svc.RedeemWebCode(ctx, id, edgeproto.WebRedeem{ID: edgeproto.NewConnID(), Code: code, Verifier: verifier}); !strings.Contains(res.Error, "expired") {
		t.Errorf("redeem an expired code = %+v", res)
	}

	// The app variant returns to the app, only when asked.
	if u := webSignIn(t, b, id, challenge, true); u.Scheme != "aether" || u.Host != "auth" || u.Path != "/callback" {
		t.Errorf("app callback = %s", u)
	}
}

func TestAuthorizeRefusals(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	id := testServerID(t)
	if err := h.svc.RecordClaim(context.Background(), id, "srv", edgeproto.Account{Provider: "github", Subject: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	challenge := edgeproto.PKCEChallenge(edgeproto.NewVerifier())
	for name, tc := range map[string]struct {
		query  url.Values
		status int
	}{
		"not a member":     {url.Values{"server": {id}, "state": {"s"}, "challenge": {challenge}}, http.StatusForbidden},
		"unknown server":   {url.Values{"server": {testServerID(t)}, "state": {"s"}, "challenge": {challenge}}, http.StatusNotFound},
		"return elsewhere": {url.Values{"server": {id}, "state": {"s"}, "challenge": {challenge}, "return": {"https://evil.example"}}, http.StatusBadRequest},
		"no challenge":     {url.Values{"server": {id}, "state": {"s"}}, http.StatusBadRequest},
		"bad state":        {url.Values{"server": {id}, "state": {"a b"}, "challenge": {challenge}}, http.StatusBadRequest},
	} {
		if resp, _ := b.get(t, "/authorize?"+tc.query.Encode()); resp.StatusCode != tc.status {
			t.Errorf("%s: GET %s, want %d", name, resp.Status, tc.status)
		}
		if resp, _ := b.post(t, "/authorize", tc.query); resp.StatusCode != tc.status {
			t.Errorf("%s: POST %s, want %d", name, resp.Status, tc.status)
		}
	}

	// A signed-out browser is sent to sign in and back.
	resp, _ := h.browser(t).get(t, "/authorize?"+url.Values{"server": {id}, "state": {"s"}, "challenge": {challenge}}.Encode())
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/signin?next=%2Fauthorize") {
		t.Errorf("signed-out authorize: %s to %q", resp.Status, loc)
	}
}

func TestRemoveServer(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	ctx := context.Background()
	mine, theirs := testServerID(t), testServerID(t)
	owner := sessionAccount(t, h, b)
	if err := h.svc.RecordClaim(ctx, mine, "mine", owner); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RecordClaim(ctx, theirs, "theirs", edgeproto.Account{Provider: "github", Subject: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ReplaceDirectory(ctx, theirs, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: owner.Provider, Subject: owner.Subject, Role: "collaborator"},
	}}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := b.post(t, "/servers/remove", url.Values{"server": {theirs}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("member removed a server they do not own: %s", resp.Status)
	}
	if resp, _ := b.post(t, "/servers/remove", url.Values{"server": {mine}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("owner removes server: %s", resp.Status)
	}
	if _, err := h.svc.Admit(ctx, mine, owner); err != edgeproto.RefusalUnknownServer {
		t.Errorf("removed server still admits: %v", err)
	}
	if len(h.link.unenroll) != 1 || h.link.unenroll[0] != mine {
		t.Errorf("unenrolled = %v, want [%s]", h.link.unenroll, mine)
	}
	if state, err := h.svc.ServerConnected(ctx, mine, "mine"); err != nil || state != edgeproto.StateUnclaimed {
		t.Errorf("removed server state = %q, %v", state, err)
	}
}

func TestEdgeKeyPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, keyFile)
	first, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %04o, want 0600", info.Mode().Perm())
	}
	second, err := loadOrCreateKey(path)
	if err != nil || !first.Equal(second) {
		t.Fatalf("reloaded key differs: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateKey(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("world-readable key loaded: %v", err)
	}
}

func TestLimiterGroupsIPv6By64(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	l := newLimiter[netip.Prefix](2, time.Minute, clock.Now)
	req := func(addr string) netip.Prefix {
		return addrKey(&http.Request{RemoteAddr: netip.AddrPortFrom(netip.MustParseAddr(addr), 1234).String()})
	}
	if !l.allow(req("2001:db8::1")) || !l.allow(req("2001:db8::ffff:2")) {
		t.Fatal("first two requests refused")
	}
	if l.allow(req("2001:db8::3")) {
		t.Error("third request from the same /64 allowed")
	}
	if !l.allow(req("2001:db8:0:1::1")) || !l.allow(req("192.0.2.1")) {
		t.Error("other blocks share the /64's budget")
	}
	clock.Advance(time.Minute)
	if !l.allow(req("2001:db8::4")) {
		t.Error("budget did not refill")
	}
}
