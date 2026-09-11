package localgw

import (
	"net/http"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestTerminalImageAPIRequiresGatewayToken(t *testing.T) {
	backend := &apiStubBackend{}
	g := newTestGateway(t, backend)
	rec := do(g, http.MethodPost, "/api/v1/terminal.image", `{"content":"aGVsbG8="}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without token = %d, want 401", rec.Code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("backend calls without token = %d, want 0", len(backend.calls))
	}
}

func TestTerminalImageAPIHasDedicatedBodyLimit(t *testing.T) {
	backend := &apiStubBackend{}
	g := newTestGateway(t, backend)

	ordinary := `{"padding":"` + strings.Repeat("x", maxRequestBody) + `"}`
	if rec := do(g, http.MethodPost, "/api/v1/run.list", ordinary, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("ordinary oversized body = %d, want 400", rec.Code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("ordinary oversized body reached backend: %+v", backend.calls)
	}

	imageBody := `{"content":"` + strings.Repeat("A", maxRequestBody+1) + `"}`
	rec := do(g, http.MethodPost, "/api/v1/terminal.image", imageBody, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("terminal.image body over ordinary cap = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(backend.calls) != 1 || backend.calls[0].method != protocol.MethodTerminalImage {
		t.Fatalf("terminal.image calls = %+v", backend.calls)
	}

	overImageCap := `{"content":"` + strings.Repeat("A", maxTerminalImageRequestBody) + `"}`
	if rec := do(g, http.MethodPost, "/api/v1/terminal.image", overImageCap, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("terminal.image body over image cap = %d, want 400", rec.Code)
	}
	if len(backend.calls) != 1 {
		t.Fatalf("over-cap image reached backend: %+v", backend.calls)
	}
}
