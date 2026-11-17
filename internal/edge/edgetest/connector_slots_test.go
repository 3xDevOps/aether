package edgetest

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/sshd"
)

// connectorPost returns a func that posts body to base as the connector
// holding token.
func connectorPost(t *testing.T, base, token string) func(headers http.Header, body string) (*http.Response, error) {
	return func(headers http.Header, body string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header = headers
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		return http.DefaultClient.Do(req)
	}
}

// served fails the test unless a tools/list through post is answered with
// the tools, behind what holds the server's connections.
func served(t *testing.T, post func(http.Header, string) (*http.Response, error), behind string) {
	t.Helper()
	start := time.Now()
	resp, err := post(http.Header{}, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "aether_list_sessions") {
		t.Fatalf("a short call behind %d %s, after %s: %s %s", edgeproto.MaxHTTPConnsPerServer, behind,
			time.Since(start).Round(time.Second), resp.Status, body)
	}
}

// A client that opens a subscriptions/listen stream on every one of a
// server's connector connections, as an MCP client of protocol 2026-07-28
// may for each conversation, gets each answered at once: a short call
// from the same connector is served, not held for a connection and refused.
func TestConnectorListenHoldsNoConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newConnectorClient(t)
	s := h.newConnectorServer(edgeproto.PolicyAccount, client, sshd.Services{})
	al := h.login(alice)[0]
	h.claimServer(al, s)
	base := h.relayURL + edgeproto.MCPPath(s.id)
	_, token := connectWithToken(t, h.control(al, s), client, base)
	post := connectorPost(t, base, token.AccessToken)
	const listen = `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{` +
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},` +
		`"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"}},"notifications":{"toolsListChanged":true}}}`
	for i := range edgeproto.MaxHTTPConnsPerServer {
		resp, err := post(http.Header{"Mcp-Protocol-Version": {"2026-07-28"}, "Mcp-Method": {"subscriptions/listen"}}, listen)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("listen %d: %s %s", i, resp.Status, body)
		}
	}
	served(t, post, "listen calls")
}

// Eight tool calls that wait, here launches that do not finish, leave
// connections for short calls: the server ends those past its bound of
// long calls, each with the answer that says to call again. That four are
// ended shows all eight were in progress.
func TestConnectorLongCallsLeaveConnections(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newConnectorClient(t)
	started, release := make(chan struct{}, edgeproto.MaxHTTPConnsPerServer), make(chan struct{})
	s := &serverNode{h: h, dir: t.TempDir(), policy: edgeproto.PolicyAccount, client: client,
		launchStarted: started, launchRelease: release}
	s.start()
	t.Cleanup(func() { s.stop() })
	al := h.login(alice)[0]
	h.claimServer(al, s)
	ws := &domain.Workspace{Name: "api", BaseBranch: "main"}
	if err := s.db.CreateWorkspace(t.Context(), ws); err != nil {
		t.Fatal(err)
	}
	grantEveryone(t, s.db, ws.ID)
	base := h.relayURL + edgeproto.MCPPath(s.id)
	_, token := connectWithToken(t, h.control(al, s), client, base, "write")
	post := connectorPost(t, base, token.AccessToken)
	answers := make(chan string, edgeproto.MaxHTTPConnsPerServer)
	for i := range edgeproto.MaxHTTPConnsPerServer {
		go func() {
			resp, err := post(http.Header{}, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aether_launch_session",`+
				`"arguments":{"workspace_id":%q,"harness":"claude","task":"task %d","idempotency_key":"hold-%d"}}}`, ws.ID, i, i))
			if err != nil {
				answers <- err.Error()
				return
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			answers <- resp.Status + " " + string(body)
		}()
	}
	// The server admits one launch at a time, so the launcher holds the
	// first and the rest wait behind it, each on its own connection.
	select {
	case <-started:
	case got := <-answers:
		t.Fatalf("a launch meant to wait ended: %s", got)
	case <-time.After(10 * time.Second):
		t.Fatal("no launch started within 10s")
	}
	time.Sleep(time.Second)
	served(t, post, "launches that wait")
	for range edgeproto.MaxHTTPConnsPerServer - edgeproto.MaxHTTPLongCallsPerServer {
		if got := <-answers; !strings.HasPrefix(got, "200 OK") || !strings.Contains(got, "The launch is still starting; call aether_launch_session again") {
			t.Fatalf("a launch past the bound of long calls: %s", got)
		}
	}
	select {
	case got := <-answers:
		t.Fatalf("a launch inside the bound of long calls ended: %s", got)
	default:
	}
	close(release)
}
