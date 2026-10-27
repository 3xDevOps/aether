package edgetest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
)

var chooseLink = regexp.MustCompile(`<a href="(/connectors/choose\?[^"]+)">([^<]+)</a>`)

// visit is a signed-in browser following an authorization request from
// target across the edge's two origins until a page answers.
func (b *browser) visit(t *testing.T, target string) (*http.Response, string, string) {
	t.Helper()
	for range 5 {
		resp, page := b.get(t, target)
		if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
			return resp, page, target
		}
		next, err := resp.Location()
		if err != nil {
			t.Fatal(err)
		}
		if next.Path == "/callback" {
			return resp, page, target
		}
		target = next.String()
	}
	t.Fatalf("too many redirects, last %s", target)
	return nil, "", ""
}

func (b *browser) get(t *testing.T, target string) (*http.Response, string) {
	t.Helper()
	return b.send(t, http.MethodGet, target, nil)
}

func (b *browser) send(t *testing.T, method, target string, form url.Values) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
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

// universalConsent is a member's browser authorizing a connector on the
// universal URL: the edge sends it to choose a server (pick, when set,
// names the one to pick from the list), then to that server's consent
// page. Under account the page names the member and asks no code; under
// approved-devices it asks the code. It returns the authorization result
// and the consent page's URL and form.
func universalConsent(t *testing.T, b *browser, authURL, pick, member, code string, policy edgeproto.AccessPolicy, scopes ...string) (*auth.AuthorizationResult, string, url.Values) {
	t.Helper()
	resp, page, at := b.visit(t, authURL)
	if pick != "" {
		var link string
		for _, m := range chooseLink.FindAllStringSubmatch(page, -1) {
			if strings.Contains(m[1], "server="+pick) {
				link = m[1]
			}
		}
		if resp.StatusCode != http.StatusOK || link == "" {
			t.Fatalf("choose page at %s: %s, no link to %s\n%s", at, resp.Status, pick, page)
		}
		resp, page, at = b.visit(t, b.h.signinURL+strings.ReplaceAll(link, "&amp;", "&"))
	}
	m := requestField.FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil || resp.Header.Get("Content-Security-Policy") != connectorCSP {
		t.Fatalf("consent page at %s: %s\n%s", at, resp.Status, page)
	}
	form := url.Values{"request": {m[1]}, "scope": scopes}
	switch asked := strings.Contains(page, `name="code"`); {
	case policy == edgeproto.PolicyAccount && (asked || !strings.Contains(page, "</strong> as <strong>"+member+"</strong>")):
		t.Fatalf("consent page under account asks a code or does not name %s:\n%s", member, page)
	case policy == edgeproto.PolicyApprovedDevices && !asked:
		t.Fatalf("consent page under approved-devices asks no code:\n%s", page)
	case policy == edgeproto.PolicyApprovedDevices:
		form.Set("code", code)
	}
	resp, page = b.send(t, http.MethodPost, at, form)
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusSeeOther || loc.Path != "/callback" {
		t.Fatalf("allow: %s %v %v\n%s", resp.Status, loc, err, page)
	}
	q := loc.Query()
	return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, at, form
}

// TestUniversalConnectorThroughTheEdge adds connectors at the one URL
// every server shares, <relay origin>/mcp, the way ChatGPT does, with
// go-sdk's MCP and OAuth client, through a real edge to two servers
// serving the real connector handler, under both policies. The edge
// serves the metadata and a 401 naming it; the browser signed in at the
// edge chooses a server (at once when the account can reach only one)
// and reaches its consent page, which under account names the member and
// asks no code, and under approved-devices asks the code. The token
// exchange, tools/list, a refresh and connector.revoke work through the
// edge, routed by the tokens' server prefix; a token presented with
// another server's prefix, a replayed consent form or assertion, and a
// forged assertion are refused. Under account, unbinding the member's
// GitHub account or tightening the policy cuts off a connector authorized
// without a code, and such a connector is refused what needs an approved
// device.
func TestUniversalConnectorThroughTheEdge(t *testing.T) {
	t.Parallel()
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			client := newConnectorClient(t)
			s1 := h.newConnectorServer(policy, client, sshd.Services{})
			s2 := h.newConnectorServer(policy, client, sshd.Services{})
			clients := h.login(alice, bob)
			al, bo := clients[0], clients[1]
			h.claimServer(al, s1)
			h.claimServer(al, s2)
			h.join(h.control(al, s1), bo, s1, string(domain.RoleCollaborator))
			eventually(t, "the edge to list bob as a member of server 1", func() error {
				acc, err := h.edgeStore().ServerAccess(t.Context(), s1.id, bob.account())
				if err != nil {
					return err
				}
				for _, e := range acc.Entries {
					if e.Kind == edgeproto.EntryMember && e.Matches(bob.account(), h.now()) {
						return nil
					}
				}
				return fmt.Errorf("entries %+v", acc.Entries)
			})
			base := h.relayURL + edgeproto.PathUniversalMCP
			bearer := func(token string) (*http.Response, string) {
				header := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}
				if token != "" {
					header.Set("Authorization", "Bearer "+token)
				}
				return mcpRequest(t, http.MethodPost, base, header, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			}

			resp, body := bearer("")
			metadata := h.relayURL + edgeproto.PathUniversalResourceMetadata
			if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer resource_metadata="`+metadata+`"` {
				t.Fatalf("tokenless call: %s %v %q, want 401 naming %s", resp.Status, resp.Header, body, metadata)
			}
			var prm, asm map[string]any
			if resp, body = mcpRequest(t, http.MethodGet, metadata, nil, nil); resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &prm) != nil ||
				prm["resource"] != base || fmt.Sprint(prm["authorization_servers"]) != "["+base+"]" {
				t.Fatalf("protected resource metadata: %s %s", resp.Status, body)
			}
			if resp, body = mcpRequest(t, http.MethodGet, h.relayURL+edgeproto.PathUniversalAuthServerMetadata, nil, nil); resp.StatusCode != http.StatusOK ||
				json.Unmarshal([]byte(body), &asm) != nil || asm["issuer"] != base || asm["authorization_endpoint"] != base+"/authorize" ||
				asm["token_endpoint"] != base+"/token" || asm["client_id_metadata_document_supported"] != true || asm["registration_endpoint"] != nil ||
				fmt.Sprint(asm["code_challenge_methods_supported"]) != "[S256]" || asm["authorization_response_iss_parameter_supported"] != true {
				t.Fatalf("authorization server metadata: %s %s", resp.Status, body)
			}
			if resp, body = mcpRequest(t, http.MethodGet, h.relayURL+edgeproto.PathOpenAIChallenge, nil, nil); resp.StatusCode != http.StatusNotFound {
				t.Fatalf("domain verification without a token: %s %s, want 404", resp.Status, body)
			}

			bctl := h.control(bo, s1)
			code := call[protocol.ConnectorCodeResult](t, bctl, protocol.MethodConnectorCode, struct{}{})
			if code.URL != base {
				t.Fatalf("connector.code url = %s, want %s", code.URL, base)
			}
			bobBrowser, err := h.signIn(bob)
			if err != nil {
				t.Fatal(err)
			}
			bobName := s1.memberOf(t, bob).DisplayName
			var token *oauth2.Token
			var consentURL string
			var consentForm url.Values
			handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
				ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: client.id},
				RedirectURL:                    client.redirect,
				AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
					var res *auth.AuthorizationResult
					res, consentURL, consentForm = universalConsent(t, bobBrowser, args.URL, "", bobName, code.Code, policy, "write")
					return res, nil
				},
				RequestRefreshToken: true,
				NewTokenSource: func(_ context.Context, _ *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
					token = tok
					return oauth2.StaticTokenSource(tok), nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			session := connectMCP(t, &mcp.StreamableClientTransport{Endpoint: base, OAuthHandler: handler})
			if !strings.HasPrefix(token.AccessToken, s1.id+".") || !strings.HasPrefix(token.RefreshToken, s1.id+".") {
				t.Fatalf("tokens %q, %q do not start with %s.", token.AccessToken, token.RefreshToken, s1.id)
			}
			var names []string
			for tool, err := range session.Tools(t.Context(), nil) {
				if err != nil {
					t.Fatal(err)
				}
				names = append(names, tool.Name)
			}
			if len(names) != 22 {
				t.Fatalf("tools with read and write = %v, want 22", names)
			}
			t.Logf("tools/list on %s as %s: %d tools: %s", base, bobName, len(names), strings.Join(names, " "))
			list := call[protocol.ConnectorListResult](t, bctl, protocol.MethodConnectorList, struct{}{})
			if len(list.Connectors) != 1 || list.Connectors[0].LastUsedAt == "" {
				t.Fatalf("connector.list = %+v, want one used connector", list)
			}

			for _, tt := range []struct {
				what, method, target string
				form                 url.Values
				want                 string
			}{
				{"the consent form posted again", http.MethodPost, consentURL, consentForm, "the edge&#39;s sign-in for it was already used"},
				{"the spent assertion again", http.MethodGet, consentURL, nil, "the edge&#39;s sign-in for it was already used"},
				{"an assertion signed by another key", http.MethodGet, forgeAssertion(t, consentURL, nil), nil,
					"grant signature is not from the pinned edge key"},
				{"an edge-signed assertion for another request", http.MethodGet, forgeAssertion(t, consentURL, h.edgeKey()), nil,
					"edge assertion is for another edge, server or authorization request"},
			} {
				r, page := bobBrowser.send(t, tt.method, tt.target, tt.form)
				if policy == edgeproto.PolicyAccount && (r.StatusCode != http.StatusBadRequest || !strings.Contains(page, tt.want)) {
					t.Fatalf("%s: %s, want 400 %q\n%s", tt.what, r.Status, tt.want, page)
				}
			}

			tokenForm := func(refresh string) url.Values {
				return url.Values{"grant_type": {"refresh_token"}, "client_id": {client.id}, "refresh_token": {refresh}, "resource": {base}}
			}
			form := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
			resp, body = mcpRequest(t, http.MethodPost, base+"/token", form, strings.NewReader(tokenForm(token.RefreshToken).Encode()))
			var rotated tokenAnswer
			if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &rotated) != nil || !strings.HasPrefix(rotated.RefreshToken, s1.id+".") ||
				rotated.RefreshToken == token.RefreshToken || rotated.AccessToken == token.AccessToken {
				t.Fatalf("refresh: %s %s, want both tokens rotated", resp.Status, body)
			}
			if resp, body = bearer(token.AccessToken); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("the access token the refresh replaced: %s %s", resp.Status, body)
			}
			other := s2.id + strings.TrimPrefix(rotated.AccessToken, s1.id)
			if resp, body = bearer(other); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "no connector holds this access token") {
				t.Fatalf("server 1's token with server 2's prefix: %s %s", resp.Status, body)
			}
			resp, body = mcpRequest(t, http.MethodPost, base+"/token", form,
				strings.NewReader(tokenForm(s2.id+strings.TrimPrefix(rotated.RefreshToken, s1.id)).Encode()))
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "no connector holds this refresh token") {
				t.Fatalf("server 1's refresh token with server 2's prefix: %s %s", resp.Status, body)
			}
			live := connectMCP(t, &mcp.StreamableClientTransport{Endpoint: base,
				HTTPClient: &http.Client{Transport: &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: rotated.AccessToken})}}})
			callTool(t, live, "aether_list_workspaces", nil)

			alBrowser, err := h.signIn(alice)
			if err != nil {
				t.Fatal(err)
			}
			acode := call[protocol.ConnectorCodeResult](t, h.control(al, s2), protocol.MethodConnectorCode, struct{}{})
			verifier := oauth2.GenerateVerifier()
			authURL := base + "/authorize?" + url.Values{"response_type": {"code"}, "client_id": {client.id}, "redirect_uri": {client.redirect},
				"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "code_challenge_method": {"S256"},
				"resource": {base}, "state": {"s"}}.Encode()
			res, _, _ := universalConsent(t, alBrowser, authURL, s2.id, s2.memberOf(t, alice).DisplayName, acode.Code, policy)
			if !strings.HasPrefix(res.Code, s2.id+".") || res.Iss != base || res.State != "s" {
				t.Fatalf("alice's authorization on server 2 = %+v, want a code for %s, iss %s", res, s2.id, base)
			}
			resp, body = mcpRequest(t, http.MethodPost, base+"/token", form, strings.NewReader(url.Values{"grant_type": {"authorization_code"},
				"client_id": {client.id}, "code": {res.Code}, "code_verifier": {verifier}, "redirect_uri": {client.redirect}, "resource": {base}}.Encode()))
			var alices tokenAnswer
			if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &alices) != nil || !strings.HasPrefix(alices.AccessToken, s2.id+".") {
				t.Fatalf("alice's token exchange on server 2: %s %s", resp.Status, body)
			}
			if resp, body = bearer(alices.AccessToken); resp.StatusCode != http.StatusOK {
				t.Fatalf("alice's connector on server 2: %s %s", resp.Status, body)
			}

			if policy == edgeproto.PolicyAccount {
				conns, err := s2.db.ListConnectors(t.Context(), s2.memberOf(t, alice).ID, time.Now())
				if err != nil || len(conns) != 1 || conns[0].Subject != alice.account().Subject || conns[0].Device != "" {
					t.Fatalf("alice's connectors on server 2 = %+v %v, want one bound to her GitHub account and no device", conns, err)
				}
				grant := sshd.ConnectorGrant{Member: conns[0].Member, Provider: conns[0].Provider, Subject: conns[0].Subject}
				_, perr := s2.ssh.ConnectorLocal(grant, conns[0].ID, "Test client", conns[0].Scopes).Call(t.Context(), protocol.MethodMemberInvite, json.RawMessage(`{}`))
				if want := "member.invite: Test client connector " + conns[0].ID +
					" cannot call this method; no connector can, so use the Aether app or the aether CLI"; perr == nil || perr.Message != want {
					t.Fatalf("member.invite through alice's connector = %v, want %q", perr, want)
				}
				bobID := s1.memberOf(t, bob).ID
				call[protocol.MemberIdentityRemoveResult](t, h.control(al, s1), protocol.MethodMemberIdentityRemove, protocol.MemberIdentityRemoveParams{
					MemberID: string(bobID), Provider: edgeproto.ProviderGitHub, Subject: bob.account().Subject})
				if resp, body = bearer(rotated.AccessToken); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "github account 1002, which authorized it, is no longer bound to member "+string(bobID)) {
					t.Fatalf("bob's connector once his GitHub account is unbound: %s %s", resp.Status, body)
				}
				s1.restart(edgeproto.PolicyAccount)
				eventually(t, "server 1 to refuse, after restarting, the consent form it issued before", func() error {
					if r, page := bobBrowser.send(t, http.MethodPost, consentURL, consentForm); r.StatusCode != http.StatusBadRequest ||
						!strings.Contains(page, "before this server last started") {
						return fmt.Errorf("%s %s", r.Status, page)
					}
					return nil
				})
				s2.restart(edgeproto.PolicyApprovedDevices)
				eventually(t, "server 2 to refuse the connector authorized without a code", func() error {
					if r, b := bearer(alices.AccessToken); r.StatusCode != http.StatusUnauthorized ||
						!strings.Contains(b, "which edge-access approved-devices does not admit; connect again with a code from aether mcp") {
						return fmt.Errorf("%s %s", r.Status, b)
					}
					return nil
				})
				return
			}
			revoked := call[protocol.ConnectorRevokeResult](t, bctl, protocol.MethodConnectorRevoke, protocol.ConnectorRevokeParams{ConnectorID: list.Connectors[0].ID})
			if revoked.Connector.ID != list.Connectors[0].ID {
				t.Fatalf("connector.revoke = %+v", revoked)
			}
			if resp, body = bearer(rotated.AccessToken); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "no connector holds this access token") {
				t.Fatalf("a revoked connector's call: %s %s", resp.Status, body)
			}
		})
	}
}

// forgeAssertion returns consentURL with its assertion replaced by one
// for a fresh nonce: signed by key, or by a key of its own when key is
// nil. One signed by the edge's key names another challenge.
func forgeAssertion(t *testing.T, consentURL string, key ed25519.PrivateKey) string {
	t.Helper()
	u, err := url.Parse(consentURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	payload, _, _ := strings.Cut(q.Get(edgeproto.ParamAssertion), ".")
	var a edgeproto.ConnectorAssertion
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	a.Nonce = edgeproto.NewConnID()
	if key == nil {
		_, key, _ = ed25519.GenerateKey(rand.Reader)
	} else {
		a.Challenge = oauth2.S256ChallengeFromVerifier(oauth2.GenerateVerifier())
	}
	signed, err := edgeproto.SignConnectorAssertion(key, a)
	if err != nil {
		t.Fatal(err)
	}
	q.Set(edgeproto.ParamAssertion, signed)
	u.RawQuery = q.Encode()
	return u.String()
}
