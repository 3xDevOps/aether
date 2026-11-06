package edgetest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

const connectorCSP = "sandbox allow-forms; default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'"

// stubConnector stands in for a server's MCP connector endpoint. It answers
// with what reached it, streams on /stream, holds requests on /hold until
// release closes, and redirects on /authorize. Every answer tries to set
// a cookie, a service worker scope and a session id, and asks a proxy not
// to buffer it.
type stubConnector struct {
	proceed chan struct{}
	held    chan struct{}
	release chan struct{}
}

// seen is what reached the connector.
type seen struct {
	URI    string      `json:"uri"`
	Host   string      `json:"host"`
	Header http.Header `json:"header"`
	Body   int         `json:"body"`
}

func newStubConnector() *stubConnector {
	return &stubConnector{proceed: make(chan struct{}), held: make(chan struct{}, edgeproto.MaxHTTPConnsPerServer), release: make(chan struct{})}
}

func (c *stubConnector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Set-Cookie", "session=1; Path=/")
	w.Header().Set("Service-Worker-Allowed", "/")
	w.Header().Set("Mcp-Session-Id", "session-1")
	w.Header().Set("X-Accel-Buffering", "no")
	switch {
	case strings.HasSuffix(r.URL.Path, "/stream"):
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "first\n")
		_ = http.NewResponseController(w).Flush()
		select {
		case <-c.proceed:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "second\n")
	case strings.HasSuffix(r.URL.Path, "/hold"):
		// Not even a stopping server answers it, so its connection is
		// never free for another request.
		c.held <- struct{}{}
		<-c.release
	case strings.HasSuffix(r.URL.Path, "/authorize"):
		http.Redirect(w, r, "https://chatgpt.example.test/callback?code=x", http.StatusFound)
	default:
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(seen{URI: r.RequestURI, Host: r.Host, Header: r.Header, Body: len(body)})
	}
}

// mcpRequest sends one request to the edge and returns the answer with
// its body. It follows no redirect.
func mcpRequest(t *testing.T, method, target string, header http.Header, body io.Reader) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(data)
}

// TestMCPConnector relays MCP connector requests through a real edge to a
// real agent serving a stub handler: the path and query arrive
// unchanged, the request's headers pass but for cookies and forwarding
// headers, only the listed answer headers pass, every answer is
// sandboxed, a streamed answer streams, bodies are capped at 1 MiB, a
// server holds at most 8 connections apart from its SSH ones and a
// request past them waits for one, and the edge refuses, with the cause, a server that serves no connector, a
// path on the wrong host and an offline server. Requests to a server that
// just went offline are held for its return, at most 16, until the
// return wait passes.
func TestMCPConnector(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	conn := newStubConnector()
	s := h.newMCPServer(edgeproto.PolicyApprovedDevices, conn)
	none := h.newServer(edgeproto.PolicyApprovedDevices)
	al := h.login(alice)[0]
	h.claimServer(al, s)
	h.claimServer(al, none)
	base := h.relayURL + edgeproto.MCPPath(s.id)
	if v := s.agent.EdgeVersion(); v != edgeproto.Version {
		t.Fatalf("EdgeVersion = %d, want %d", v, edgeproto.Version)
	}

	t.Run("round trip", func(t *testing.T) {
		sent := http.Header{
			"Authorization":        {"Bearer connector-token"},
			"Content-Type":         {"application/json"},
			"Accept":               {"application/json, text/event-stream"},
			"Mcp-Protocol-Version": {"2026-07-28"},
			"Mcp-Param-Region":     {"eu"},
			"User-Agent":           {"probe"},
			"Origin":               {"null"},
		}
		header := http.Header{"Cookie": {"edge=1"}, "X-Forwarded-Port": {"443"}, edgeproto.HeaderClientAddr: {"203.0.113.9:1"}}
		for k, v := range sent {
			header[k] = v
		}
		path := edgeproto.MCPPath(s.id) + "/rpc/x?a=1&b=two%20words&b=3"
		resp, body := mcpRequest(t, http.MethodPost, h.relayURL+path, header, strings.NewReader(`{"jsonrpc":"2.0"}`))
		var got seen
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
			t.Fatalf("POST %s: %s %q", path, resp.Status, body)
		}
		if got.URI != path || got.Host != s.id || got.Body != len(`{"jsonrpc":"2.0"}`) {
			t.Fatalf("the server saw %s on %s with %d bytes; want %s on %s", got.URI, got.Host, got.Body, path, s.id)
		}
		for k, v := range sent {
			if strings.Join(got.Header[k], ",") != strings.Join(v, ",") {
				t.Errorf("request header %s reached the server as %q, want %q", k, got.Header[k], v)
			}
		}
		addr, err := netip.ParseAddrPort(got.Header.Get(edgeproto.HeaderClientAddr))
		if err != nil || addr.Addr() != netip.MustParseAddr("127.0.0.1") {
			t.Errorf("%s = %q, want the edge's own record of the client, 127.0.0.1", edgeproto.HeaderClientAddr, got.Header.Get(edgeproto.HeaderClientAddr))
		}
		for k := range got.Header {
			if _, ok := sent[k]; !ok && k != edgeproto.HeaderClientAddr && k != "Content-Length" {
				t.Errorf("request header %s = %q reached the server", k, got.Header[k])
			}
		}
		if csp := resp.Header.Get("Content-Security-Policy"); csp != connectorCSP {
			t.Errorf("Content-Security-Policy = %q, want %q", csp, connectorCSP)
		}
		if resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Service-Worker-Allowed") != "" ||
			resp.Header.Get("Mcp-Session-Id") != "" {
			t.Errorf("response headers %v: want X-Accel-Buffering and no Set-Cookie, Service-Worker-Allowed or Mcp-Session-Id", resp.Header)
		}
	})

	t.Run("metadata", func(t *testing.T) {
		for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
			path += edgeproto.MCPPath(s.id)
			resp, body := mcpRequest(t, http.MethodGet, h.relayURL+path, nil, nil)
			var got seen
			if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.URI != path {
				t.Fatalf("GET %s: %s %q", path, resp.Status, body)
			}
		}
	})

	t.Run("redirect", func(t *testing.T) {
		resp, _ := mcpRequest(t, http.MethodGet, base+"/authorize?state=s", nil, nil)
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://chatgpt.example.test/callback?code=x" ||
			resp.Header.Get("Content-Security-Policy") != connectorCSP {
			t.Fatalf("authorize: %s %v; want 302 to the callback, sandboxed", resp.Status, resp.Header)
		}
	})

	t.Run("streamed", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close() //nolint:errcheck // test client
		if resp.Header.Get("Content-Security-Policy") != connectorCSP {
			t.Errorf("streamed answer not sandboxed: %v", resp.Header)
		}
		r := bufio.NewReader(resp.Body)
		// The server writes the second line only after this read.
		if line, err := r.ReadString('\n'); err != nil || line != "first\n" {
			t.Fatalf("first line %q, %v", line, err)
		}
		close(conn.proceed)
		if line, err := r.ReadString('\n'); err != nil || line != "second\n" {
			t.Fatalf("second line %q, %v", line, err)
		}
	})

	t.Run("body cap", func(t *testing.T) {
		resp, body := mcpRequest(t, http.MethodPost, base, nil, strings.NewReader(strings.Repeat("x", edgeproto.MaxHTTPBodySize)))
		var got seen
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Body != edgeproto.MaxHTTPBodySize {
			t.Fatalf("a 1 MiB body: %s %q", resp.Status, body)
		}
		resp, body = mcpRequest(t, http.MethodPost, base, nil, strings.NewReader(strings.Repeat("x", edgeproto.MaxHTTPBodySize+1)))
		if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body, "request body is over the edge's limit of 1048576 bytes") {
			t.Fatalf("a body over 1 MiB: %s %q", resp.Status, body)
		}
	})

	t.Run("per-server cap", func(t *testing.T) {
		const extra = 4
		var wg sync.WaitGroup
		results := make(chan string, edgeproto.MaxHTTPConnsPerServer+extra)
		for range edgeproto.MaxHTTPConnsPerServer + extra {
			wg.Go(func() {
				resp, err := http.Post(base+"/hold", "text/plain", nil)
				if err != nil {
					results <- err.Error()
					return
				}
				_ = resp.Body.Close()
				results <- resp.Status
			})
		}
		for range edgeproto.MaxHTTPConnsPerServer {
			<-conn.held
		}
		select {
		case <-conn.held:
			t.Fatalf("a request past the %d connections reached the server while they were busy", edgeproto.MaxHTTPConnsPerServer)
		case res := <-results:
			t.Fatalf("a request past the %d busy connections ended with %s; want it to wait", edgeproto.MaxHTTPConnsPerServer, res)
		case <-time.After(200 * time.Millisecond):
		}
		_ = h.mustDial(al, s).Close()
		close(conn.release)
		wg.Wait()
		close(results)
		for res := range results {
			if res != "200 OK" {
				t.Errorf("a held request ended with %s", res)
			}
		}
	})

	t.Run("no connector", func(t *testing.T) {
		resp, body := mcpRequest(t, http.MethodPost, h.relayURL+edgeproto.MCPPath(none.id), nil, nil)
		if resp.StatusCode != http.StatusForbidden || strings.TrimSpace(body) != "this server does not serve MCP connectors" {
			t.Fatalf("a server without a connector: %s %q", resp.Status, body)
		}
	})

	t.Run("wrong host", func(t *testing.T) {
		resp, body := mcpRequest(t, http.MethodPost, h.signinURL+edgeproto.MCPPath(s.id), nil, nil)
		want := edgeproto.MCPPath(s.id) + " is served on " + h.relayURL + ", not on this host"
		if resp.StatusCode != http.StatusMisdirectedRequest || !strings.Contains(body, want) {
			t.Errorf("on the sign-in host: %s %q, want 421 %q", resp.Status, body, want)
		}
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "other.example.test:" + u.Port()
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusMisdirectedRequest || !strings.Contains(string(data), `not the host "other.example.test"`) {
			t.Errorf("on another host: %s %q", r.Status, data)
		}
	})

	t.Run("offline", func(t *testing.T) {
		s.stop()
		eventually(t, "server offline", func() error {
			if h.relay().Online(s.id) {
				return fmt.Errorf("server %s still online", s.id)
			}
			return nil
		})
		if v := s.agent.EdgeVersion(); v != 0 {
			t.Errorf("EdgeVersion of a stopped server = %d, want 0", v)
		}
		refused := func(what string, resp *http.Response, body string) {
			if resp.StatusCode != http.StatusServiceUnavailable || strings.TrimSpace(body) != string(edgeproto.RefusalNotConnected) {
				t.Errorf("%s: %s %q", what, resp.Status, body)
			}
		}
		var wg sync.WaitGroup
		held := make(chan struct{}, edgeproto.MaxHTTPWaitersPerServer)
		for range edgeproto.MaxHTTPWaitersPerServer {
			wg.Go(func() {
				resp, body := mcpRequest(t, http.MethodPost, base, nil, nil)
				held <- struct{}{}
				refused("a request held past the return wait", resp, body)
			})
		}
		eventually(t, "every request held", func() error {
			select {
			case <-held:
				t.Fatal("a request to a server that just went offline was answered; want it held")
			default:
			}
			if n := h.relay().Metrics().MCPWaiting; n != edgeproto.MaxHTTPWaitersPerServer {
				return fmt.Errorf("%d held", n)
			}
			return nil
		})
		start := time.Now()
		resp, body := mcpRequest(t, http.MethodPost, base, nil, nil)
		refused("a request past the held ones", resp, body)
		if waited := time.Since(start); waited > returnWait/2 {
			t.Errorf("a request past the %d held ones waited %s; want it refused at once", edgeproto.MaxHTTPWaitersPerServer, waited)
		}
		wg.Wait()
	})
}

// TestMCPRequestQueuedAcrossRestart has a request wait for one of its
// server's 8 busy connections when the server stops. The edge refuses it
// as not connected before it was written to the server, so it waits for
// the server to return and reaches the restarted one.
func TestMCPRequestQueuedAcrossRestart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	conn := newStubConnector()
	s := h.newMCPServer(edgeproto.PolicyApprovedDevices, conn)
	h.claimServer(h.login(alice)[0], s)
	base := h.relayURL + edgeproto.MCPPath(s.id)
	t.Cleanup(func() { close(conn.release) })
	for range edgeproto.MaxHTTPConnsPerServer {
		go func() {
			if resp, err := http.Post(base+"/hold", "text/plain", nil); err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	for range edgeproto.MaxHTTPConnsPerServer {
		<-conn.held
	}
	type answer struct {
		status int
		body   string
		err    error
	}
	queued := make(chan answer, 1)
	go func() {
		resp, err := http.Post(base, "application/json", strings.NewReader("{}"))
		if err != nil {
			queued <- answer{err: err}
			return
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		queued <- answer{resp.StatusCode, strings.TrimSpace(string(body)), err}
	}()
	eventually(t, "the request waiting for a connection", func() error {
		if n := h.relay().Metrics().MCPWaiting; n != 1 {
			return fmt.Errorf("%d waiting", n)
		}
		return nil
	})
	s.stop()
	eventually(t, "the request waiting for the server", func() error {
		select {
		case a := <-queued:
			t.Fatalf("the queued request ended while the server was down: %d %q %v", a.status, a.body, a.err)
		default:
		}
		if h.relay().Online(s.id) || h.relay().Metrics().MCPWaiting != 1 {
			return fmt.Errorf("server online %t, %d waiting", h.relay().Online(s.id), h.relay().Metrics().MCPWaiting)
		}
		return nil
	})
	s.start()
	if a := <-queued; a.err != nil || a.status != http.StatusOK {
		t.Fatalf("the queued request after the restart: %d %q %v, want 200", a.status, a.body, a.err)
	}
}
