package webgate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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

const (
	// httpReadHeaderTimeout bounds how long a client may dribble request
	// headers.
	httpReadHeaderTimeout = 10 * time.Second
	// closeTimeout bounds the graceful drain in Close.
	closeTimeout = 5 * time.Second
)

// Gateway is the transport-neutral dashboard gateway: the SPA, the
// /api/v1 shape, and the events, attach and terminal WebSockets, each
// bridged onto whatever Backend the authorizer hands back. It is an
// http.Handler the composer adds its own routes to, and it serves the
// listeners the composer binds.
type Gateway struct {
	*http.ServeMux
	cfg Config
	srv *http.Server

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
		// An /api or /ws request that misses every method-qualified
		// pattern lands here; answering it with the SPA would turn a
		// wrong-verb client bug into a silent 200. The local verbs are the
		// local gateway's to mount; without them the path does not exist.
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/"):
			WriteError(w, http.StatusMethodNotAllowed, &protocol.Error{
				Code:    protocol.CodeInvalidRequest,
				Message: "method not allowed",
			})
		case strings.HasPrefix(r.URL.Path, "/local/"):
			WriteError(w, http.StatusNotFound, &protocol.Error{
				Code:    protocol.CodeMethodNotFound,
				Message: "no local verbs on this gateway",
			})
		default:
			static.ServeHTTP(w, r)
		}
	}))
	g.srv = &http.Server{Handler: g, ReadHeaderTimeout: httpReadHeaderTimeout}
	return g, nil
}

// authorize runs the same-origin rule and then the authorizer, writing
// the refusal and reporting whether the request may proceed.
//
// A browser sends Origin on every cross-site request and on every
// WebSocket handshake. The server gateway has no bearer token - the
// browser's tailnet position is the whole credential - so a page on any
// other origin could otherwise act as the member with a plain fetch: a
// request an Origin names that is not this host is refused before any
// identity is resolved.
func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request, handshake bool) (Backend, bool) {
	if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
		(&Refusal{
			Status: http.StatusForbidden,
			Error:  &protocol.Error{Code: protocol.CodeDenied, Message: "cross-origin request refused"},
		}).Write(w)
		return nil, false
	}
	backend, refusal := g.cfg.Authorize(r, handshake)
	if refusal != nil {
		refusal.Write(w)
		return nil, false
	}
	return backend, true
}

// sameOrigin reports whether an Origin header names host, the same rule
// coder/websocket applies to a handshake.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// Serve serves the gateway on ln in the background until Close.
func (g *Gateway) Serve(ln net.Listener) {
	go func() { _ = g.srv.Serve(ln) }()
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

// Close stops serving, drains in-flight requests briefly before cutting
// them off, then ends every live WebSocket - which http.Server.Shutdown
// cannot reach once hijacked - and waits for its handler to return. Safe
// before Serve, and safe to call twice.
func (g *Gateway) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	err := g.srv.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = g.srv.Close()
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	g.closeSockets()
	return err
}

func (g *Gateway) closeSockets() {
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
