package localgw

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// apiStubBackend fakes the linked server for handler tests: canned call
// results keyed by method, recording what was asked. The streaming
// surfaces are never reached by the HTTP handlers under test.
type apiStubBackend struct {
	calls   []stubCall
	results map[string]json.RawMessage
	errs    map[string]*protocol.Error
}

type stubCall struct {
	method string
	params string
}

func (b *apiStubBackend) Call(_ context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	b.calls = append(b.calls, stubCall{method: method, params: string(params)})
	if perr, ok := b.errs[method]; ok {
		return nil, perr
	}
	return b.results[method], nil
}

func (b *apiStubBackend) Events(protocol.SubscribeRequest) (io.ReadWriteCloser, error) {
	panic("not reached")
}

func (b *apiStubBackend) Attach(protocol.AttachRequest) (cli.Terminal, protocol.AttachResponse, error) {
	panic("not reached")
}

func (b *apiStubBackend) Sync(string, bool) (io.ReadWriteCloser, error) {
	panic("not reached")
}
func (b *apiStubBackend) Close() error { return nil }

type closeBackend struct {
	apiStubBackend
	closed chan struct{}
}

func (b *closeBackend) Close() error {
	close(b.closed)
	return nil
}

func TestGatewayCloseClosesBackend(t *testing.T) {
	backend := &closeBackend{
		apiStubBackend: apiStubBackend{results: map[string]json.RawMessage{}},
		closed:         make(chan struct{}),
	}
	g := newTestGateway(t, backend)
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-backend.closed:
	default:
		t.Fatal("gateway close did not close backend")
	}
}

// newTestGateway builds a gateway around the stub without binding a port;
// requests go straight at the mux.
func newTestGateway(t *testing.T, backend Backend) *Gateway {
	t.Helper()
	g, err := New(Config{Backend: backend})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

// do drives one request through the mux, adding the gateway token as a
// Bearer header unless the raw flag says to leave it off.
func do(g *Gateway, method, path, body string, withToken bool) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if withToken {
		req.Header.Set("Authorization", "Bearer "+g.Token())
	}
	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, body []byte) *protocol.Error {
	t.Helper()
	var out webgate.ErrorBody
	if err := json.Unmarshal(body, &out); err != nil || out.Error == nil {
		t.Fatalf("not an error body: %s", body)
	}
	return out.Error
}

func TestAPITokenRequired(t *testing.T) {
	backend := &apiStubBackend{results: map[string]json.RawMessage{"run.list": json.RawMessage(`{"runs":[]}`)}}
	g := newTestGateway(t, backend)

	rec := do(g, http.MethodPost, "/api/v1/run.list", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeDenied {
		t.Errorf("error code = %d, want CodeDenied", perr.Code)
	}
	if len(backend.calls) != 0 {
		t.Errorf("backend called %d times without a token", len(backend.calls))
	}

	// The API surface takes the token as a Bearer header only; the query
	// form is reserved for WebSocket handshakes and the initial tab.
	rec = do(g, http.MethodPost, "/api/v1/run.list?token="+g.Token(), "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("query token on /api = %d, want 401", rec.Code)
	}

	rec = do(g, http.MethodPost, "/api/v1/run.list", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("with token = %d, want 200: %s", rec.Code, rec.Body.Bytes())
	}
}

func TestAPIRoundTrip(t *testing.T) {
	backend := &apiStubBackend{results: map[string]json.RawMessage{
		"run.get": json.RawMessage(`{"run":{"id":"r1","state":"running"}}`),
	}}
	g := newTestGateway(t, backend)

	rec := do(g, http.MethodPost, "/api/v1/run.get", `{"run_id":"r1"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.Bytes())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	if got := rec.Body.String(); got != `{"run":{"id":"r1","state":"running"}}` {
		t.Errorf("body = %q, want the backend result verbatim", got)
	}
	want := []stubCall{{method: "run.get", params: `{"run_id":"r1"}`}}
	if !reflect.DeepEqual(backend.calls, want) {
		t.Errorf("calls = %+v, want %+v", backend.calls, want)
	}
}

func TestAPIEmptyBodyAndResult(t *testing.T) {
	backend := &apiStubBackend{}
	g := newTestGateway(t, backend)

	rec := do(g, http.MethodPost, "/api/v1/server.info", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// An empty result still answers a JSON object so clients always decode.
	if got := rec.Body.String(); got != "{}" {
		t.Errorf("empty result body = %q, want {}", got)
	}
	if len(backend.calls) != 1 || backend.calls[0].params != "" {
		t.Errorf("calls = %+v, want one call with nil params", backend.calls)
	}
}

func TestAPIInvalidJSON(t *testing.T) {
	backend := &apiStubBackend{}
	g := newTestGateway(t, backend)

	rec := do(g, http.MethodPost, "/api/v1/run.get", `{"run_id":`, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d, want 400", rec.Code)
	}
	if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeParse {
		t.Errorf("error code = %d, want CodeParse", perr.Code)
	}
	if len(backend.calls) != 0 {
		t.Errorf("backend called on invalid JSON")
	}
}

func TestAPIErrorMapping(t *testing.T) {
	cases := []struct {
		code int
		want int
	}{
		{protocol.CodeNotFound, http.StatusNotFound},
		{protocol.CodeDenied, http.StatusForbidden},
		{protocol.CodeInvalidParams, http.StatusBadRequest},
		{protocol.CodeInvalidState, http.StatusConflict},
		{protocol.CodeUnavailable, http.StatusServiceUnavailable},
		{protocol.CodeInternal, http.StatusInternalServerError},
	}
	for _, c := range cases {
		backend := &apiStubBackend{errs: map[string]*protocol.Error{
			"run.get": {Code: c.code, Message: "nope"},
		}}
		g := newTestGateway(t, backend)
		rec := do(g, http.MethodPost, "/api/v1/run.get", `{"run_id":"r1"}`, true)
		if rec.Code != c.want {
			t.Errorf("code %d = HTTP %d, want %d", c.code, rec.Code, c.want)
			continue
		}
		perr := decodeError(t, rec.Body.Bytes())
		if perr.Code != c.code || perr.Message != "nope" {
			t.Errorf("code %d error = %+v, want it echoed", c.code, perr)
		}
	}
}

func TestPatchProxies(t *testing.T) {
	patch := `{"run_id":"r1","base":"abc","patch":"diff --git","truncated":false}`
	backend := &apiStubBackend{results: map[string]json.RawMessage{
		protocol.MethodRunPatch: json.RawMessage(patch),
	}}
	g := newTestGateway(t, backend)

	rec := do(g, http.MethodGet, "/api/v1/run/r1/patch", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.Bytes())
	}
	if got := rec.Body.String(); got != patch {
		t.Errorf("body = %q, want the run.patch result verbatim", got)
	}
	want := []stubCall{{method: protocol.MethodRunPatch, params: `{"run_id":"r1"}`}}
	if !reflect.DeepEqual(backend.calls, want) {
		t.Errorf("calls = %+v, want %+v", backend.calls, want)
	}

	if rec := do(g, http.MethodGet, "/api/v1/run/r1/patch", "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
}

func TestDiskProxies(t *testing.T) {
	disk := `{"used_bytes":1,"total_bytes":2,"free_bytes":1,"worktree_bytes":0,"transcript_bytes":0,"database_bytes":0}`
	backend := &apiStubBackend{results: map[string]json.RawMessage{
		protocol.MethodServerDisk: json.RawMessage(disk),
	}}
	g := newTestGateway(t, backend)

	rec := do(g, http.MethodGet, "/api/v1/disk", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.Bytes())
	}
	if got := rec.Body.String(); got != disk {
		t.Errorf("body = %q, want the server.disk result verbatim", got)
	}
	want := []stubCall{{method: protocol.MethodServerDisk, params: ""}}
	if !reflect.DeepEqual(backend.calls, want) {
		t.Errorf("calls = %+v, want %+v", backend.calls, want)
	}

	if rec := do(g, http.MethodGet, "/api/v1/disk", "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
}

func TestCapabilities(t *testing.T) {
	g := newTestGateway(t, &apiStubBackend{})

	rec := do(g, http.MethodGet, "/api/v1/capabilities", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var caps protocol.GatewayCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if caps.Gateway != "local" {
		t.Errorf("gateway = %q, want local", caps.Gateway)
	}
	if !reflect.DeepEqual(caps.Methods, []string{"*"}) {
		t.Errorf("methods = %v, want [*]", caps.Methods)
	}
	if !reflect.DeepEqual(caps.WS, []string{"events", "attach", "envscan"}) {
		t.Errorf("ws = %v, want [events attach envscan]", caps.WS)
	}
	if !reflect.DeepEqual(caps.Local, localVerbs) {
		t.Errorf("local = %v, want %v", caps.Local, localVerbs)
	}

	if rec := do(g, http.MethodGet, "/api/v1/capabilities", "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
}

func TestMethodNotAllowedGuard(t *testing.T) {
	g := newTestGateway(t, &apiStubBackend{})

	// Paths under /api, /ws, or /local that match no registered pattern
	// fall through to the SPA handler, which must refuse them instead of
	// answering with index.html.
	for _, path := range []string{"/api/", "/api/v2/run.list", "/ws/nope", "/local/", "/local/v2/pull"} {
		rec := do(g, http.MethodGet, path, "", true)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want 405", path, rec.Code)
			continue
		}
		if perr := decodeError(t, rec.Body.Bytes()); perr.Code != protocol.CodeInvalidRequest {
			t.Errorf("%s error code = %d, want CodeInvalidRequest", path, perr.Code)
		}
	}
}

func TestNewRequiresBackend(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without a Backend should fail")
	}
}

func TestTokenShape(t *testing.T) {
	g := newTestGateway(t, &apiStubBackend{})
	tok := g.Token()
	if len(tok) != 43 { // 32 bytes, base64url, no padding
		t.Errorf("token length = %d, want 43", len(tok))
	}
	if strings.ContainsAny(tok, "+/=") {
		t.Errorf("token %q is not raw-URL base64", tok)
	}
	g2 := newTestGateway(t, &apiStubBackend{})
	if g2.Token() == tok {
		t.Error("two gateways minted the same token")
	}
}
