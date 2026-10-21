package webgate

import (
	"compress/gzip"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticHandlerCompressesAndCaches(t *testing.T) {
	page := "<!doctype html>" + strings.Repeat("<div>aether</div>", 100)
	script := strings.Repeat("console.log('aether');\n", 200)
	font := make([]byte, 4096)
	_, _ = rand.NewChaCha8([32]byte{}).Read(font)
	h := StaticHandler(fstest.MapFS{
		"index.html":                           {Data: []byte(page)},
		"_next/static/chunks/0yk4b_qdfml3g.js": {Data: []byte(script)},
		"fonts/mono.woff2":                     {Data: font},
	})
	const (
		immutable = "public, max-age=31536000, immutable"
		remote    = "192.0.2.1:1234"
		loopback  = "127.0.0.1:1234"
	)
	get := func(target, from, accept, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = from
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, c := range []struct {
		name, path, from, accept string
		body, encoding, cache    string
		vary                     bool
	}{
		{"hashed asset", "/_next/static/chunks/0yk4b_qdfml3g.js", remote, "gzip, deflate, br", script, "gzip", immutable, true},
		{"hashed asset to a client without gzip", "/_next/static/chunks/0yk4b_qdfml3g.js", remote, "", script, "", immutable, true},
		{"hashed asset to a client refusing gzip", "/_next/static/chunks/0yk4b_qdfml3g.js", remote, "br, gzip;q=0", script, "", immutable, true},
		{"hashed asset on loopback", "/_next/static/chunks/0yk4b_qdfml3g.js", loopback, "gzip", script, "", immutable, false},
		{"entry point", "/", remote, "gzip", page, "gzip", "no-cache", true},
		{"client route", "/runs/abc", remote, "gzip", page, "gzip", "no-cache", true},
		{"missing hashed asset", "/_next/static/chunks/gone.js", remote, "gzip", page, "gzip", "no-cache", true},
		{"incompressible file", "/fonts/mono.woff2", remote, "gzip", string(font), "", "no-cache", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := get(c.path, c.from, c.accept, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != c.encoding {
				t.Errorf("Content-Encoding = %q, want %q", got, c.encoding)
			}
			if got := rec.Header().Get("Cache-Control"); got != c.cache {
				t.Errorf("Cache-Control = %q, want %q", got, c.cache)
			}
			if got := rec.Header().Get("Vary") == "Accept-Encoding"; got != c.vary {
				t.Errorf("Vary = %q, want Accept-Encoding: %v", rec.Header().Get("Vary"), c.vary)
			}
			var body io.Reader = rec.Body
			if c.encoding == "gzip" {
				zr, err := gzip.NewReader(rec.Body)
				if err != nil {
					t.Fatalf("gzip body: %v", err)
				}
				body = zr
			}
			if got, err := io.ReadAll(body); err != nil || string(got) != c.body {
				t.Errorf("body = %.40q (err %v), want %.40q", got, err, c.body)
			}
			etag := rec.Header().Get("ETag")
			if etag == "" {
				t.Fatal("no ETag")
			}
			if c.encoding == "gzip" && etag == get(c.path, c.from, "", "").Header().Get("ETag") {
				t.Errorf("gzip and identity share ETag %s", etag)
			}
			again := get(c.path, c.from, c.accept, etag)
			if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
				t.Errorf("revalidation = %d with %d bytes, want 304 with none", again.Code, again.Body.Len())
			}
		})
	}
}
