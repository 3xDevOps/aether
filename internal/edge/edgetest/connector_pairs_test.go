package edgetest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/sshd"
)

// A server enrolled at relay origin A of an edge that also serves B takes
// connectors at both, at its own URL and at the universal one: each
// metadata document, issuer and token names the address the client used,
// and a token issued at one address is refused at the other, even with a
// client's own Aether-Edge-Relay-Origin, which the edge replaces. A
// connector authorized before the edge served B keeps working at A.
// Tokens survive a server restart, and an edge restart that drops B and
// one that adds it back: a token issued at B works at B again, and never
// at A.
func TestConnectorAtEveryRelayOrigin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newConnectorClient(t)
	policy := edgeproto.PolicyApprovedDevices
	s := h.newConnectorServer(policy, client, sshd.Services{})
	al := h.login(alice)[0]
	h.claimServer(al, s)
	relayA := h.relayURL
	baseA := relayA + edgeproto.MCPPath(s.id)
	before := connectAt(t, h, client, al, s, baseA)

	h.stopEdge()
	h.pairB = true
	h.startEdge()
	relayB := h.relayB
	baseB := relayB + edgeproto.MCPPath(s.id)
	backOnline(t, h, s)

	for _, base := range []string{baseA, baseB} {
		var prm, asm map[string]any
		resp, body := mcpRequest(t, http.MethodGet, strings.Replace(base, "/mcp/", "/.well-known/oauth-protected-resource/mcp/", 1), nil, nil)
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &prm) != nil || prm["resource"] != base {
			t.Fatalf("protected resource metadata for %s: %s %s", base, resp.Status, body)
		}
		resp, body = mcpRequest(t, http.MethodGet, strings.Replace(base, "/mcp/", "/.well-known/oauth-authorization-server/mcp/", 1), nil, nil)
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &asm) != nil || asm["issuer"] != base || asm["token_endpoint"] != base+"/token" {
			t.Fatalf("authorization server metadata for %s: %s %s", base, resp.Status, body)
		}
		resp, _ = toolsList(t, base, "", nil)
		if want := `resource_metadata="` + strings.TrimSuffix(base, edgeproto.MCPPath(s.id)) + "/.well-known/oauth-protected-resource" + edgeproto.MCPPath(s.id) + `"`; resp.StatusCode != http.StatusUnauthorized ||
			!strings.Contains(resp.Header.Get("WWW-Authenticate"), want) {
			t.Fatalf("tokenless call at %s: %s %v, want 401 naming %s", base, resp.Status, resp.Header, want)
		}
	}

	atB := connectAt(t, h, client, al, s, baseB)
	universalB := universalAt(t, h, client, al, s, policy, h.signinB, h.relayB)
	mustCall(t, baseA, before.AccessToken)
	list := call[protocol.ConnectorListResult](t, h.control(al, s), protocol.MethodConnectorList, struct{}{})
	if want := []string{relayA + edgeproto.PathUniversalMCP, relayB + edgeproto.PathUniversalMCP}; !slices.Equal(list.UniversalURLs, want) ||
		list.UniversalURL != want[0] {
		t.Errorf("connector.list universal URLs %v (%s), want %v", list.UniversalURLs, list.UniversalURL, want)
	}

	for _, tc := range []struct {
		name, base, token, issued string
		forged                    string
	}{
		{"A's at B", baseB, before.AccessToken, baseA, ""},
		{"B's at A", baseA, atB.AccessToken, baseB, ""},
		{"A's at B naming A", baseB, before.AccessToken, baseA, relayA},
		{"B's at A naming B", baseA, atB.AccessToken, baseB, relayB},
		{"universal B's at universal A", relayA + edgeproto.PathUniversalMCP, universalB.AccessToken, relayB + edgeproto.PathUniversalMCP, ""},
	} {
		header := http.Header{}
		if tc.forged != "" {
			header.Set(edgeproto.HeaderRelayOrigin, tc.forged)
		}
		resp, body := toolsList(t, tc.base, tc.token, header)
		if want := "the access token is for " + tc.issued + ", not " + tc.base; resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, want) {
			t.Errorf("%s: %s %q, want 401 %q", tc.name, resp.Status, body, want)
		}
	}

	code, verifier := authorizationCode(t, h, client, al, s, baseA)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {client.id}, "code": {code}, "code_verifier": {verifier},
		"redirect_uri": {client.redirect}, "resource": {baseB}}
	_, body := mcpRequest(t, http.MethodPost, baseB+"/token", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, strings.NewReader(form.Encode()))
	if want := "the authorization code was issued for " + baseA + ", not " + baseB; !strings.Contains(body, `"invalid_grant"`) || !strings.Contains(body, want) {
		t.Errorf("A's authorization code at B: %s, want invalid_grant %q", body, want)
	}
	_, page := mcpRequest(t, http.MethodGet, authorizeURL(client, baseA), nil, nil)
	m := requestField.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("consent page at A:\n%s", page)
	}
	pairing := call[protocol.ConnectorCodeResult](t, h.control(al, s), protocol.MethodConnectorCode, struct{}{})
	resp, page := mcpRequest(t, http.MethodPost, baseB+"/authorize", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		strings.NewReader(url.Values{"request": {m[1]}, "code": {pairing.Code}}.Encode()))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, "This form was not issued by this server.") {
		t.Errorf("A's consent form posted at B: %s\n%s", resp.Status, page)
	}

	refresh := func(base, token string, resource string) tokenAnswer {
		t.Helper()
		form := url.Values{"grant_type": {"refresh_token"}, "client_id": {client.id}, "refresh_token": {token}}
		if resource != "" {
			form.Set("resource", resource)
		}
		_, body := mcpRequest(t, http.MethodPost, base+"/token", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
			strings.NewReader(form.Encode()))
		var a tokenAnswer
		if err := json.Unmarshal([]byte(body), &a); err != nil {
			t.Fatalf("token answer %q: %v", body, err)
		}
		return a
	}
	if a := refresh(baseB, before.RefreshToken, ""); a.Error != "invalid_grant" || !strings.Contains(a.Description, "the refresh token is for "+baseA+", not "+baseB) {
		t.Errorf("A's refresh token at B: %+v", a)
	}
	for _, resource := range []string{baseB, "https://elsewhere.example" + edgeproto.MCPPath(s.id)} {
		if a := refresh(baseA, before.RefreshToken, resource); a.Error != "invalid_target" || a.Description != "resource must be "+baseA {
			t.Errorf("A's refresh token at A for %s: %+v", resource, a)
		}
	}
	rotated := refresh(baseA, before.RefreshToken, baseA)
	if rotated.Error != "" {
		t.Fatalf("A's refresh token at A: %+v", rotated)
	}
	mustCall(t, baseA, rotated.AccessToken)

	s.restart(policy)
	mustCall(t, baseA, rotated.AccessToken)
	mustCall(t, baseB, atB.AccessToken)
	mustCall(t, relayB+edgeproto.PathUniversalMCP, universalB.AccessToken)

	h.stopEdge()
	h.pairB = false
	h.startEdge()
	backOnline(t, h, s)
	mustCall(t, baseA, rotated.AccessToken)
	if resp, body := toolsList(t, baseA, atB.AccessToken, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("B's token at A while the edge serves A only: %s %q", resp.Status, body)
	}

	h.stopEdge()
	h.pairB = true
	h.startEdge()
	backOnline(t, h, s)
	mustCall(t, baseB, atB.AccessToken)
	if a := refresh(baseB, atB.RefreshToken, baseB); a.Error != "" {
		t.Errorf("B's refresh token at B once the edge serves B again: %+v", a)
	}
}

// backOnline waits until the restarted edge holds s's control channel
// again and s's agent has made that session live. The server's own status
// file can still say connected from before the restart until its agent
// notices the old connection ended, and the relay counts the server online
// as it sends Ready, before the agent has read it.
func backOnline(t *testing.T, h *harness, s *serverNode) {
	t.Helper()
	eventually(t, "server back on the restarted edge", func() error {
		switch {
		case !h.relay().Online(s.id):
			return errors.New("not online at the edge")
		case !s.agent.Connected():
			return errors.New("online at the edge, but the agent has not read Ready yet")
		}
		return nil
	})
}

// connectAt authorizes a connector at base, a server's own connector URL,
// with go-sdk's OAuth client and a pairing code c mints, and lists the
// workspaces with it.
func connectAt(t *testing.T, h *harness, client *connectorClient, c *client, s *serverNode, base string) *oauth2.Token {
	t.Helper()
	code := call[protocol.ConnectorCodeResult](t, h.control(c, s), protocol.MethodConnectorCode, struct{}{})
	var token *oauth2.Token
	var iss string
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: client.id},
		RedirectURL:                    client.redirect,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			res, err := client.consent(ctx, args.URL, code.Code)
			if res != nil {
				iss = res.Iss
			}
			return res, err
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
	callTool(t, session, "aether_list_workspaces", nil)
	if iss != base || token == nil {
		t.Fatalf("authorization at %s returned iss %q, token %v", base, iss, token)
	}
	return token
}

// universalAt authorizes a connector at the universal URL of relay, the
// relay origin of the edge's pair with sign-in origin signin, signed in
// there, and lists the workspaces with it.
func universalAt(t *testing.T, h *harness, client *connectorClient, c *client, s *serverNode, policy edgeproto.AccessPolicy, signin, relay string) *oauth2.Token {
	t.Helper()
	code := call[protocol.ConnectorCodeResult](t, h.control(c, s), protocol.MethodConnectorCode, struct{}{})
	before, relayURL := h.signinURL, h.relayURL
	h.signinURL, h.relayURL = signin, relay
	defer func() { h.signinURL, h.relayURL = before, relayURL }()
	b, err := h.signIn(alice)
	if err != nil {
		t.Fatal(err)
	}
	var token *oauth2.Token
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: client.id},
		RedirectURL:                    client.redirect,
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			res, _ := universalConsent(t, b, args.URL, "", s.memberOf(t, alice).DisplayName, code.Code, policy)
			if want := relay + edgeproto.PathUniversalMCP; res.Iss != want {
				return nil, errors.New("iss " + res.Iss + ", want " + want)
			}
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
	session := connectMCP(t, &mcp.StreamableClientTransport{Endpoint: relay + edgeproto.PathUniversalMCP, OAuthHandler: handler})
	callTool(t, session, "aether_list_workspaces", nil)
	return token
}

// toolsList sends a tools/list to base with token, when set, and header.
func toolsList(t *testing.T, base, token string, header http.Header) (*http.Response, string) {
	t.Helper()
	header = header.Clone()
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	return mcpRequest(t, http.MethodPost, base, header, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
}

// mustCall lists tools at base with token, which must work.
func mustCall(t *testing.T, base, token string) {
	t.Helper()
	if resp, body := toolsList(t, base, token, nil); resp.StatusCode != http.StatusOK || !strings.Contains(body, "aether_list_workspaces") {
		t.Fatalf("tools/list at %s: %s %q", base, resp.Status, body)
	}
}

// authorizeURL is an authorization request for base from client, with
// the PKCE challenge of pkceVerifier.
func authorizeURL(client *connectorClient, base string) string {
	sum := sha256.Sum256([]byte(pkceVerifier))
	return base + "/authorize?" + url.Values{"response_type": {"code"}, "client_id": {client.id}, "redirect_uri": {client.redirect},
		"state": {"s"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {base}}.Encode()
}

const pkceVerifier = "a-verifier-of-forty-three-characters-or-more-0123"

// authorizationCode has the member behind c authorize client at base
// with a pairing code, and returns the authorization code and its PKCE
// verifier.
func authorizationCode(t *testing.T, h *harness, client *connectorClient, c *client, s *serverNode, base string) (string, string) {
	t.Helper()
	pairing := call[protocol.ConnectorCodeResult](t, h.control(c, s), protocol.MethodConnectorCode, struct{}{})
	res, err := client.consent(t.Context(), authorizeURL(client, base), pairing.Code)
	if err != nil {
		t.Fatal(err)
	}
	if res.Iss != base {
		t.Fatalf("authorization at %s returned iss %q", base, res.Iss)
	}
	return res.Code, pkceVerifier
}

// A server that speaks edge protocol 5, as one released before
// VersionRelayOrigin does, behind an edge that serves two pairs and sets
// Aether-Edge-Relay-Origin, ignores the header: it serves its connector
// at the relay origin it enrolled at only, as before.
func TestConnectorAtEdgeProtocol5(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
		if ch, ok := e.msg.(edgeproto.Challenge); ok {
			ch.Version = edgeproto.VersionRelayOrigin - 1
			return ch, true
		}
		return e.msg, true
	})
	client := newConnectorClient(t)
	s := h.newConnectorServer(edgeproto.PolicyApprovedDevices, client, sshd.Services{})
	al := h.login(alice)[0]
	h.claimServer(al, s)
	baseA := h.relayURL + edgeproto.MCPPath(s.id)
	h.stopEdge()
	h.pairB = true
	h.startEdge()
	backOnline(t, h, s)
	if v := s.agent.EdgeVersion(); v != edgeproto.VersionRelayOrigin-1 {
		t.Fatalf("EdgeVersion = %d, want %d", v, edgeproto.VersionRelayOrigin-1)
	}
	baseB := h.relayB + edgeproto.MCPPath(s.id)
	var prm map[string]any
	resp, body := mcpRequest(t, http.MethodGet, h.relayB+"/.well-known/oauth-protected-resource"+edgeproto.MCPPath(s.id), nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &prm) != nil || prm["resource"] != baseA {
		t.Fatalf("protected resource metadata at B: %s %s, want resource %s", resp.Status, body, baseA)
	}
	code, verifier := authorizationCode(t, h, client, al, s, baseA)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {client.id}, "code": {code}, "code_verifier": {verifier},
		"redirect_uri": {client.redirect}, "resource": {baseB}}
	_, body = mcpRequest(t, http.MethodPost, baseB+"/token", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, strings.NewReader(form.Encode()))
	if !strings.Contains(body, `"invalid_target"`) || !strings.Contains(body, "resource must be "+baseA) {
		t.Errorf("a resource at B: %s, want invalid_target naming %s", body, baseA)
	}
	if list := call[protocol.ConnectorListResult](t, h.control(al, s), protocol.MethodConnectorList, struct{}{}); len(list.UniversalURLs) != 1 ||
		list.UniversalURLs[0] != h.relayURL+edgeproto.PathUniversalMCP {
		t.Errorf("connector.list universal URLs %v, want only %s", list.UniversalURLs, h.relayURL+edgeproto.PathUniversalMCP)
	}
}
