package webgate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	defer g.Close()

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

func TestCapabilitiesFillTheSharedFields(t *testing.T) {
	g, err := New(Config{
		Authorize:    admitAll(&stubBackend{}),
		Capabilities: protocol.GatewayCapabilities{Gateway: "server", WS: []string{"events"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
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
