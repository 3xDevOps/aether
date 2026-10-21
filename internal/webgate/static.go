package webgate

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hashedAssets is where Next's static export writes the files whose names
// change with their content.
const hashedAssets = "_next/static/"

type staticFile struct {
	load  sync.Once
	err   error
	body  []byte
	ctype string
	hash  string

	compress sync.Once
	// Nil when gzip does not shrink the file by a tenth, as for fonts and
	// images that are compressed already.
	gzipped []byte
}

// StaticHandler serves the built SPA, falling back to index.html so a hard
// refresh on a client-side route still loads the app. A binary built
// without an SPA answers a plain "dashboard not built" instead of a blank
// page. Each file is read, hashed and gzipped once, on its first request.
func StaticHandler(fsys fs.FS) http.Handler {
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "dashboard not built: this server binary embeds no web/dist", http.StatusServiceUnavailable)
		})
	}
	var mu sync.Mutex
	files := map[string]*staticFile{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if info, err := fs.Stat(fsys, name); err != nil || info.IsDir() {
			name = "index.html"
		}
		mu.Lock()
		f := files[name]
		if f == nil {
			f = &staticFile{}
			files[name] = f
		}
		mu.Unlock()
		if err := f.read(fsys, name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", f.ctype)
		if strings.HasPrefix(name, hashedAssets) {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// Revalidated on every load, so a server update shows on the next one.
			h.Set("Cache-Control", "no-cache")
		}
		body, etag := f.body, f.hash
		// The local gateway's only client is on loopback, where gzip costs CPU
		// on every launch and saves no transfer time.
		if !fromLoopback(r) {
			if gzipped := f.gzip(); gzipped != nil {
				h.Set("Vary", "Accept-Encoding")
				if acceptsGzip(r) {
					h.Set("Content-Encoding", "gzip")
					body, etag = gzipped, etag+"-gzip"
				}
			}
		}
		h.Set("ETag", `"`+etag+`"`)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
}

func (f *staticFile) read(fsys fs.FS, name string) error {
	f.load.Do(func() {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			f.err = fmt.Errorf("dashboard file %s: %w", name, err)
			return
		}
		f.body = body
		sum := sha256.Sum256(body)
		f.hash = fmt.Sprintf("%x", sum[:12])
		f.ctype = mime.TypeByExtension(path.Ext(name))
		// Go's built-in table has no .webmanifest entry, and whether any of
		// the system files it also reads supplies one varies by host;
		// without this the web app manifest is sniffed as plain text on
		// some machines and not others.
		if path.Ext(name) == ".webmanifest" {
			f.ctype = "application/manifest+json"
		}
		if f.ctype == "" {
			f.ctype = http.DetectContentType(body)
		}
	})
	return f.err
}

func (f *staticFile) gzip() []byte {
	f.compress.Do(func() {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		// Writes to a bytes.Buffer cannot fail.
		_, _ = zw.Write(f.body)
		_ = zw.Close()
		if buf.Len() < len(f.body)-len(f.body)/10 {
			f.gzipped = buf.Bytes()
		}
	})
	return f.gzipped
}

func fromLoopback(r *http.Request) bool {
	addr, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && addr.Addr().Unmap().IsLoopback()
}

func acceptsGzip(r *http.Request) bool {
	for _, header := range r.Header.Values("Accept-Encoding") {
		for _, part := range strings.Split(header, ",") {
			coding, params, _ := strings.Cut(part, ";")
			if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
				continue
			}
			q, weighted := strings.CutPrefix(strings.TrimSpace(params), "q=")
			if !weighted {
				return true
			}
			weight, err := strconv.ParseFloat(q, 64)
			return err == nil && weight > 0
		}
	}
	return false
}
