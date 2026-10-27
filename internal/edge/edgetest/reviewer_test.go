package edgetest

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	edge "github.com/3xDevOps/Aether/internal/edge/service"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
)

const (
	reviewerLogin    = "dir-review"
	reviewerPassword = "correct horse battery staple"
)

var reviewerLink = regexp.MustCompile(`href="(` + regexp.QuoteMeta(edge.PathReviewerSignin) + `\?[^"]+)"`)

// reviewerEdge is a harness whose edge has a reviewer account for a
// server that does not exist yet, and that server's directory, so the
// server it starts there is the one the account file names.
func reviewerEdge(t *testing.T) (*harness, string, string) {
	t.Helper()
	h := newHarness(t)
	dir := t.TempDir()
	key, err := sshd.LoadOrCreateHostKey(filepath.Join(dir, "ssh", "host_ed25519_key"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := edge.HashReviewerPassword(reviewerPassword)
	if err != nil {
		t.Fatal(err)
	}
	id := edgeproto.ServerID(key.PublicKey())
	h.stopEdge()
	h.reviewer = &edge.ReviewerAccount{Login: reviewerLogin, PasswordHash: hash, ServerID: id}
	h.startEdge()
	return h, dir, id
}

// reviewerServer starts a connector server in dir, which alice claims and
// bob joins as a collaborator. reviewer is its edge-reviewer option.
func (h *harness) reviewerServer(dir string, policy edgeproto.AccessPolicy, client *connectorClient, reviewer bool) (*serverNode, domain.MemberID, domain.MemberID) {
	h.t.Helper()
	s := &serverNode{h: h, dir: dir, policy: policy, client: client, reviewer: reviewer, addr: "127.0.0.1:0"}
	s.start()
	h.t.Cleanup(func() { s.stop() })
	clients := h.login(alice, bob)
	h.claimServer(clients[0], s)
	h.join(h.control(clients[0], s), clients[1], s, string(domain.RoleCollaborator))
	return s, s.memberOf(h.t, alice).ID, s.memberOf(h.t, bob).ID
}

// authRequest is a universal authorization request of client, and its
// PKCE verifier.
func authRequest(h *harness, client *connectorClient) (string, string) {
	verifier := oauth2.GenerateVerifier()
	base := h.relayURL + edgeproto.PathUniversalMCP
	q := url.Values{"response_type": {"code"}, "client_id": {client.id}, "redirect_uri": {client.redirect},
		"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "code_challenge_method": {"S256"},
		"resource": {base}, "state": {"s"}}
	return base + "/authorize?" + q.Encode(), verifier
}

// reviewerSignIn follows authURL in b, signed in nowhere, to the sign-in
// page, takes its reviewer link and posts login and password on the
// reviewer page.
func reviewerSignIn(t *testing.T, b *browser, authURL, login, password string) (*http.Response, string) {
	t.Helper()
	resp, page, at := b.visit(t, authURL)
	m := reviewerLink.FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("sign-in page at %s: %s, no reviewer link\n%s", at, resp.Status, page)
	}
	resp, page = b.get(t, b.h.signinURL+html.UnescapeString(m[1]))
	form := url.Values{}
	for _, f := range hiddenField.FindAllStringSubmatch(page, -1) {
		form.Add(html.UnescapeString(f[1]), html.UnescapeString(f[2]))
	}
	if resp.StatusCode != http.StatusOK || form.Get("client_id") == "" {
		t.Fatalf("reviewer page: %s\n%s", resp.Status, page)
	}
	form.Set("login", login)
	form.Set("password", password)
	return b.send(t, http.MethodPost, b.h.signinURL+edge.PathReviewerSignin, form)
}

// TestReviewerSignInOff checks that an edge without a reviewer account
// file serves no reviewer page and offers no reviewer link.
func TestReviewerSignInOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b, err := h.newBrowser()
	if err != nil {
		t.Fatal(err)
	}
	authURL, _ := authRequest(h, newConnectorClient(t))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if resp, page := b.send(t, method, h.signinURL+edge.PathReviewerSignin+"?client_id=x", url.Values{"login": {reviewerLogin}}); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s: %s, want 404\n%s", method, edge.PathReviewerSignin, resp.Status, page)
		}
	}
	if resp, page, _ := b.visit(t, authURL); resp.StatusCode != http.StatusOK || strings.Contains(page, edge.PathReviewerSignin) {
		t.Fatalf("sign-in page: %s, want no reviewer link\n%s", resp.Status, page)
	}
}

// TestReviewerSignIn has an app directory's reviewer connect through the
// edge's reviewer account, bound to bob, a collaborator, on the one
// server the account file names. The reviewer sees only that server, and
// gets no edge session: no device sign-in, server claim or invitation.
// The consent page names bob, and the connector works until the account
// is bound to an admin or unbound. Binding needs an admin and refuses an
// admin member.
func TestReviewerSignIn(t *testing.T) {
	t.Parallel()
	h, dir, id := reviewerEdge(t)
	client := newConnectorClient(t)
	s, aliceID, bobID := h.reviewerServer(dir, edgeproto.PolicyAccount, client, true)
	other := h.newConnectorServer(edgeproto.PolicyAccount, client, sshd.Services{})
	h.claimServer(h.login(alice)[0], other)
	if s.id != id {
		t.Fatalf("server id %s, want the account file's %s", s.id, id)
	}

	link := func(as domain.MemberID, member domain.MemberID) error {
		return s.local(t, as, protocol.MethodMemberReviewerLink, protocol.MemberReviewerLinkParams{MemberID: string(member), Login: reviewerLogin}, nil)
	}
	for _, tt := range []struct {
		what       string
		as, member domain.MemberID
		want       string
	}{
		{"bob binding himself", bobID, bobID, "admins only"},
		{"alice binding herself, an admin", aliceID, aliceID, "is an admin"},
	} {
		if err := link(tt.as, tt.member); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%s: %v, want %q", tt.what, err, tt.want)
		}
	}

	b, err := h.newBrowser()
	if err != nil {
		t.Fatal(err)
	}
	authURL, _ := authRequest(h, client)
	resp, page := reviewerSignIn(t, b, authURL, reviewerLogin, reviewerPassword)
	action, forms := chooseForms(t, page)
	if resp.StatusCode != http.StatusOK || len(forms) != 1 || forms[s.id] == nil || strings.Contains(page, other.id) {
		t.Fatalf("reviewer's choose page: %s, want a form for %s alone and no %s\n%s", resp.Status, s.id, other.id, page)
	}
	resp, page = b.send(t, http.MethodPost, action, forms[s.id])
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, "reviewer account "+reviewerLogin+" is bound to no member") {
		t.Fatalf("consent before binding: %s\n%s", resp.Status, page)
	}

	for _, path := range []string{"/servers", "/device", "/account", edgeproto.PathMCPChoose} {
		if got, _ := b.get(t, h.signinURL+path); got.StatusCode != http.StatusSeeOther || !strings.HasPrefix(got.Header.Get("Location"), "/signin?") {
			t.Fatalf("the reviewer's browser opening %s: %s %s, want sent to sign in", path, got.Status, got.Header.Get("Location"))
		}
	}

	if err = link(aliceID, bobID); err != nil {
		t.Fatal(err)
	}
	if err = link(aliceID, bobID); err == nil || !strings.Contains(err.Error(), "is bound to member "+string(bobID)+" already") {
		t.Fatalf("binding twice: %v", err)
	}
	authURL, verifier := authRequest(h, client)
	_, page = reviewerSignIn(t, b, authURL, reviewerLogin, reviewerPassword)
	action, forms = chooseForms(t, page)
	resp, page = b.send(t, http.MethodPost, action, forms[s.id])
	m := requestField.FindStringSubmatch(page)
	bobName := s.memberOf(t, bob).DisplayName
	if resp.StatusCode != http.StatusOK || m == nil || strings.Contains(page, `name="code"`) || !strings.Contains(page, "</strong> as <strong>"+bobName+"</strong>") {
		t.Fatalf("consent page: %s, want bob named and no code asked\n%s", resp.Status, page)
	}
	resp, page = b.send(t, http.MethodPost, action, url.Values{"request": {m[1]}, edgeproto.ParamServer: {s.id}, "scope": {"write"}})
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc.Query().Get("code"), s.id+".") {
		t.Fatalf("allow: %s %v %v\n%s", resp.Status, loc, err, page)
	}
	base := h.relayURL + edgeproto.PathUniversalMCP
	resp, body := mcpRequest(t, http.MethodPost, base+"/token", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		strings.NewReader(url.Values{"grant_type": {"authorization_code"}, "client_id": {client.id}, "code": {loc.Query().Get("code")},
			"code_verifier": {verifier}, "redirect_uri": {client.redirect}, "resource": {base}}.Encode()))
	var tok tokenAnswer
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &tok) != nil {
		t.Fatalf("token exchange: %s %s", resp.Status, body)
	}
	toolsList := func() (*http.Response, string) {
		header := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}, "Authorization": {"Bearer " + tok.AccessToken}}
		return mcpRequest(t, http.MethodPost, base, header, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	}
	if resp, body = toolsList(); resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list: %s %s", resp.Status, body)
	}

	role := func(r domain.Role) {
		t.Helper()
		if rerr := s.local(t, aliceID, protocol.MethodMemberRole, protocol.MemberRoleParams{MemberID: string(bobID), Role: string(r)}, nil); rerr != nil {
			t.Fatal(rerr)
		}
	}
	role(domain.RoleAdmin)
	if resp, body = toolsList(); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "a reviewer may act only as a viewer or collaborator") {
		t.Fatalf("tools/list as an admin: %s %s, want 401", resp.Status, body)
	}
	role(domain.RoleCollaborator)
	if resp, body = toolsList(); resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list as a collaborator again: %s %s", resp.Status, body)
	}
	if err = s.local(t, aliceID, protocol.MethodMemberIdentityRemove, protocol.MemberIdentityRemoveParams{
		MemberID: string(bobID), Provider: edgeproto.ProviderReviewer, Subject: reviewerLogin}, nil); err != nil {
		t.Fatal(err)
	}
	if resp, body = toolsList(); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "is no longer bound to member "+string(bobID)) {
		t.Fatalf("tools/list once unbound: %s %s, want 401", resp.Status, body)
	}
}

// TestReviewerLockout gets the reviewer password wrong until the sign-in
// locks: then even the right password is refused until the lock ends.
func TestReviewerLockout(t *testing.T) {
	t.Parallel()
	h, _, _ := reviewerEdge(t)
	client := newConnectorClient(t)
	b, err := h.newBrowser()
	if err != nil {
		t.Fatal(err)
	}
	authURL, _ := authRequest(h, client)
	for i := range 10 {
		login, password := reviewerLogin, "wrong"
		if i%2 == 1 {
			login, password = "someone-else", reviewerPassword
		}
		if resp, page := reviewerSignIn(t, b, authURL, login, password); resp.StatusCode != http.StatusForbidden ||
			!strings.Contains(page, "wrong reviewer login or password") {
			t.Fatalf("attempt %d with %s: %s, want 403\n%s", i+1, login, resp.Status, page)
		}
	}
	resp, page := reviewerSignIn(t, b, authURL, reviewerLogin, reviewerPassword)
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(page, "the reviewer sign-in is locked after 10 wrong logins or passwords in a row") {
		t.Fatalf("the right password while locked: %s, want 429\n%s", resp.Status, page)
	}
	h.advance(15 * time.Minute)
	if resp, page = reviewerSignIn(t, b, authURL, reviewerLogin, reviewerPassword); resp.StatusCode != http.StatusOK ||
		!strings.Contains(page, "no account claimed this server at this edge") {
		t.Fatalf("the right password after the lock: %s\n%s", resp.Status, page)
	}
}

// TestReviewerNeedsServerOptIn has the reviewer sign in to servers that
// do not let it act as a member: one with edge-reviewer off, whose
// binding was written to its database by hand, and one under
// approved-devices, whose consent page asks for a code as for anyone.
func TestReviewerNeedsServerOptIn(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		policy   edgeproto.AccessPolicy
		reviewer bool
	}{
		{"edge-reviewer off", edgeproto.PolicyAccount, false},
		{"approved-devices", edgeproto.PolicyApprovedDevices, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, dir, _ := reviewerEdge(t)
			client := newConnectorClient(t)
			s, aliceID, bobID := h.reviewerServer(dir, tt.policy, client, tt.reviewer)
			err := s.local(t, aliceID, protocol.MethodMemberReviewerLink, protocol.MemberReviewerLinkParams{MemberID: string(bobID), Login: reviewerLogin}, nil)
			switch {
			case !tt.reviewer && (err == nil || !strings.Contains(err.Error(), "edge-reviewer is off")):
				t.Fatalf("binding with edge-reviewer off: %v", err)
			case !tt.reviewer:
				if err = s.db.BindIdentity(t.Context(), &domain.Identity{Member: bobID, Provider: edgeproto.ProviderReviewer,
					Subject: reviewerLogin, Login: reviewerLogin}); err != nil {
					t.Fatal(err)
				}
			case err != nil:
				t.Fatal(err)
			}
			b, err := h.newBrowser()
			if err != nil {
				t.Fatal(err)
			}
			authURL, _ := authRequest(h, client)
			_, page := reviewerSignIn(t, b, authURL, reviewerLogin, reviewerPassword)
			action, forms := chooseForms(t, page)
			if forms[s.id] == nil {
				t.Fatalf("no form for %s:\n%s", s.id, page)
			}
			resp, page := b.send(t, http.MethodPost, action, forms[s.id])
			if tt.reviewer {
				if resp.StatusCode != http.StatusOK || !strings.Contains(page, `name="code"`) || strings.Contains(page, s.memberOf(t, bob).DisplayName) {
					t.Fatalf("consent under approved-devices: %s, want a code asked and bob not named\n%s", resp.Status, page)
				}
				return
			}
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, "does not take the edge&#39;s reviewer account: edge-reviewer is off") {
				t.Fatalf("consent with edge-reviewer off: %s\n%s", resp.Status, page)
			}
		})
	}
}

// TestReviewerOlderServer runs the reviewer's server on a control channel
// that negotiated protocol 4, as a server from before VersionReviewer,
// which refuses a reviewer assertion as malformed: the edge offers it as
// too old and signs it no assertion.
func TestReviewerOlderServer(t *testing.T) {
	t.Parallel()
	h, dir, _ := reviewerEdge(t)
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
		if ch, ok := e.msg.(edgeproto.Challenge); ok {
			ch.Version = edgeproto.VersionConnectorSignIn
			return ch, true
		}
		return e.msg, true
	})
	client := newConnectorClient(t)
	s, _, _ := h.reviewerServer(dir, edgeproto.PolicyAccount, client, true)
	b, err := h.newBrowser()
	if err != nil {
		t.Fatal(err)
	}
	authURL, _ := authRequest(h, client)
	resp, page := reviewerSignIn(t, b, authURL, reviewerLogin, reviewerPassword)
	if _, forms := chooseForms(t, page); resp.StatusCode != http.StatusOK || forms[s.id] != nil || !strings.Contains(page, "its aether-server is too old") {
		t.Fatalf("choose page for a protocol 4 server: %s\n%s", resp.Status, page)
	}
}
