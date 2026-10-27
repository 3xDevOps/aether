package edgetest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/version"
)

// connectorClient is the client registration ChatGPT and Claude make: a
// client_id that is the URL of a metadata document served over HTTPS,
// listing the one redirect the client is sent back to.
type connectorClient struct {
	srv      *httptest.Server
	host     string
	id       string
	redirect string
}

func newConnectorClient(t *testing.T) *connectorClient {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewUnstartedServer(mux)
	host := srv.Listener.Addr().String()
	c := &connectorClient{srv: srv, host: host, id: "https://" + host + "/client.json", redirect: "https://" + host + "/callback"}
	doc, err := json.Marshal(map[string]any{"client_id": c.id, "client_name": "Test client", "redirect_uris": []string{c.redirect}})
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("GET /client.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	})
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return c
}

// newConnectorServer is newServer with the real connector handler serving
// its MCP connector requests, trusting client, and services serving the
// team features.
func (h *harness) newConnectorServer(policy edgeproto.AccessPolicy, client *connectorClient, services sshd.Services) *serverNode {
	h.t.Helper()
	s := &serverNode{h: h, dir: h.t.TempDir(), policy: policy, client: client, services: services, addr: "127.0.0.1:0"}
	s.start()
	h.t.Cleanup(func() { s.stop() })
	return s
}

// launcher starts no agent: it stores each launch as a queued run, which
// run.list then lists.
type launcher struct {
	noRuns
	db *store.DB
}

func (l launcher) Launch(ctx context.Context, ws domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	run := &domain.Run{WorkspaceID: ws, MemberID: member, AccountMemberID: account, Task: task, Harness: harness, Mode: mode, Status: domain.RunQueued}
	return run, l.db.CreateRun(ctx, run)
}

func (launcher) Paused(domain.RunID) bool { return false }
func (launcher) DefaultMode(context.Context, domain.MemberID, domain.MemberID, string) (domain.LaunchMode, error) {
	return domain.LaunchACP, nil
}
func (launcher) PendingInputs(domain.RunID) []domain.RunInputRequest          { return nil }
func (launcher) Cause(domain.RunID, domain.RunActivity) events.RunStatusCause { return "" }
func (launcher) Switching(domain.RunID) domain.LaunchMode                     { return "" }
func (launcher) Retention(*domain.Run) (scheduler.RetentionInfo, error) {
	return scheduler.RetentionInfo{}, nil
}

var requestField = regexp.MustCompile(`name="request" value="([^"]+)"`)

// consent is the member's browser on the consent page, through the edge:
// the page must be sandboxed and name the client. It pastes a wrong code,
// then code with the boxes of scopes checked, and returns where the server
// sent the browser.
func (c *connectorClient) consent(ctx context.Context, authURL, code string, scopes ...string) (*auth.AuthorizationResult, error) {
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(method string, form url.Values) (*http.Response, string, error) {
		req, err := http.NewRequestWithContext(ctx, method, authURL, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, "", err
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := browser.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close() //nolint:errcheck // test client
		body, err := io.ReadAll(resp.Body)
		return resp, string(body), err
	}
	resp, page, err := send(http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	m := requestField.FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil || resp.Header.Get("Content-Security-Policy") != connectorCSP ||
		!strings.Contains(page, "Connect <strong>Test client ("+c.host+")</strong>") {
		return nil, fmt.Errorf("consent page: %s %v\n%s", resp.Status, resp.Header, page)
	}
	if resp, page, err = send(http.MethodPost, url.Values{"request": {m[1]}, "code": {"zzzz-zzzz"}}); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Content-Security-Policy") != connectorCSP ||
		!strings.Contains(page, "That code is wrong, expired or already used. Get a new one from Settings → ChatGPT and Claude in the Aether app, or aether mcp, and paste it here.") {
		return nil, fmt.Errorf("wrong code: %s %v\n%s", resp.Status, resp.Header, page)
	}
	if resp, page, err = send(http.MethodPost, url.Values{"request": {m[1]}, "code": {code}, "scope": scopes}); err != nil {
		return nil, err
	}
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc.String(), c.redirect+"?") {
		return nil, fmt.Errorf("allow: %s %v %v\n%s", resp.Status, loc, err, page)
	}
	q := loc.Query()
	return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
}

func connectMCP(t *testing.T, transport *mcp.StreamableClientTransport) *mcp.ClientSession {
	t.Helper()
	transport.DisableStandaloneSSE = true
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		content, _ := json.Marshal(res.Content)
		t.Fatalf("%s failed: %s", name, content)
	}
	out, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// tokenAnswer is the token endpoint's answer: tokens, or an RFC 6749
// error.
type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// TestConnectorThroughTheEdge adds a server as a custom connector at its
// own URL, <edge>/mcp/<server id>, the way ChatGPT does, over HTTP with go-sdk's MCP and OAuth client, through a
// real edge to a server serving the real connector handler, under both
// policies. A tokenless call gets a 401 naming the metadata, which the
// edge relays; the client registers by a metadata document served over
// HTTPS; a member mints a pairing code over their relayed SSH connection
// and pastes a wrong code, then it, on the sandboxed consent page; the
// token exchange, the tool list, aether_list_runs and aether_launch_run,
// a refresh rotation and aether mcp revoke work through the edge, and the
// revoked connector's next call is refused. Under approved-devices a
// device waiting for approval mints no code.
func TestConnectorThroughTheEdge(t *testing.T) {
	t.Parallel()
	for _, policy := range policies {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			client := newConnectorClient(t)
			s := h.newConnectorServer(policy, client, sshd.Services{})
			clients := h.login(alice, bob, bob)
			al, bo, bobNew := clients[0], clients[1], clients[2]
			h.claimServer(al, s)
			h.join(h.control(al, s), bo, s, string(domain.RoleCollaborator))
			ws := &domain.Workspace{Name: "api", BaseBranch: "main"}
			if err := s.db.CreateWorkspace(t.Context(), ws); err != nil {
				t.Fatal(err)
			}
			base := h.relayURL + edgeproto.MCPPath(s.id)
			bearer := func(token string) (*http.Response, string) {
				header := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}
				if token != "" {
					header.Set("Authorization", "Bearer "+token)
				}
				return mcpRequest(t, http.MethodPost, base, header, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			}

			resp, body := bearer("")
			metadata := h.relayURL + "/.well-known/oauth-protected-resource" + edgeproto.MCPPath(s.id)
			if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="`+metadata+`"`) {
				t.Fatalf("tokenless call: %s %v %q, want 401 naming %s", resp.Status, resp.Header, body, metadata)
			}
			var prm, asm map[string]any
			if resp, body = mcpRequest(t, http.MethodGet, metadata, nil, nil); resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &prm) != nil ||
				prm["resource"] != base || fmt.Sprint(prm["authorization_servers"]) != "["+base+"]" ||
				fmt.Sprint(prm["scopes_supported"]) != "[offline_access]" {
				t.Fatalf("protected resource metadata: %s %s", resp.Status, body)
			}
			if resp, body = mcpRequest(t, http.MethodGet, h.relayURL+"/.well-known/oauth-authorization-server"+edgeproto.MCPPath(s.id), nil, nil); resp.StatusCode != http.StatusOK ||
				json.Unmarshal([]byte(body), &asm) != nil || asm["issuer"] != base || asm["authorization_endpoint"] != base+"/authorize" ||
				asm["token_endpoint"] != base+"/token" || asm["client_id_metadata_document_supported"] != true || asm["registration_endpoint"] != nil ||
				fmt.Sprint(asm["scopes_supported"]) != "[offline_access]" {
				t.Fatalf("authorization server metadata: %s %s", resp.Status, body)
			}

			bctl := h.control(bo, s)
			code := call[protocol.ConnectorCodeResult](t, bctl, protocol.MethodConnectorCode, struct{}{})
			if want := h.relayURL + edgeproto.PathUniversalMCP; code.URL != want {
				t.Fatalf("connector.code url = %s, want the universal %s", code.URL, want)
			}
			var token *oauth2.Token
			handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
				ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: client.id},
				RedirectURL:                    client.redirect,
				AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
					return client.consent(ctx, args.URL, code.Code, "write", "control")
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

			var names []string
			for tool, err := range session.Tools(t.Context(), nil) {
				if err != nil {
					t.Fatal(err)
				}
				names = append(names, tool.Name)
			}
			if len(names) != 36 {
				t.Fatalf("tools = %v, want all 36", names)
			}
			const task = "fix the flaky login test"
			if out := callTool(t, session, "aether_list_runs", map[string]any{"mine": true}); strings.Contains(out, task) {
				t.Fatalf("aether_list_runs before the launch = %s", out)
			}
			callTool(t, session, "aether_launch_run", map[string]any{"workspace_id": ws.ID, "harness": "claude", "task": task})
			if out := callTool(t, session, "aether_list_runs", map[string]any{"mine": true}); !strings.Contains(out, task) {
				t.Fatalf("aether_list_runs after the launch = %s", out)
			}
			runs, err := s.db.ListRunsByMember(t.Context(), s.memberOf(t, bob).ID)
			if err != nil || len(runs) != 1 || runs[0].Task != task || runs[0].WorkspaceID != ws.ID {
				t.Fatalf("bob's runs = %+v %v, want the one launched through the connector", runs, err)
			}
			list := call[protocol.ConnectorListResult](t, bctl, protocol.MethodConnectorList, struct{}{})
			if len(list.Connectors) != 1 || list.Connectors[0].DeviceID != string(s.deviceIDOf(t, bo)) || list.Connectors[0].RedirectHost != client.host ||
				list.Connectors[0].LastUsedAt == "" {
				t.Fatalf("connector.list = %+v, want one connector bound to bob's device", list)
			}

			resp, body = mcpRequest(t, http.MethodPost, base+"/token", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
				strings.NewReader(url.Values{"grant_type": {"refresh_token"}, "client_id": {client.id}, "refresh_token": {token.RefreshToken}, "resource": {base}}.Encode()))
			var rotated tokenAnswer
			if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &rotated) != nil || rotated.RefreshToken == "" ||
				rotated.RefreshToken == token.RefreshToken || rotated.AccessToken == token.AccessToken {
				t.Fatalf("refresh: %s %s, want both tokens rotated", resp.Status, body)
			}
			if resp, body = bearer(token.AccessToken); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("the access token the refresh replaced: %s %s", resp.Status, body)
			}
			live := connectMCP(t, &mcp.StreamableClientTransport{Endpoint: base,
				HTTPClient: &http.Client{Transport: &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: rotated.AccessToken})}}})
			callTool(t, live, "aether_list_workspaces", nil)

			revoked := call[protocol.ConnectorRevokeResult](t, bctl, protocol.MethodConnectorRevoke, protocol.ConnectorRevokeParams{ConnectorID: list.Connectors[0].ID})
			if revoked.Connector.ID != list.Connectors[0].ID {
				t.Fatalf("connector.revoke = %+v", revoked)
			}
			if _, err = live.ListTools(t.Context(), nil); err == nil {
				t.Fatal("a revoked connector still lists tools")
			}
			if resp, body = bearer(rotated.AccessToken); resp.StatusCode != http.StatusUnauthorized ||
				!strings.Contains(body, "no connector holds this access token") {
				t.Fatalf("a revoked connector's call: %s %s", resp.Status, body)
			}

			ctl, err := h.tryControl(bobNew, s)
			if policy == edgeproto.PolicyApprovedDevices {
				s.waitingCode(t, "bob's new device minting a pairing code", err)
				return
			}
			if err != nil {
				t.Fatalf("bob's new device under account: %v", err)
			}
			call[protocol.ConnectorCodeResult](t, ctl, protocol.MethodConnectorCode, struct{}{})
		})
	}
}

// TestConnectorAtEdgeProtocol1 has the edge and a server serving the real
// connector handler settle on protocol version 1, as an edge or a server
// from before connectors does. The edge answers connector requests with
// 501 and the server's release, sends the server no http open, and keeps
// relaying its control and SSH connections; connector.code refuses with
// the edge's version.
func TestConnectorAtEdgeProtocol1(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.proxy.setTamper(t, func(e logEntry) (edgeproto.Message, bool) {
		if ch, ok := e.msg.(edgeproto.Challenge); ok {
			ch.Version = 1
			return ch, true
		}
		return e.msg, true
	})
	s := h.newConnectorServer(edgeproto.PolicyApprovedDevices, newConnectorClient(t), sshd.Services{})
	al := h.login(alice)[0]
	h.claimServer(al, s)
	if v := s.agent.EdgeVersion(); v != 1 {
		t.Fatalf("EdgeVersion = %d, want 1", v)
	}

	from := h.proxy.mark()
	name, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("server %s (%s) runs aether-server %s, which has no MCP endpoint; upgrade it: sudo aether-server update", name, s.id, version.Version)
	for _, path := range []string{edgeproto.MCPPath(s.id), "/.well-known/oauth-protected-resource" + edgeproto.MCPPath(s.id)} {
		if resp, body := mcpRequest(t, http.MethodGet, h.relayURL+path, nil, nil); resp.StatusCode != http.StatusNotImplemented || strings.TrimSpace(body) != want {
			t.Errorf("GET %s: %s %q, want 501 %q", path, resp.Status, body, want)
		}
	}
	err = h.control(al, s).Call(protocol.MethodConnectorCode, struct{}{}, nil)
	if wantCode := "connector.code: edge " + h.relayURL + " speaks edge protocol 1 and cannot forward connectors; its operator upgrades it"; err == nil ||
		!strings.Contains(err.Error(), wantCode) {
		t.Errorf("connector.code at protocol 1: %v, want %q", err, wantCode)
	}
	h.proxy.mu.Lock()
	log := slices.Clone(h.proxy.log[from:])
	h.proxy.mu.Unlock()
	for _, e := range log {
		switch m := e.msg.(type) {
		case edgeproto.Open:
			if m.Kind == edgeproto.KindHTTP {
				t.Errorf("the edge sent a protocol-1 server an http open: %+v", m)
			}
		case edgeproto.Hello:
			t.Errorf("the server enrolled again: %+v", m)
		}
	}
}
