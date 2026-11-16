package edgetest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
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
	s.waitEnrolled()

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
	universalB := universalAt(t, h, client, al, s, policy)
	mustCall(t, baseA, before.AccessToken)

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
	s.waitEnrolled()
	mustCall(t, baseA, rotated.AccessToken)
	if resp, body := toolsList(t, baseA, atB.AccessToken, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("B's token at A while the edge serves A only: %s %q", resp.Status, body)
	}

	h.stopEdge()
	h.pairB = true
	h.startEdge()
	s.waitEnrolled()
	mustCall(t, baseB, atB.AccessToken)
	if a := refresh(baseB, atB.RefreshToken, baseB); a.Error != "" {
		t.Errorf("B's refresh token at B once the edge serves B again: %+v", a)
	}
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

// universalAt authorizes a connector at the universal URL of the edge's
// second pair, signed in at its sign-in origin, and lists the workspaces
// with it.
func universalAt(t *testing.T, h *harness, client *connectorClient, c *client, s *serverNode, policy edgeproto.AccessPolicy) *oauth2.Token {
	t.Helper()
	code := call[protocol.ConnectorCodeResult](t, h.control(c, s), protocol.MethodConnectorCode, struct{}{})
	signin, relayURL := h.signinURL, h.relayURL
	h.signinURL, h.relayURL = h.signinB, h.relayB
	defer func() { h.signinURL, h.relayURL = signin, relayURL }()
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
			if want := h.relayB + edgeproto.PathUniversalMCP; res.Iss != want {
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
	session := connectMCP(t, &mcp.StreamableClientTransport{Endpoint: h.relayB + edgeproto.PathUniversalMCP, OAuthHandler: handler})
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
