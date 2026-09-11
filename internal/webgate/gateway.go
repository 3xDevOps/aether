package webgate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/web"
)

// Config wires a gateway to the identity it trusts and the static assets
// it serves.
type Config struct {
	// Authorize identifies every /api and /ws caller. Required.
	Authorize Authorizer
	// Capabilities is what GET /api/v1/capabilities answers, minus the
	// fields every gateway shares: Methods is always "*", and Version and
	// Commit are this build's.
	Capabilities protocol.GatewayCapabilities
	// Static is the built SPA; nil means the embedded web/dist.
	Static fs.FS
}

// Gateway is the transport-neutral dashboard gateway: the SPA, the
// /api/v1 shape, and the events, attach and terminal WebSockets, each
// bridged onto whatever Backend the authorizer hands back. It is an
// http.Handler; the composer owns the listener and adds its own routes.
type Gateway struct {
	*http.ServeMux
	cfg Config

	// ctx bounds every WebSocket handler, which http.Server.Shutdown
	// cannot reach once the connection is hijacked; Close cancels it and
	// waits on wg for those handlers to return.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	conns   map[*websocket.Conn]struct{}
	closing bool
}

// New builds the gateway. It binds nothing: the caller serves it.
func New(cfg Config) (*Gateway, error) {
	if cfg.Authorize == nil {
		return nil, errors.New("webgate: config requires an Authorizer")
	}
	if cfg.Static == nil {
		sub, err := fs.Sub(web.Dist, "dist")
		if err != nil {
			return nil, fmt.Errorf("webgate: embedded spa: %w", err)
		}
		cfg.Static = sub
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{
		ServeMux: http.NewServeMux(),
		cfg:      cfg,
		ctx:      ctx,
		cancel:   cancel,
		conns:    make(map[*websocket.Conn]struct{}),
	}
	g.HandleFunc("POST /api/v1/{method}", g.handleAPI)
	g.HandleFunc("GET /api/v1/run/{run}/patch", g.handlePatch)
	g.HandleFunc("GET /api/v1/disk", g.handleDisk)
	g.HandleFunc("GET /api/v1/capabilities", g.handleCapabilities)
	g.HandleFunc("GET /ws/events", g.handleEvents)
	g.HandleFunc("GET /ws/attach/{run}", g.handleAttach)
	g.HandleFunc("GET /ws/terminal", g.handleTerminal)
	static := StaticHandler(cfg.Static)
	g.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An /api, /ws, or /local request that misses every
		// method-qualified pattern lands here; answering it with the SPA
		// would turn a wrong-verb client bug into a silent 200.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") || strings.HasPrefix(r.URL.Path, "/local/") {
			WriteError(w, http.StatusMethodNotAllowed, &protocol.Error{
				Code:    protocol.CodeInvalidRequest,
				Message: "method not allowed",
			})
			return
		}
		static.ServeHTTP(w, r)
	}))
	return g, nil
}

// authorize runs the authorizer and writes its refusal, reporting whether
// the request may proceed.
func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request, handshake bool) (Backend, bool) {
	backend, refusal := g.cfg.Authorize(r, handshake)
	if refusal != nil {
		refusal.write(w)
		return nil, false
	}
	return backend, true
}

// beginHandler registers a WebSocket handler with the shutdown WaitGroup
// unless the gateway is closing, so an Add can never race the Wait in
// Close.
func (g *Gateway) beginHandler(conn *websocket.Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.wg.Add(1)
	g.conns[conn] = struct{}{}
	return true
}

func (g *Gateway) endHandler(conn *websocket.Conn) {
	g.mu.Lock()
	delete(g.conns, conn)
	g.mu.Unlock()
	g.wg.Done()
}

// Close ends every live WebSocket and waits for its handler to return.
// The composer shuts its http.Server down separately; that covers the
// ordinary requests, this covers the hijacked ones. Safe to call twice.
func (g *Gateway) Close() {
	g.mu.Lock()
	g.closing = true
	conns := make([]*websocket.Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()
	g.cancel()
	for _, c := range conns {
		_ = c.CloseNow()
	}
	g.wg.Wait()
}
