// Package dashboard is the web gateway: it serves the embedded SPA and
// bridges browser clients onto the same control-channel handlers, event
// bus, and PTY host the SSH transport uses. One service layer, two
// transports - the HTTP and WebSocket handlers here hold no business
// logic of their own, only framing and the bearer tokens that carry a
// member's SSH-proven identity onto HTTP.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/3xDevOps/Aether/internal/disk"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/webgate"
	"github.com/3xDevOps/Aether/web"
)

const (
	// readHeaderTimeout bounds how long a client may take to send its
	// request headers, and how long an attach may take to send its
	// header frame.
	readHeaderTimeout = 10 * time.Second
	// shutdownTimeout bounds the graceful HTTP shutdown on Close.
	shutdownTimeout = 5 * time.Second
	// defaultRevalidateInterval is how often a live WebSocket re-checks
	// that its token still exists, so a revoke reaches streams that are
	// already open.
	defaultRevalidateInterval = 5 * time.Second
)

// Config wires the gateway to the components it fronts.
type Config struct {
	// Port is the loopback listener port (--dashboard-port), the target of
	// the `aether dash` SSH forward; 0 means no loopback listener.
	Port int
	// Addr exposes the gateway directly on this address
	// (--dashboard-addr); empty means loopback only.
	Addr string
	// RPC dispatches control-channel methods.
	RPC *sshd.Bridge
	// Bus feeds the event subscription endpoint.
	Bus events.Bus
	// PTY serves terminal attach.
	PTY sshd.PTYAttacher
	// Git renders run diffs as patch text; nil disables the diff endpoint.
	Git Patcher
	// DataDir is the server's data directory, reported as disk usage;
	// empty disables the disk endpoint.
	DataDir string
	// Static is the built SPA; nil means the embedded web/dist.
	Static fs.FS

	// revalidate is how often a live WebSocket re-checks its token; zero
	// means defaultRevalidateInterval. Unexported test knob, like the SSH
	// server's sync revalidation interval.
	revalidate time.Duration
}

// Gateway is the HTTP/WebSocket dashboard server.
type Gateway struct {
	cfg    Config
	tokens *Tokens
	srv    *http.Server
	// disk memoizes the data directory's tree walk; the gauge is refreshed
	// on every event batch and the walk is far too expensive for that.
	disk *disk.Cache

	// ctx bounds every request, including the hijacked WebSocket handlers
	// http.Server.Shutdown cannot wait for; wg counts those handlers plus
	// the listener goroutines so Close outlives none of them.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	lns     []net.Listener
	conns   map[*websocket.Conn]struct{}
	closing bool
	once    sync.Once
	err     error
}

// beginHandler registers a streaming handler with the shutdown WaitGroup
// unless the gateway is closing, so an Add can never race the Wait in
// Close - hijacked WebSocket connections are past anything
// http.Server.Shutdown still tracks.
func (g *Gateway) beginHandler() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.wg.Add(1)
	return true
}

// trackConn registers a live WebSocket so Close can tear it down. It
// reports false once the gateway is closing, in which case the caller
// closes the socket and returns. Close waits for handlers, so it must
// never depend on one noticing a canceled context: a socket read or write
// that has stopped making progress is ended by closing the socket under
// it, exactly as the SSH server closes its tracked connections.
func (g *Gateway) trackConn(c *websocket.Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.conns[c] = struct{}{}
	return true
}

func (g *Gateway) untrackConn(c *websocket.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.conns, c)
}

// New builds the gateway. It binds nothing until Start.
func New(cfg Config) (*Gateway, error) {
	if cfg.RPC == nil || cfg.Bus == nil || cfg.PTY == nil {
		return nil, errors.New("dashboard: config requires RPC, Bus, and PTY")
	}
	if cfg.Port == 0 && cfg.Addr == "" {
		return nil, errors.New("dashboard: config requires a loopback port or a direct address")
	}
	if cfg.Static == nil {
		sub, err := fs.Sub(web.Dist, "dist")
		if err != nil {
			return nil, fmt.Errorf("dashboard: embedded spa: %w", err)
		}
		cfg.Static = sub
	}
	if cfg.revalidate <= 0 {
		cfg.revalidate = defaultRevalidateInterval
	}
	g := &Gateway{
		cfg:    cfg,
		tokens: newTokens(directOrigin(cfg.Addr)),
		conns:  make(map[*websocket.Conn]struct{}),
	}
	if cfg.DataDir != "" {
		g.disk = disk.NewCache(cfg.DataDir, 0)
	}
	g.ctx, g.cancel = context.WithCancel(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/{method}", g.handleAPI)
	mux.HandleFunc("GET /api/v1/run/{run}/patch", g.handlePatch)
	mux.HandleFunc("GET /api/v1/disk", g.handleDisk)
	mux.HandleFunc("GET /api/v1/capabilities", g.handleCapabilities)
	mux.HandleFunc("GET /ws/events", g.handleEvents)
	mux.HandleFunc("GET /ws/attach/{run}", g.handleAttach)
	static := staticHandler(cfg.Static)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An /api or /ws request that misses every method-qualified
		// pattern lands here; answering it with the SPA would turn a
		// wrong-verb client bug into a silent 200.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			webgate.WriteError(w, http.StatusMethodNotAllowed, &protocol.Error{
				Code:    protocol.CodeInvalidRequest,
				Message: "method not allowed",
			})
			return
		}
		static.ServeHTTP(w, r)
	}))
	g.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return g.ctx },
	}
	return g, nil
}

// Tokens is the bearer-token table, minted and revoked over the SSH
// control channel through the sshd.DashboardTokens seam.
func (g *Gateway) Tokens() *Tokens { return g.tokens }

// Start binds the configured listeners and serves in the background. The
// context bounds setup only; Close stops the gateway.
func (g *Gateway) Start(_ context.Context) error {
	addrs := make([]string, 0, 2)
	if g.cfg.Port > 0 {
		addrs = append(addrs, net.JoinHostPort("127.0.0.1", strconv.Itoa(g.cfg.Port)))
	}
	if g.cfg.Addr != "" {
		addrs = append(addrs, g.cfg.Addr)
	}
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("dashboard: listen %s: %w", addr, err)
		}
		g.mu.Lock()
		g.lns = append(g.lns, ln)
		g.mu.Unlock()
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			_ = g.srv.Serve(ln)
		}()
	}
	return nil
}

// Addrs returns the bound listener addresses, empty before Start.
func (g *Gateway) Addrs() []net.Addr {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]net.Addr, 0, len(g.lns))
	for _, ln := range g.lns {
		out = append(out, ln.Addr())
	}
	return out
}

// Close stops serving and waits for every handler, streaming ones
// included, to return. Idempotent and safe before Start.
func (g *Gateway) Close() error {
	g.once.Do(func() {
		g.mu.Lock()
		g.closing = true
		live := make([]*websocket.Conn, 0, len(g.conns))
		for c := range g.conns {
			live = append(live, c)
		}
		g.conns = make(map[*websocket.Conn]struct{})
		g.mu.Unlock()

		// Cancel first so streams end on their own terms, then close the
		// sockets under whatever is left: Shutdown does not track
		// hijacked connections, so nothing else would ever unblock a
		// handler parked on one.
		g.cancel()
		for _, c := range live {
			_ = c.CloseNow()
		}
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := g.srv.Shutdown(ctx); err != nil {
			g.err = fmt.Errorf("dashboard: shutdown: %w", err)
		}
		g.wg.Wait()
	})
	return g.err
}
