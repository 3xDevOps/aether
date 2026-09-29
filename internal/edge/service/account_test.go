package edge

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// accountFixture is a signed-in owner, with a device, who owns one server,
// is a member of another and is invited to a third.
type accountFixture struct {
	b                     *browser
	tok                   edgeproto.DeviceTokenResponse
	owned, member, invite string
}

func newAccountFixture(t *testing.T, h *harness) accountFixture {
	t.Helper()
	ctx := context.Background()
	f := accountFixture{b: signedInBrowser(t, h), owned: testServerID(t), member: testServerID(t), invite: testServerID(t)}
	f.tok = approvedDevice(t, h, f.b)
	me := f.tok.Account.Account
	h.claim(t, f.owned, "mine", me)
	other := edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "2", Login: "other"}
	h.claim(t, f.member, "theirs", other)
	h.claim(t, f.invite, "invited", other)
	if err := h.svc.ReplaceDirectory(ctx, f.member, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: me.Provider, Subject: me.Subject, Role: "collaborator"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ReplaceDirectory(ctx, f.invite, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryInvitation, Provider: edgeproto.ProviderGitHub, Login: me.Login, Role: "viewer",
			ExpiresAt: h.clock.Now().Add(time.Hour)},
	}}); err != nil {
		t.Fatal(err)
	}
	return f
}

// checkDeleted checks what deleting the fixture's account left.
func checkDeleted(t *testing.T, h *harness, f accountFixture) {
	t.Helper()
	ctx := context.Background()
	if status, _ := h.apiCall(t, http.MethodGet, edgeproto.PathServers, f.tok.Token, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("device token after deletion: %d, want 401", status)
	}
	if resp, _ := f.b.get(t, "/servers"); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("session after deletion: %s, want a redirect to sign in", resp.Status)
	}
	srv, err := h.svc.store.Server(ctx, f.owned)
	if err != nil || srv.Owner != nil {
		t.Errorf("owned server after deletion: %+v, %v; want it enrolled without an owner", srv, err)
	}
	me := f.tok.Account.Account
	if len(h.link.deleted) != 1 || !sameAccount(h.link.deleted[0].account, me) ||
		!slices.Equal(h.link.deleted[0].servers, sortedIDs(f.owned, f.member)) {
		t.Fatalf("relay told %+v, want %s deleted on %s and %s", h.link.deleted, me.Subject, f.owned, f.member)
	}
	// A server that is offline is sent the deletion when it next
	// connects: the relay reads it from here.
	dir := relayDirectory{h.svc}
	for _, id := range []string{f.owned, f.member} {
		owed, err := dir.PendingDeletions(ctx, id)
		if err != nil || len(owed) != 1 || owed[0] != (edgeproto.AccountDeleted{Provider: me.Provider, Subject: me.Subject}) {
			t.Errorf("owed to %s: %+v, %v", id, owed, err)
		}
	}
	if owed, err := dir.PendingDeletions(ctx, f.invite); err != nil || len(owed) != 0 {
		t.Errorf("owed to %s, where the account was only invited: %+v, %v", f.invite, owed, err)
	}
}

func sameAccount(a, b edgeproto.Account) bool {
	return a.Provider == b.Provider && a.Subject == b.Subject
}

func sortedIDs(ids ...string) []string {
	slices.Sort(ids)
	return ids
}

func TestAccountPageDeletes(t *testing.T) {
	h := newHarness(t)
	f := newAccountFixture(t, h)

	resp, page := f.b.get(t, "/account")
	for _, want := range []string{f.owned, f.member, html.EscapeString(transferCommand), "sudo aether-server edge claim-code",
		"keeps working until an admin removes it there", "Type <strong>owner</strong> to confirm", f.tok.Account.ID} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, want) {
			t.Errorf("account page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, f.invite) {
		t.Errorf("account page lists %s, where the account is only invited", f.invite)
	}

	// Minutes after signing in, the page asks for a new sign-in and the
	// deletion is refused however it is posted.
	h.clock.Advance(reauthWindow + time.Second)
	if _, page = f.b.get(t, "/account"); strings.Contains(page, `action="/account/delete"`) ||
		!strings.Contains(page, `href="`+testSignin+`/signin/github?next=%2Faccount"`) {
		t.Errorf("stale sign-in: account page offers deletion:\n%s", page)
	}
	if resp, page = f.b.post(t, "/account/delete", url.Values{"confirm": {"owner"}}); resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(page, "deleting an account needs a sign-in from the last 5m0s") {
		t.Fatalf("delete with a stale sign-in: %s\n%s", resp.Status, page)
	}

	f.b.signIn(t, edgeproto.ProviderGitHub)
	for _, typed := range []string{"", "other", "owner@example.test.evil", f.tok.Account.Subject} {
		if resp, page = f.b.post(t, "/account/delete", url.Values{"confirm": {typed}}); resp.StatusCode != http.StatusBadRequest ||
			!strings.Contains(page, "the confirmation does not name this account: type owner") {
			t.Fatalf("delete confirmed with %q: %s\n%s", typed, resp.Status, page)
		}
	}
	if resp, _ = f.b.do(t, http.MethodPost, "/account/delete", url.Values{"confirm": {"owner"}}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete without the CSRF token: %s", resp.Status)
	}
	if len(h.link.deleted) != 0 {
		t.Fatal("a refused deletion reached the relay")
	}

	resp, page = f.b.post(t, "/account/delete", url.Values{"confirm": {" OWNER@example.test "}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "The account is deleted. 1 server(s) you owned now have no owner.") {
		t.Fatalf("delete: %s\n%s", resp.Status, page)
	}
	checkDeleted(t, h, f)
}

func TestAccountAPIDeletes(t *testing.T) {
	h := newHarness(t)
	f := newAccountFixture(t, h)

	var sum edgeproto.AccountSummary
	if status, msg := h.apiCall(t, http.MethodGet, edgeproto.PathAccount, f.tok.Token, nil, &sum); status != http.StatusOK {
		t.Fatalf("account: %d %s", status, msg)
	}
	if sum.Account != f.tok.Account || sum.Confirm != "owner" || len(sum.Owned) != 1 || sum.Owned[0].ID != f.owned ||
		len(sum.Member) != 1 || sum.Member[0].ID != f.member || sum.Member[0].Role != "collaborator" {
		t.Fatalf("account summary %+v", sum)
	}
	if status, _ := h.apiCall(t, http.MethodPost, edgeproto.PathAccountDelete, "", edgeproto.AccountDeleteRequest{Confirm: "owner"}, nil); status != http.StatusUnauthorized {
		t.Fatalf("delete without a device token: %d", status)
	}

	// A device token alone does not delete: the person signs in again.
	h.clock.Advance(reauthWindow + time.Second)
	if status, msg := h.apiCall(t, http.MethodPost, edgeproto.PathAccountDelete, f.tok.Token, edgeproto.AccountDeleteRequest{Confirm: "owner"}, nil); status != http.StatusForbidden ||
		!strings.Contains(msg, "sign in again at "+testSignin+"/signin/github?next=%2Faccount") {
		t.Fatalf("delete with a stale sign-in: %d %q", status, msg)
	}
	f.b.signIn(t, edgeproto.ProviderGitHub)
	if status, msg := h.apiCall(t, http.MethodPost, edgeproto.PathAccountDelete, f.tok.Token, edgeproto.AccountDeleteRequest{Confirm: "someone"}, nil); status != http.StatusBadRequest {
		t.Fatalf("delete with a wrong confirmation: %d %q", status, msg)
	}
	if status, msg := h.apiCall(t, http.MethodPost, edgeproto.PathAccountDelete, f.tok.Token, edgeproto.AccountDeleteRequest{Confirm: "owner"}, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %q", status, msg)
	}
	checkDeleted(t, h, f)
}

func TestConfirmationOfAnAccountWithoutLoginOrEmail(t *testing.T) {
	a := edgeproto.AccountInfo{ID: edgeproto.NewAccountID(), Account: edgeproto.Account{Provider: edgeproto.ProviderGoogle, Subject: "g-1"}}
	if confirmText(a) != a.ID || !confirms(a, a.ID) || confirms(a, "g-1") || confirms(a, "") {
		t.Errorf("an account with neither login nor email is confirmed by its id %s, and only by it", a.ID)
	}
}
