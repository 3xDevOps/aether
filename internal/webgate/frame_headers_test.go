package webgate

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// The dashboard must refuse cross-origin framing: its ambient-position auth
// (loopback token or tailnet identity) makes a framed click act as the member.
func TestStaticHandlerRefusesFraming(t *testing.T) {
	h := StaticHandler(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>spa")}})
	req := httptest.NewRequest(http.MethodGet, "/?new=1&prompt=hi", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Fatalf("Content-Security-Policy = %q, want frame-ancestors 'none'", got)
	}
}
