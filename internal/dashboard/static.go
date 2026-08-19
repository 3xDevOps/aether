package dashboard

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// staticHandler serves the built SPA, falling back to index.html so a hard
// refresh on a client-side route still loads the app. A binary built
// without an SPA answers a plain "dashboard not built" instead of a blank
// page.
func staticHandler(fsys fs.FS) http.Handler {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "dashboard not built: this server binary embeds no web/dist", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServerFS(fsys)
	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if name == "." || name == "/" {
			serveIndex(w)
			return
		}
		if _, err := fs.Stat(fsys, name); err != nil {
			serveIndex(w)
			return
		}
		files.ServeHTTP(w, r)
	})
}
