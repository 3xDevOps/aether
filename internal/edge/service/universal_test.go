package edge

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestUniversalConnectorRouting checks how the relay origin routes the
// universal connector URL: /mcp by the bearer token's server prefix,
// /mcp/token by the prefix of the code or, for a refresh, the refresh
// token, within a limit per address and server; /mcp/authorize to the
// choose page without a server and to the server with one. Requests that
// name no server are answered at the edge.
func TestUniversalConnectorRouting(t *testing.T) {
	h := newHarness(t)
	const a, b = "aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	do := func(method, path, auth string, form url.Values) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, testRelay+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := (&http.Client{Transport: h.client.Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close() //nolint:errcheck // test client
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, strings.TrimSpace(string(body))
	}

	metadata := `resource_metadata="` + testRelay + `/.well-known/oauth-protected-resource/mcp"`
	if resp, body := do(http.MethodPost, "/mcp", "", nil); resp.StatusCode != http.StatusUnauthorized ||
		resp.Header.Get("WWW-Authenticate") != "Bearer "+metadata {
		t.Errorf("tokenless /mcp: %s %v %q", resp.Status, resp.Header, body)
	}
	if resp, body := do(http.MethodPost, "/mcp", "Bearer not-a-server.x", nil); resp.StatusCode != http.StatusUnauthorized ||
		resp.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token", `+metadata {
		t.Errorf("/mcp with a token naming no server: %s %v %q", resp.Status, resp.Header, body)
	}
	if _, body := do(http.MethodPost, "/mcp", "Bearer "+a+".token", nil); body != "forwarded to "+a {
		t.Errorf("/mcp with server a's token: %q", body)
	}

	if _, body := do(http.MethodPost, "/mcp/token", "", url.Values{"grant_type": {"authorization_code"}, "code": {b + ".code"}}); body != "forwarded to "+b {
		t.Errorf("code exchange of server b's code: %q", body)
	}
	if _, body := do(http.MethodPost, "/mcp/token", "", url.Values{"grant_type": {"refresh_token"}, "code": {a + ".code"},
		"refresh_token": {b + ".refresh"}}); body != "forwarded to "+b {
		t.Errorf("refresh of server b's refresh token: %q", body)
	}
	if resp, body := do(http.MethodPost, "/mcp/token", "", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"unprefixed"}}); resp.StatusCode != http.StatusBadRequest ||
		resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(body, `"error":"invalid_grant"`) {
		t.Errorf("refresh token naming no server: %s %q", resp.Status, body)
	}
	if resp, _ := do(http.MethodGet, "/mcp/token", "", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp/token: %s", resp.Status)
	}
	limited := 0
	for range 100 {
		if resp, _ := do(http.MethodPost, "/mcp/token", "", url.Values{"code": {a + ".code"}}); resp.StatusCode == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("100 token requests for server a at once were all forwarded")
	}
	if _, body := do(http.MethodPost, "/mcp/token", "", url.Values{"code": {b + ".code"}}); body != "forwarded to "+b {
		t.Errorf("server b's token request once server a's limit is reached: %q", body)
	}

	query := url.Values{"client_id": {"https://chatgpt.com/client.json"}, "code_challenge": {"c"}}.Encode()
	if resp, body := do(http.MethodGet, "/mcp/authorize?"+query, "", nil); resp.StatusCode != http.StatusSeeOther ||
		resp.Header.Get("Location") != testSignin+"/connectors/choose?"+query {
		t.Errorf("authorize without a server: %s %v %q", resp.Status, resp.Header, body)
	}
	if _, body := do(http.MethodPost, "/mcp/authorize?server="+a+"&"+query, "", url.Values{"request": {"r"}}); body != "forwarded to "+a {
		t.Errorf("consent form for server a: %q", body)
	}
	if resp, _ := do(http.MethodGet, "/mcp/authorize?server=nope", "", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("authorize naming a malformed server: %s", resp.Status)
	}

	if resp, _ := do(http.MethodGet, "/.well-known/openai-apps-challenge", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("domain verification without a token: %s", resp.Status)
	}
	h.svc.openAIChallenge = "challenge-token"
	if resp, body := do(http.MethodGet, "/.well-known/openai-apps-challenge", "", nil); resp.StatusCode != http.StatusOK || body != "challenge-token" ||
		resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("domain verification: %s %v %q", resp.Status, resp.Header, body)
	}
}
