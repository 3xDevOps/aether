package webgate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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

type Config struct {
	// Required.
	Authorize Authorizer
	// Methods, Version and Commit are filled in by the gateway.
	Capabilities protocol.GatewayCapabilities
	// Static is the built SPA; nil means the embedded web/dist.
	Static fs.FS
	// Bound how long a half-open socket (a phone that slept or changed
	// networks) keeps its PTY client. Zero means the defaults.
	PingInterval time.Duration
	PingTimeout  time.Duration
}

const (
	httpReadHeaderTimeout = 10 * time.Second
	closeTimeout          = 5 * time.Second
)

// Gateway is the transport-neutral dashboard gateway, bridged onto whatever
// Backend the authorizer hands back. Composers add their own routes to it.
type Gateway struct {
	*http.ServeMux
	cfg Config
	srv *http.Server

	// Admission covers an import's whole lifetime, including the response,
	// because it retains its wire body until then.
	configImports        chan struct{}
	configImportIdle     time.Duration
	configImportDuration time.Duration

	// Bounds every WebSocket handler, which http.Server.Shutdown cannot
	// reach once hijacked.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	conns   map[*websocket.Conn]struct{}
	closing bool
	serving int
	failed  error
	done    chan struct{}
}

// New binds nothing: the caller serves it.
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
	if cfg.PingInterval == 0 {
		cfg.PingInterval = defaultPingInterval
	}
	if cfg.PingTimeout == 0 {
		cfg.PingTimeout = defaultPingTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{
		ServeMux:             http.NewServeMux(),
		cfg:                  cfg,
		ctx:                  ctx,
		cancel:               cancel,
		conns:                make(map[*websocket.Conn]struct{}),
		done:                 make(chan struct{}),
		configImports:        make(chan struct{}, 2),
		configImportIdle:     30 * time.Second,
		configImportDuration: 15 * time.Minute,
	}
	g.HandleFunc("POST /api/v1/{method}", g.handleAPI)
	g.HandleFunc("GET /api/v1/run/{run}/patch", g.handlePatch)
	g.HandleFunc("GET /api/runs/{run}/terminal-history", g.handleHistory)
	g.HandleFunc("POST /api/runs/{run}/terminal-history", g.handleHistoryForm)
	g.HandleFunc("GET /api/v1/disk", g.handleDisk)
	g.HandleFunc("GET /api/v1/capabilities", g.handleCapabilities)
	g.HandleFunc("GET /ws/events", g.handleEvents)
	g.HandleFunc("GET /ws/attach/{run}", g.handleAttach)
	g.HandleFunc("GET /ws/acp/{run}", g.handleACP)
	g.HandleFunc("GET /ws/terminal", g.handleTerminal)
	g.HandleFunc("GET /ws/dev/browser/{run}", g.handleDevelopmentBrowser)
	g.HandleFunc("GET /api/v1/dev/{run}/artifacts/{artifact}", g.handleDevelopmentArtifact)
	static := StaticHandler(cfg.Static)
	g.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serving the SPA here would turn a wrong-verb client bug into a
		// silent 200.
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

// Authorize must guard every route, including those a composer adds. The
// server gateway has no bearer token - tailnet position is the whole
// credential - so without the same-origin check any other page could act
// as the member with a plain fetch.
func (g *Gateway) Authorize(w http.ResponseWriter, r *http.Request, handshake bool) (Backend, bool) {
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

// sameOrigin is the same rule coder/websocket applies to a handshake.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

func (g *Gateway) Serve(ln net.Listener) {
	g.mu.Lock()
	g.serving++
	g.mu.Unlock()
	go func() {
		err := g.srv.Serve(ln)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.serving--
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("webgate: listener stopped serving", "addr", ln.Addr(), "error", err)
			if g.failed == nil {
				g.failed = fmt.Errorf("webgate: listener %s stopped serving: %w", ln.Addr(), err)
			}
		}
		if g.serving == 0 && g.failed != nil && !g.closing {
			close(g.done)
		}
	}()
}

// Done is closed when every listener has died with an error. It stays open
// through Close.
func (g *Gateway) Done() <-chan struct{} { return g.done }

// Err is valid once Done is closed.
func (g *Gateway) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failed
}

// beginHandler refuses once closing so wg.Add can never race the Wait in
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

// Close is safe before Serve and safe to call twice.
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
