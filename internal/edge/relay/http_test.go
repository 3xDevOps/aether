package relay

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"net/textproto"
	"strings"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
)

// rawAnswer is a server's answer that tries every way past the edge's
// response filter: a 1xx with a cookie, headers off the list, a CSP of its
// own, and a declared and an undeclared trailer.
const rawAnswer = "HTTP/1.1 103 Early Hints\r\nSet-Cookie: early=1\r\nLink: </x.css>; rel=preload\r\n\r\n" +
	"HTTP/1.1 200 OK\r\n" +
	"Content-Type: application/json\r\n" +
	"Cache-Control: no-store\r\n" +
	"X-Accel-Buffering: no\r\n" +
	"Mcp-Session-Id: session-1\r\n" +
	"WWW-Authenticate: Bearer resource_metadata=\"https://edge.example.test/.well-known/oauth-protected-resource/mcp/x\"\r\n" +
	"Set-Cookie: session=1; Path=/\r\n" +
	"Service-Worker-Allowed: /\r\n" +
	"Access-Control-Allow-Origin: *\r\n" +
	"Content-Security-Policy: default-src *\r\n" +
	"Trailer: X-Late\r\n" +
	"Transfer-Encoding: chunked\r\n\r\n" +
	"2\r\n{}\r\n0\r\nX-Late: 1\r\nSet-Cookie: late=1\r\n\r\n"

// serveData answers every request on each data socket a attaches with
// answer, and sends the request, its body read, to the returned channel.
func serveData(t *testing.T, a *agent, answer string) <-chan *http.Request {
	reqs := make(chan *http.Request, 16)
	go func() {
		for {
			select {
			case nc := <-a.data:
				go func() {
					br := bufio.NewReader(nc)
					for {
						req, err := http.ReadRequest(br)
						if err != nil {
							return
						}
						body, _ := io.ReadAll(req.Body)
						req.Body = io.NopCloser(bytes.NewReader(body))
						reqs <- req
						if _, err := io.WriteString(nc, answer); err != nil {
							return
						}
					}
				}()
			case <-t.Context().Done():
				return
			}
		}
	}()
	return reqs
}

// A forwarded request keeps its path, query and headers but for Cookie,
// hop-by-hop headers, Accept-Encoding, Host and the client's forwarding
// headers, and carries the client address as the edge saw it; the answer
// carries only the listed headers and the edge's sandbox, with no 1xx and
// no trailer. A directory push, which closes the connections of accounts
// it no longer lists, leaves the kept-alive http connection open.
func TestHTTPHeaders(t *testing.T) {
	e := newEnv(t)
	a := claimedAgent(t, e)
	reqs := serveData(t, a, rawAnswer)
	path := edgeproto.MCPPath(a.id) + "/rpc?a=1&b=two%20words&b=3"
	const body = `{"jsonrpc":"2.0","id":1}`
	passed := http.Header{
		"Authorization":        {"Bearer connector-token"},
		"Content-Type":         {"application/json"},
		"Accept":               {"application/json, text/event-stream"},
		"Mcp-Protocol-Version": {"2026-07-28"},
		"Mcp-Method":           {"tools/call"},
		"Mcp-Param-Region":     {"eu"},
		"User-Agent":           {"probe"},
		"X-Custom":             {"1", "2"},
	}
	do := func() {
		t.Helper()
		// A body of unknown length is sent chunked, with the trailer.
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.base+path, io.MultiReader(strings.NewReader(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = passed.Clone()
		for k, v := range map[string]string{"Cookie": "edge=1", "X-Forwarded-For": "198.51.100.1", "X-Forwarded-Port": "443",
			"Forwarded": "for=198.51.100.1", "Accept-Encoding": "gzip", "Te": "trailers", "Connection": "Upgrade", "Upgrade": "websocket",
			edgeproto.HeaderClientAddr: "203.0.113.9:1"} {
			req.Header.Set(k, v)
		}
		req.Trailer = http.Header{"X-Client-Trailer": {"1"}}
		var early []int
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			Got1xxResponse: func(code int, _ textproto.MIMEHeader) error { early = append(early, code); return nil },
		}))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || string(got) != "{}" {
			t.Fatalf("answer %s %q, %v", resp.Status, got, err)
		}
		for k, v := range resp.Header {
			switch k {
			case "Content-Type", "Cache-Control", "X-Accel-Buffering", "Www-Authenticate", "Content-Security-Policy", "Date":
			default:
				t.Errorf("response header %s = %q passed", k, v)
			}
		}
		if csp := resp.Header.Get("Content-Security-Policy"); csp != connectorCSP {
			t.Errorf("Content-Security-Policy = %q, want %q", csp, connectorCSP)
		}
		if len(early) != 0 || len(resp.Trailer) != 0 {
			t.Errorf("1xx answers %v and trailer %v passed", early, resp.Trailer)
		}

		var sreq *http.Request
		select {
		case sreq = <-reqs:
		case <-time.After(5 * time.Second):
			t.Fatal("no request reached the server")
		}
		if sreq.RequestURI != path || sreq.Host != a.id {
			t.Errorf("server got %s on %s, want %s on %s", sreq.RequestURI, sreq.Host, path, a.id)
		}
		for k, v := range sreq.Header {
			if _, ok := passed[k]; !ok && k != edgeproto.HeaderClientAddr && k != "Content-Length" {
				t.Errorf("request header %s = %q reached the server", k, v)
			}
		}
		for k, v := range passed {
			if strings.Join(sreq.Header[k], ",") != strings.Join(v, ",") {
				t.Errorf("request header %s reached the server as %q, want %q", k, sreq.Header[k], v)
			}
		}
		if ap, err := netip.ParseAddrPort(sreq.Header.Get(edgeproto.HeaderClientAddr)); err != nil || !ap.Addr().IsLoopback() {
			t.Errorf("%s = %q, want the client's address", edgeproto.HeaderClientAddr, sreq.Header.Get(edgeproto.HeaderClientAddr))
		}
		sent, _ := io.ReadAll(sreq.Body)
		if string(sent) != body || sreq.ContentLength != int64(len(body)) || len(sreq.TransferEncoding) != 0 || len(sreq.Trailer) != 0 {
			t.Errorf("server got body %q of length %d, encoding %v, trailer %v", sent, sreq.ContentLength, sreq.TransferEncoding, sreq.Trailer)
		}
	}
	do()
	if got := e.r.Metrics().BytesRelayed; got < uint64(len(rawAnswer)+len(body)) {
		t.Errorf("relayed %d bytes, want the request and the answer counted", got)
	}

	e.dir.mu.Lock()
	writes := e.dir.dirWrites
	e.dir.mu.Unlock()
	send(t, a.ws, edgeproto.Directory{Entries: []edgeproto.DirectoryEntry{
		{Kind: edgeproto.EntryMember, Provider: edgeproto.ProviderGitHub, Subject: "1001", Role: "admin"},
	}})
	eventually(t, "directory stored", func() bool {
		e.dir.mu.Lock()
		defer e.dir.mu.Unlock()
		return e.dir.dirWrites > writes
	})
	do()
	next[edgeproto.Open](t, a)
	select {
	case m := <-a.msgs:
		t.Fatalf("server got %T %+v; want the second request on the first connection", m, m)
	default:
	}
}

// The edge refuses, in plain text and with the cause, a request for a
// server that is not a server id, unknown, offline, unclaimed, or
// connected at protocol version 1. A version-1 server never receives an
// http open, which it would answer by dropping its control channel.
func TestHTTPRefusals(t *testing.T) {
	e := newEnv(t)
	unknown := edgeproto.ServerID(newSigner(t).PublicKey())
	offline := edgeproto.ServerID(newSigner(t).PublicKey())
	signer := newSigner(t)
	e.dir.mu.Lock()
	e.dir.owners[offline] = true
	e.dir.owners[edgeproto.ServerID(signer.PublicKey())] = true
	e.dir.mu.Unlock()
	unclaimed := enroll(t, e, newSigner(t))
	old := enrollAt(t, e, signer, 1)
	go old.run()

	for _, tt := range []struct {
		name, path string
		status     int
		want       string
	}{
		{"not a server id", "/mcp/not-a-server/rpc", http.StatusNotFound, "unknown server"},
		{"unknown", edgeproto.MCPPath(unknown), http.StatusNotFound, "unknown server"},
		{"offline", edgeproto.MCPPath(offline), http.StatusServiceUnavailable, "server is not connected to the edge"},
		{"unclaimed", edgeproto.MCPPath(unclaimed.id), http.StatusConflict, "server is connected to the edge but not claimed"},
		{"version 1", "/.well-known/oauth-protected-resource" + edgeproto.MCPPath(old.id), http.StatusNotImplemented,
			"server devbox (" + old.id + ") runs aether-server test, which has no MCP endpoint; upgrade it: sudo aether-server update"},
	} {
		resp, err := http.Post(e.base+tt.path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tt.status || strings.TrimSpace(string(got)) != tt.want || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
			t.Errorf("%s: %s %s %q, want %d %q", tt.name, resp.Status, resp.Header.Get("Content-Type"), got, tt.status, tt.want)
		}
	}

	send(t, old.ws, edgeproto.Ping{})
	select {
	case m, ok := <-old.msgs:
		if _, pong := m.(edgeproto.Pong); !ok || !pong {
			t.Fatalf("version-1 server got %T %+v (closed: %v); want only the pong", m, m, !ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no pong")
	}
	if !e.r.Online(old.id) {
		t.Fatal("version-1 server went offline")
	}
}

// A universal connector request for a server whose protocol predates the
// universal URL is refused with 501, naming the server's own URL, and
// never reaches it.
func TestUniversalRefusesAnOlderServer(t *testing.T) {
	e := newEnv(t)
	signer := newSigner(t)
	e.dir.mu.Lock()
	e.dir.owners[edgeproto.ServerID(signer.PublicKey())] = true
	e.dir.mu.Unlock()
	old := enrollAt(t, e, signer, edgeproto.VersionHTTP)
	go old.run()
	if v := e.r.Version(old.id); v != edgeproto.VersionHTTP {
		t.Fatalf("Version = %d, want %d", v, edgeproto.VersionHTTP)
	}
	w := httptest.NewRecorder()
	e.r.ForwardUniversal(w, httptest.NewRequest(http.MethodPost, edgeproto.PathUniversalMCP, nil), old.id, nil)
	want := "server devbox (" + old.id + ") runs aether-server test, which does not serve the connector at " + e.origin + "/mcp; " +
		"upgrade it: sudo aether-server update, or use its own connector URL " + e.origin + edgeproto.MCPPath(old.id)
	if w.Code != http.StatusNotImplemented || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("universal request to a protocol-2 server: %d %q, want 501 %q", w.Code, w.Body.String(), want)
	}
}
