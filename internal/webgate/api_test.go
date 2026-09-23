package webgate

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/memberhome"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// stubBackend records control calls; the streaming surfaces are never
// reached by the handlers under test here.
type stubBackend struct {
	calls []string
}

func (b *stubBackend) Call(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, *protocol.Error) {
	b.calls = append(b.calls, method)
	return nil, nil
}

func (b *stubBackend) Events(context.Context, protocol.SubscribeRequest) (io.ReadCloser, error) {
	panic("not reached")
}

func (b *stubBackend) Attach(context.Context, protocol.AttachRequest) (Terminal, protocol.AttachResponse, error) {
	panic("not reached")
}

func (b *stubBackend) Terminal(context.Context, protocol.TerminalRequest) (Terminal, protocol.TerminalResponse, error) {
	panic("not reached")
}

// admitAll is an authorizer that hands every request the same backend.
func admitAll(b Backend) Authorizer {
	return func(*http.Request, bool) (Backend, *Refusal) { return b, nil }
}

func post(g *Gateway, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

func TestTerminalImageAPIHasDedicatedBodyLimit(t *testing.T) {
	backend := &stubBackend{}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	ordinary := `{"padding":"` + strings.Repeat("x", MaxRequestBody) + `"}`
	if rec := post(g, "/api/v1/run.list", ordinary); rec.Code != http.StatusBadRequest {
		t.Fatalf("ordinary oversized body = %d, want 400", rec.Code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("ordinary oversized body reached backend: %+v", backend.calls)
	}

	imageBody := `{"content":"` + strings.Repeat("A", MaxRequestBody+1) + `"}`
	rec := post(g, "/api/v1/terminal.image", imageBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("terminal.image body over ordinary cap = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(backend.calls) != 1 || backend.calls[0] != protocol.MethodTerminalImage {
		t.Fatalf("terminal.image calls = %+v", backend.calls)
	}

	overImageCap := `{"content":"` + strings.Repeat("A", maxTerminalImageRequestBody) + `"}`
	if rec := post(g, "/api/v1/terminal.image", overImageCap); rec.Code != http.StatusBadRequest {
		t.Fatalf("terminal.image body over image cap = %d, want 400", rec.Code)
	}
	if len(backend.calls) != 1 {
		t.Fatalf("over-cap image reached backend: %+v", backend.calls)
	}
}

func TestFileWriteAPIAcceptsLargeEditorBodies(t *testing.T) {
	backend := &stubBackend{}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	body := `{"content":"` + strings.Repeat("x", 5<<20) + `"}`
	if rec := post(g, "/api/v1/config.write", body); rec.Code != http.StatusOK {
		t.Fatalf("large config.write body = %d: %s", rec.Code, rec.Body)
	}
	if len(backend.calls) != 1 || backend.calls[0] != protocol.MethodConfigWrite {
		t.Fatalf("large config.write calls = %+v", backend.calls)
	}
}

func TestConfigImportAPIBoundsEncodedRequest(t *testing.T) {
	calls := make(chan string, 2)
	backend := &importTestBackend{call: func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, *protocol.Error) {
		calls <- method
		return nil, nil
	}}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	server := httptest.NewServer(g)
	defer server.Close()
	send := func(body io.Reader) int {
		t.Helper()
		res, err := server.Client().Post(server.URL+"/api/v1/config.import", "application/json", body)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if _, err := io.Copy(io.Discard, res.Body); err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	// Generate the base64 form of one maximum-size file. Keeping a giant
	// source string as well as the handler's body buffer is unnecessary.
	encodedBytes := base64.StdEncoding.EncodedLen(memberhome.ConfigMaxFileBytes)
	body := io.MultiReader(
		strings.NewReader(`{"harness":"claude","files":[{"path":"plugin.bin","content_base64":"`),
		io.LimitReader(importBodyBytes{}, int64(encodedBytes-2)),
		strings.NewReader(`==","mode":420}]}`),
	)
	if status := send(body); status != http.StatusOK {
		t.Fatalf("maximum decoded import body = %d", status)
	}
	if method := <-calls; method != protocol.MethodConfigImport {
		t.Fatalf("maximum decoded import method = %s", method)
	}
	overLimit := io.MultiReader(
		strings.NewReader(`{"padding":"`),
		io.LimitReader(importBodyBytes{}, maxConfigImportRequestBody),
		strings.NewReader(`"}`),
	)
	if status := send(overLimit); status != http.StatusBadRequest {
		t.Fatalf("over-limit import body = %d", status)
	}
	select {
	case method := <-calls:
		t.Fatalf("over-limit import reached backend: %s", method)
	default:
	}
}

type importBodyBytes struct{}

func (importBodyBytes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

type importTestBackend struct {
	stubBackend
	call func(context.Context, string, json.RawMessage) (json.RawMessage, *protocol.Error)
}

func (b *importTestBackend) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	if b.call != nil {
		return b.call(ctx, method, params)
	}
	return nil, nil
}

func importTestServer(t *testing.T, backend Backend, idle, total time.Duration) *httptest.Server {
	t.Helper()
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	g.configImportIdle = idle
	g.configImportDuration = total
	server := httptest.NewServer(g)
	t.Cleanup(func() {
		server.Close()
		_ = g.Close()
	})
	return server
}

func importTestConn(t *testing.T, server *httptest.Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}

func importTestResponse(t *testing.T, reader *bufio.Reader, want int) {
	t.Helper()
	res, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("status = %d, want %d: %s", res.StatusCode, want, body)
	}
}

func importTestPost(t *testing.T, server *httptest.Server, method, body string) int {
	t.Helper()
	res, err := server.Client().Post(server.URL+"/api/v1/"+method, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if _, err := io.Copy(io.Discard, res.Body); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode
}

func TestConfigImportAdmissionCoversBodyAndBackend(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	defer close(release)
	backend := &importTestBackend{call: func(ctx context.Context, method string, _ json.RawMessage) (json.RawMessage, *protocol.Error) {
		if method == protocol.MethodConfigImport {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return nil, nil
	}}
	server := importTestServer(t, backend, time.Second, time.Minute)
	conns := make([]net.Conn, 2)
	readers := make([]*bufio.Reader, 2)
	for i := range conns {
		conns[i], readers[i] = importTestConn(t, server)
		_, err := fmt.Fprint(conns[i], "POST /api/v1/config.import HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 2\r\nExpect: 100-continue\r\n\r\n")
		if err != nil {
			t.Fatal(err)
		}
		// 100 Continue proves the handler has admitted the body and
		// entered a real blocked read, without filling a huge buffer.
		importTestResponse(t, readers[i], http.StatusContinue)
	}
	reject := func() {
		t.Helper()
		conn, reader := importTestConn(t, server)
		defer func() { _ = conn.Close() }()
		if _, err := fmt.Fprint(conn, "POST /api/v1/config.import HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 1000000\r\nExpect: 100-continue\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		// A 100 response here would mean the rejected body was read.
		importTestResponse(t, reader, http.StatusServiceUnavailable)
	}
	reject()
	if status := importTestPost(t, server, "run.list", "{}"); status != http.StatusOK {
		t.Fatalf("ordinary API while imports blocked = %d", status)
	}
	for i, conn := range conns {
		if _, err := io.WriteString(conn, "{}"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("import %d did not reach backend", i)
		}
	}
	reject()
	for range conns {
		release <- struct{}{}
	}
	for _, reader := range readers {
		importTestResponse(t, reader, http.StatusOK)
	}
	// A third admitted request demonstrates release after completion.
	release <- struct{}{}
	if status := importTestPost(t, server, "config.import", "{}"); status != http.StatusOK {
		t.Fatalf("import after completion = %d", status)
	}
}

func TestConfigImportBodyDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name    string
		idle    time.Duration
		total   time.Duration
		trickle bool
	}{
		{name: "idle", idle: 80 * time.Millisecond, total: time.Second},
		{name: "whole body", idle: time.Second, total: 150 * time.Millisecond, trickle: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := importTestServer(t, &importTestBackend{}, tc.idle, tc.total)
			conns := make([]net.Conn, 2)
			readers := make([]*bufio.Reader, 2)
			for i := range conns {
				conns[i], readers[i] = importTestConn(t, server)
				if _, err := fmt.Fprint(conns[i], "POST /api/v1/config.import HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 1000000\r\nExpect: 100-continue\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				importTestResponse(t, readers[i], http.StatusContinue)
			}
			if tc.trickle {
				for range 8 {
					for _, conn := range conns {
						_, _ = io.WriteString(conn, " ")
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
			for _, reader := range readers {
				importTestResponse(t, reader, http.StatusRequestTimeout)
			}
			if status := importTestPost(t, server, "config.import", "{}"); status != http.StatusOK {
				t.Fatalf("import after timed-out bodies = %d", status)
			}
		})
	}
}

func TestConfigImportProgressAndKeepalive(t *testing.T) {
	idle := 200 * time.Millisecond
	server := importTestServer(t, &importTestBackend{}, idle, 3*time.Second)
	conn, reader := importTestConn(t, server)
	if _, err := fmt.Fprint(conn, "POST /api/v1/config.import HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 8\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// Total transfer exceeds idle, but each byte makes timely progress.
	for _, b := range []byte("      {}") {
		if _, err := conn.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	importTestResponse(t, reader, http.StatusOK)
	// The final body's read deadline must not leak into a later request
	// on the same TCP connection.
	time.Sleep(2 * idle)
	if _, err := fmt.Fprint(conn, "POST /api/v1/run.list HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"); err != nil {
		t.Fatal(err)
	}
	importTestResponse(t, reader, http.StatusOK)
}

func TestConfigImportAdmissionReleasedOnErrorAndDisconnect(t *testing.T) {
	backend := &importTestBackend{call: func(_ context.Context, _ string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
		if string(params) == `{"fail":true}` {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: "import denied"}
		}
		return nil, nil
	}}
	server := importTestServer(t, backend, time.Second, time.Minute)
	for range 3 {
		if status := importTestPost(t, server, "config.import", "{"); status != http.StatusBadRequest {
			t.Fatalf("malformed import = %d", status)
		}
		if status := importTestPost(t, server, "config.import", `{"fail":true}`); status != http.StatusForbidden {
			t.Fatalf("backend-denied import = %d", status)
		}
	}
	for range 2 {
		conn, reader := importTestConn(t, server)
		if _, err := fmt.Fprint(conn, "POST /api/v1/config.import HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 1000000\r\nExpect: 100-continue\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		importTestResponse(t, reader, http.StatusContinue)
		_ = conn.Close()
	}
	until := time.Now().Add(3 * time.Second)
	for {
		status := importTestPost(t, server, "config.import", "{}")
		if status == http.StatusOK {
			break
		}
		if status != http.StatusServiceUnavailable || time.Now().After(until) {
			t.Fatalf("import after disconnect = %d", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConfigImportCancellationInterruptsRead(t *testing.T) {
	g, err := New(Config{Authorize: admitAll(&importTestBackend{})})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	cancels := make(chan context.CancelFunc, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		cancels <- cancel
		g.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	for range 3 {
		conn, reader := importTestConn(t, server)
		if _, err := fmt.Fprint(conn, "POST /api/v1/config.import HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 1000000\r\nExpect: 100-continue\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		importTestResponse(t, reader, http.StatusContinue)
		// Cancel the request without disconnecting the peer. Its socket
		// read must wake now, not retain admission until the idle timeout.
		cancel := <-cancels
		cancel()
		importTestResponse(t, reader, http.StatusRequestTimeout)
		_ = conn.Close()
	}
}

func TestConfigImportRequiresReadDeadlineSupport(t *testing.T) {
	backend := &stubBackend{}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	for range 3 {
		// ResponseRecorder cannot enforce socket deadlines: fail closed,
		// rather than silently disabling protection in this environment.
		if rec := post(g, "/api/v1/config.import", "{}"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("unsupported deadline writer = %d", rec.Code)
		}
	}
	if len(backend.calls) != 0 {
		t.Fatalf("unsupported writer reached backend: %v", backend.calls)
	}
}

func TestCapabilitiesFillTheSharedFields(t *testing.T) {
	g, err := New(Config{
		Authorize:    admitAll(&stubBackend{}),
		Capabilities: protocol.GatewayCapabilities{Gateway: "server", WS: []string{"events"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities = %d: %s", rec.Code, rec.Body)
	}
	var caps protocol.GatewayCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Gateway != "server" || len(caps.Methods) != 1 || caps.Methods[0] != "*" || caps.Local != nil {
		t.Fatalf("capabilities = %+v", caps)
	}
	if !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("capabilities carry no version: %s", rec.Body)
	}
}

// A cross-site page can post without a CORS preflight only with a simple
// content type, and it always carries its own Origin; both are refused
// before the backend is reached, so a browser's ambient credential on the
// server gateway cannot be borrowed.
func TestAPIRefusesCrossSiteRequests(t *testing.T) {
	backend := &stubBackend{}
	g, err := New(Config{Authorize: admitAll(backend)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()

	plain := httptest.NewRequest(http.MethodPost, "/api/v1/run.kill", strings.NewReader(`{"run_id":"run_1"}`))
	plain.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, plain)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain POST = %d, want 415: %s", rec.Code, rec.Body)
	}

	foreign := httptest.NewRequest(http.MethodPost, "/api/v1/run.kill", strings.NewReader(`{"run_id":"run_1"}`))
	foreign.Header.Set("Content-Type", "application/json")
	foreign.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, foreign)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Origin POST = %d, want 403: %s", rec.Code, rec.Body)
	}
	var body ErrorBody
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error == nil || body.Error.Code != protocol.CodeDenied {
		t.Fatalf("foreign Origin body = %s", rec.Body)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("cross-site requests reached the backend: %v", backend.calls)
	}

	own := httptest.NewRequest(http.MethodPost, "/api/v1/run.list", strings.NewReader(`{}`))
	own.Header.Set("Content-Type", "application/json; charset=utf-8")
	own.Header.Set("Origin", "http://"+own.Host)
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, own)
	if rec.Code != http.StatusOK || len(backend.calls) != 1 {
		t.Fatalf("same-origin JSON POST = %d (calls %v), want 200", rec.Code, backend.calls)
	}
}

func TestLocalVerbsAreNotFoundWithoutTheLocalGateway(t *testing.T) {
	g, err := New(Config{Authorize: admitAll(&stubBackend{})})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	if rec := post(g, "/local/v1/link.status", "{}"); rec.Code != http.StatusNotFound {
		t.Fatalf("/local/v1 on a gateway without local verbs = %d, want 404", rec.Code)
	}
}
