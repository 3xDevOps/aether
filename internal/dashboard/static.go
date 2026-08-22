package dashboard

import (
	"io/fs"
	"net/http"

	"github.com/3xDevOps/Aether/internal/webgate"
)

// staticHandler serves the built SPA; the behavior lives in webgate and is
// shared with the local gateway.
func staticHandler(fsys fs.FS) http.Handler {
	return webgate.StaticHandler(fsys)
}
