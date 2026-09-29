package edge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

func TestAdmissionMatrix(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.clock.Now()
	owner := edgeproto.Account{Provider: "github", Subject: "1", Login: "owner"}
	member := edgeproto.Account{Provider: "github", Subject: "2", Login: "member", Email: "member@example.test"}
	loginInvitee := edgeproto.Account{Provider: "github", Subject: "3", Login: "Invitee", IdentityAt: now}
	emailInvitee := edgeproto.Account{Provider: "github", Subject: "4", Login: "someone", Email: "Invited@Example.test", IdentityAt: now}
	staleInvitee := loginInvitee
	staleInvitee.IdentityAt = now.Add(-edgeproto.IdentityMaxAge - time.Second)
	unverified := edgeproto.Account{Provider: "github", Subject: "5", Login: "unverified", IdentityAt: now}
	kelvin := edgeproto.Account{Provider: "github", Subject: "6", Login: "Kelvin", IdentityAt: now}
	// An account v0.5.2-alpha.3 signed in with Google.
	sameSubjectOtherProvider := edgeproto.Account{Provider: "google", Subject: "1", IdentityAt: now}
	expiredInvitee := edgeproto.Account{Provider: "github", Subject: "7", Login: "late", IdentityAt: now}

	id := testServerID(t)
	other := testServerID(t)
	h.claim(t, id, "alpha", owner)
	h.claim(t, other, "beta", edgeproto.Account{Provider: "github", Subject: "99"})
	if role, err := h.svc.Admit(ctx, id, owner); err != nil || role != "admin" {
		t.Fatalf("owner before any directory = %q, %v; want admin", role, err)
	}
	if err := h.svc.ReplaceDirectory(ctx, id, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: "github", Subject: "1", Role: "admin"},
		{Kind: edgeproto.EntryMember, Provider: "github", Subject: "2", Role: "collaborator"},
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
		{"login invitation, login not confirmed for a day", id, staleInvitee, "", edgeproto.RefusalIdentityStale},
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

// Alice signed in while she held the GitHub login "alice", then renamed,
// and Bob took the login without signing in to this edge. An invitation
// for "alice" is meant for Bob: a day after GitHub last confirmed Alice's
// login, her device token no longer matches it, and her next edge page
// asks GitHub again.
func TestStaleLoginMatchesNoInvitation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.setGitHubUser(1, "alice", "alice@example.test", true)
	b := h.browser(t)
	b.signIn(t)
	start := startDevice(t, h)
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	tok, status, msg := pollDevice(t, h, start.DeviceCode)
	if status != http.StatusOK {
		t.Fatalf("poll = %d %s", status, msg)
	}
	id := testServerID(t)
	h.claim(t, id, "bobs-box", edgeproto.Account{Provider: "github", Subject: "2"})
	if err := h.svc.ReplaceDirectory(ctx, id, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryInvitation, Provider: "github", Login: "alice", Role: "admin",
			ExpiresAt: h.clock.Now().Add(edgeproto.InvitationTTL)},
	}}); err != nil {
		t.Fatal(err)
	}
	dir := relayDirectory{h.svc}
	listed := func() []edgeproto.ServerInfo {
		t.Helper()
		var servers edgeproto.ServersResponse
		if status, msg := h.apiCall(t, http.MethodGet, edgeproto.PathServers, tok.Token, nil, &servers); status != http.StatusOK {
			t.Fatalf("servers = %d %s", status, msg)
		}
		return servers.Servers
	}
	admit := func() error {
		t.Helper()
		a, _, err := dir.Authenticate(ctx, tok.Token)
		if err != nil {
			t.Fatal(err)
		}
		return dir.Admit(ctx, id, a)
	}

	if got := listed(); len(got) != 1 || admit() != nil {
		t.Fatalf("within a day of the sign-in: servers %+v", got)
	}
	h.clock.Advance(edgeproto.IdentityMaxAge + time.Minute)
	if got := listed(); len(got) != 0 {
		t.Fatalf("a day later, the invitation for alice still lists for the account that held the login: %+v", got)
	}
	if err := admit(); !errors.Is(err, edgeproto.RefusalIdentityStale) {
		t.Fatalf("a day later, Admit = %v, want %q", err, edgeproto.RefusalIdentityStale)
	}
	resp, _ := b.get(t, "/servers")
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/signin/github?next=%2Fservers" {
		t.Fatalf("an edge page a day later: %s to %q, want GitHub asked again", resp.Status, loc)
	}
	h.setGitHubUser(1, "alice-old", "alice@example.test", true)
	b.signIn(t)
	if err := admit(); !errors.Is(err, edgeproto.RefusalNotMember) {
		t.Fatalf("after GitHub reports the new login, Admit = %v, want %q", err, edgeproto.RefusalNotMember)
	}
}

// approvedDevice signs a device in through b's approval and returns its
// token.
func approvedDevice(t *testing.T, h *harness, b *browser) edgeproto.DeviceTokenResponse {
	t.Helper()
	start := startDevice(t, h)
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	tok, status, msg := pollDevice(t, h, start.DeviceCode)
	if status != http.StatusOK {
		t.Fatalf("poll = %d %s", status, msg)
	}
	return tok
}

// TestOwnershipComesFromServerReports records owners the way the relay
// does when a server reports them, and checks what the edge refuses to
// record.
func TestOwnershipComesFromServerReports(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := testServerID(t)
	owner := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "owner"}
	heir := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2", Login: "heir"}

	if err := h.svc.RecordClaim(ctx, id, "workstation", edgeproto.PolicyAccount, owner); !errors.Is(err, refusalNoOwnerAccount) {
		t.Fatalf("claim by an account the edge does not hold: %v", err)
	}
	h.claim(t, id, "workstation", owner)
	if _, err := h.svc.store.SignIn(ctx, heir, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RecordClaim(ctx, id, "workstation", edgeproto.PolicyAccount, heir); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("second claim: %v", err)
	}
	if err := h.svc.TransferOwner(ctx, id, edgeproto.Principal{Type: edgeproto.PrincipalAccount,
		Provider: edgeproto.ProviderGitHub, Subject: "404"}); !errors.Is(err, refusalNoOwnerAccount) {
		t.Fatalf("transfer to an account the edge does not hold: %v", err)
	}
	if err := h.svc.TransferOwner(ctx, id, edgeproto.AccountPrincipal(heir)); err != nil {
		t.Fatal(err)
	}
	if role, err := h.svc.Admit(ctx, id, heir); err != nil || role != "admin" {
		t.Fatalf("new owner admitted as %q, %v", role, err)
	}
	if _, err := h.svc.Admit(ctx, id, owner); !errors.Is(err, edgeproto.RefusalNotMember) {
		t.Fatalf("previous owner, not in the directory: %v", err)
	}
	if err := h.svc.DropOwner(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.AdmitClaim(ctx, id, owner, netip.MustParseAddr("192.0.2.1")); err != nil {
		t.Fatalf("claim connection to an ownerless server: %v", err)
	}
	if err := h.svc.RecordClaim(ctx, id, "workstation", edgeproto.PolicyApprovedDevices, owner); err != nil {
		t.Fatalf("claim of an ownerless server: %v", err)
	}

	blocked := testServerID(t)
	if err := h.svc.store.BlockServer(ctx, blocked, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RecordClaim(ctx, blocked, "blocked", edgeproto.PolicyAccount, owner); !errors.Is(err, edgeproto.RefusalServerBlocked) {
		t.Fatalf("claim of a blocked server: %v", err)
	}
	if _, err := h.svc.ServerConnected(ctx, blocked, "blocked", edgeproto.PolicyAccount); !errors.Is(err, edgeproto.RefusalServerBlocked) {
		t.Fatalf("blocked server enrolls: %v", err)
	}
	if err := h.svc.DropOwner(ctx, testServerID(t)); !errors.Is(err, refusalNeverClaimed) {
		t.Fatalf("ownerless report of an unclaimed server: %v", err)
	}

	// No request to either origin records an owner.
	tok := approvedDevice(t, h, signedInBrowser(t, h))
	for _, origin := range []string{testSignin, testRelay} {
		if status, _ := h.apiCallOn(t, origin, http.MethodPost, "/v1/claim", tok.Token, map[string]string{"code": id}, nil); status != http.StatusNotFound {
			t.Errorf("POST %s/v1/claim: %d, want 404", origin, status)
		}
	}
}

func TestClaimConnectionsAreLimited(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := testServerID(t)
	alice := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1"}
	addr := func(i int) netip.Addr { return netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}) }

	// Per address: other accounts from one address share its budget.
	for i := range 5 {
		acct := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: fmt.Sprint(100 + i)}
		if err := h.svc.AdmitClaim(ctx, testServerID(t), acct, addr(1)); err != nil {
			t.Fatalf("claim %d from one address: %v", i+1, err)
		}
	}
	if err := h.svc.AdmitClaim(ctx, id, alice, addr(1)); !errors.Is(err, edgeproto.RefusalTooMany) {
		t.Fatalf("6th claim from one address: %v", err)
	}
	// Per account: one account from many addresses.
	for i := range 5 {
		if err := h.svc.AdmitClaim(ctx, testServerID(t), alice, addr(10+i)); err != nil {
			t.Fatalf("claim %d by one account: %v", i+1, err)
		}
	}
	if err := h.svc.AdmitClaim(ctx, testServerID(t), alice, addr(20)); !errors.Is(err, edgeproto.RefusalTooMany) {
		t.Fatalf("6th claim by one account: %v", err)
	}
	// Per server: many accounts from many addresses guessing one code.
	for i := range 10 {
		acct := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: fmt.Sprint(100 + i)}
		if err := h.svc.AdmitClaim(ctx, id, acct, addr(30+i)); err != nil {
			t.Fatalf("claim %d of one server: %v", i+1, err)
		}
	}
	if err := h.svc.AdmitClaim(ctx, id, edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "200"}, addr(50)); !errors.Is(err, edgeproto.RefusalTooMany) {
		t.Fatalf("11th claim of one server: %v", err)
	}
	h.clock.Advance(time.Minute)
	owned := testServerID(t)
	h.claim(t, owned, "owned", edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "9"})
	if err := h.svc.AdmitClaim(ctx, owned, alice, addr(60)); !errors.Is(err, edgeproto.RefusalClaimed) {
		t.Fatalf("claim connection to an owned server: %v", err)
	}
}

func TestBlockedAccount(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	start := startDevice(t, h)
	b.post(t, "/device/confirm", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	tok, status, msg := pollDevice(t, h, start.DeviceCode)
	if status != http.StatusOK {
		t.Fatalf("poll = %d %s", status, msg)
	}
	ctx := context.Background()
	if err := h.svc.store.BlockAccount(ctx, edgeproto.ProviderGitHub, "1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}

	if status, msg := h.apiCall(t, http.MethodGet, edgeproto.PathServers, tok.Token, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("device token of a blocked account: %d %q", status, msg)
	}
	if resp, _ := b.get(t, "/servers"); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("session of a blocked account: %s, want a redirect to sign in", resp.Status)
	}
	signIn := func() (*http.Response, string) {
		resp, _ := b.get(t, "/signin/github")
		code, state := h.provider.authorize(t, resp.Header.Get("Location"))
		return b.get(t, "/signin/github/callback?"+url.Values{"code": {code}, "state": {state}}.Encode())
	}
	if resp, page := signIn(); resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(page, "account is blocked by this edge&#39;s operator") {
		t.Fatalf("sign-in of a blocked account: %s\n%s", resp.Status, page)
	}

	if err := h.svc.store.UnblockAccount(ctx, edgeproto.ProviderGitHub, "1"); err != nil {
		t.Fatal(err)
	}
	if resp, page := signIn(); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in after unblock: %s\n%s", resp.Status, page)
	}
}

func TestCSRF(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	h.setGitHubUser(2, "other", "other@example.test", true)
	other := h.browser(t)
	other.signIn(t)

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

func TestRemoveServer(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	ctx := context.Background()
	mine, theirs := testServerID(t), testServerID(t)
	owner := sessionAccount(t, h, b)
	h.claim(t, mine, "mine", owner)
	h.claim(t, theirs, "theirs", edgeproto.Account{Provider: "github", Subject: "2"})
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
	if state, err := h.svc.ServerConnected(ctx, mine, "mine", edgeproto.PolicyAccount); err != nil || state != edgeproto.StateUnclaimed {
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

func limitReq(addr string) []netip.Prefix {
	return addrKeys(&http.Request{RemoteAddr: netip.AddrPortFrom(netip.MustParseAddr(addr), 1234).String()})
}

func TestLimiterGroupsIPv6By64(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	l := newAddrLimiter(2, time.Minute, clock.Now)
	if !l.allow(limitReq("2001:db8::1")...) || !l.allow(limitReq("2001:db8::ffff:2")...) {
		t.Fatal("first two requests refused")
	}
	if l.allow(limitReq("2001:db8::3")...) {
		t.Error("third request from the same /64 allowed")
	}
	if !l.allow(limitReq("2001:db8:0:1::1")...) || !l.allow(limitReq("192.0.2.1")...) {
		t.Error("other blocks share the /64's budget")
	}
	clock.Advance(time.Minute)
	if !l.allow(limitReq("2001:db8::4")...) {
		t.Error("budget did not refill")
	}
}

// TestLimiterCountsIPv6Sites spreads requests over the /64s of one /48,
// as anyone holding a routed /48 can: together they get the /48's budget,
// and the rest of the Internet keeps its own.
func TestLimiterCountsIPv6Sites(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	l := newAddrLimiter(2, time.Minute, clock.Now)
	allowed := 0
	for i := range 1000 {
		if l.allow(limitReq(fmt.Sprintf("2001:db8:1:%x::1", i))...) {
			allowed++
		}
	}
	if want := 2 * edgeproto.RateLimitScale(netip.MustParsePrefix("2001:db8:1::/48")); allowed != want {
		t.Errorf("1000 /64s of one /48 got %d requests, want the /48's %d", allowed, want)
	}
	if !l.allow(limitReq("2001:db8:2::1")...) || !l.allow(limitReq("192.0.2.1")...) {
		t.Error("another /48 or an IPv4 address shares the exhausted /48's budget")
	}
}

// TestLimiterFullTableAdmitsNewAddresses fills the limiter with limited
// addresses: a new address is still served, and the table stays bounded.
func TestLimiterFullTableAdmitsNewAddresses(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	l := newAddrLimiter(10, 30*time.Second, clock.Now)
	for i := range maxTracked {
		l.allow(limitReq(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}).String())...)
	}
	if !l.allow(limitReq("198.51.100.7")...) {
		t.Error("a new address was refused because the table is full")
	}
	if n := len(l.buckets); n > maxTracked {
		t.Errorf("limiter tracks %d keys, bound %d", n, maxTracked)
	}
}

// TestGoogleAccountFromAnEarlierVersion meets an account v0.5.2-alpha.3
// signed in with Google, with a browser session and a device token: both
// are refused with the reason and the command that removes the account,
// and the browser can then sign in with GitHub.
func TestGoogleAccountFromAnEarlierVersion(t *testing.T) {
	h := newHarness(t)
	b := signedInBrowser(t, h)
	tok := approvedDevice(t, h, b)
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.Join(h.dataDir, "edge.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test
	if _, err := db.Exec(`UPDATE accounts SET provider = 'google', login = '' WHERE subject = '1'`); err != nil {
		t.Fatal(err)
	}
	const reason = `sign-in provider "google" is not supported: Aether signs in with GitHub only; ` +
		`this edge's operator removes the account with: aether-edge accounts delete google:1`

	if status, msg := h.apiCall(t, http.MethodGet, edgeproto.PathServers, tok.Token, nil, nil); status != http.StatusForbidden || !strings.Contains(msg, reason) {
		t.Errorf("device token of a Google account: %d %q", status, msg)
	}
	if resp, page := b.get(t, "/servers"); resp.StatusCode != http.StatusForbidden || !strings.Contains(page, html.EscapeString(reason)) {
		t.Errorf("session of a Google account: %s\n%s", resp.Status, page)
	}
	if resp, page := b.get(t, "/signin"); resp.StatusCode != http.StatusOK || !strings.Contains(page, "Sign in with GitHub") {
		t.Errorf("sign-in page after the refusal: %s\n%s", resp.Status, page)
	}
}
