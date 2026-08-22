package webgate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestStatusFor(t *testing.T) {
	cases := []struct {
		code int
		want int
	}{
		{protocol.CodeParse, http.StatusBadRequest},
		{protocol.CodeInvalidRequest, http.StatusBadRequest},
		{protocol.CodeInvalidParams, http.StatusBadRequest},
		{protocol.CodeDenied, http.StatusForbidden},
		{protocol.CodeMethodNotFound, http.StatusNotFound},
		{protocol.CodeNotFound, http.StatusNotFound},
		{protocol.CodeInvalidState, http.StatusConflict},
		{protocol.CodeConflict, http.StatusConflict},
		{protocol.CodeUnavailable, http.StatusServiceUnavailable},
		{protocol.CodeInternal, http.StatusInternalServerError},
		{0, http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := StatusFor(c.code); got != c.want {
			t.Errorf("StatusFor(%d) = %d, want %d", c.code, got, c.want)
		}
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusConflict, &protocol.Error{Code: protocol.CodeConflict, Message: "busy"})
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var out ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Error == nil {
		t.Fatalf("not an error body: %s", rec.Body.Bytes())
	}
	if out.Error.Code != protocol.CodeConflict || out.Error.Message != "busy" {
		t.Errorf("error = %+v", out.Error)
	}
}

func TestStaticHandlerFallback(t *testing.T) {
	spa := fstest.MapFS{
		"index.html":     {Data: []byte("<!doctype html>spa")},
		"assets/app.js":  {Data: []byte("console.log(1)")},
		"assets/app.css": {Data: []byte("body{}")},
	}
	h := StaticHandler(spa)
	for path, want := range map[string]string{
		"/":              "<!doctype html>spa",
		"/runs/abc":      "<!doctype html>spa",
		"/assets/app.js": "console.log(1)",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
			continue
		}
		if got := rec.Body.String(); got != want {
			t.Errorf("%s body = %q, want %q", path, got, want)
		}
	}
}

func TestStaticHandlerNotBuilt(t *testing.T) {
	rec := httptest.NewRecorder()
	StaticHandler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unbuilt dashboard = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "dashboard not built") {
		t.Errorf("body = %q, want the not-built notice", rec.Body.String())
	}
}
