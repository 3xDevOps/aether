// Package localgw is the client-side local gateway: it serves the
// embedded dashboard SPA on a tokened loopback port and proxies the
// dashboard API shape over the linked server's SSH connection, adding the
// /local/v1 verbs only a machine with the user's repository and SSH key
// can offer. Same SPA, same API shape, full SSH authority.
package localgw

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/selfupdate"
	"github.com/3xDevOps/Aether/internal/webgate"
	"github.com/3xDevOps/Aether/web"
)

// httpReadHeaderTimeout bounds how long a client may dribble request headers.
const httpReadHeaderTimeout = 10 * time.Second

// closeTimeout bounds the graceful drain in Close.
const closeTimeout = 5 * time.Second

// Backend is the local gateway's view of the linked server: control-channel
// calls plus the streaming subsystems the WebSocket handlers bridge.
type Backend interface {
	// Call performs one control-channel method call. Server-reported
	// failures and transport failures both surface as *protocol.Error.
	Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error)
	// Events opens the events subsystem; the header and ack are already
	// consumed. A server refusal surfaces as *protocol.Error.
	Events(req protocol.SubscribeRequest) (io.ReadWriteCloser, error)
	// Attach opens the attach subsystem for one run's PTY.
	Attach(req protocol.AttachRequest) (cli.Terminal, protocol.AttachResponse, error)
	// Shell opens the unified workspace-shell subsystem.
	Shell(req protocol.WorkspaceShellRequest) (cli.Terminal, protocol.WorkspaceShellResponse, error)
	// Sync opens the sync subsystem's raw mutagen endpoint stream.
	Sync(runID string, force bool) (io.ReadWriteCloser, error)
}

// Config wires the local gateway to its backend and static assets.
type Config struct {
	// Port is the loopback port to bind; 0 picks an ephemeral one.
	Port int
	// Backend proxies calls and streams to the linked server. Required.
	Backend Backend
	// Static is the built SPA; nil means the embedded web/dist.
	Static fs.FS
	// CLI is the saved link config (addr/user/repo/key/known_hosts) the
	// /local/v1 verbs operate on.
	CLI cli.Config
	// Update answers the release check for the update verbs; nil installs
	// selfupdate.DefaultChecker().
	Update *selfupdate.Checker
	// Supervised marks a gateway the desktop shell spawned (aether gui
	// --json): update.apply exits the process because the shell restarts
	// it.
	Supervised bool
}

// Gateway is the local HTTP/WebSocket gateway server.
type Gateway struct {
	cfg   Config
	local *localState
	token string
	mux   *http.ServeMux
	srv   *http.Server
	ln    net.Listener
	// exit is closed once when a verb asks the process to stop; the
	// command that owns the process waits on it beside its signals.
	exit     chan struct{}
	exitOnce sync.Once
	// exitCode is the status that command should exit with, written
	// before exit closes and read only after. ExitRelaunch tells the
	// desktop shell to relaunch itself rather than respawn the sidecar.
	exitCode int
	// rebuild tracks the desktop-app build update.apply starts.
	rebuild *rebuildState
	// updating is set while one update.apply is swapping the binary, so
	// a second cannot start another swap - or a second administrator
	// dialog - under it.
	updating atomic.Bool
	// installed is what update.apply last put on disk from this process.
	// The release check keeps reporting the version this process was
	// built with, so without it a second tab's click would download and,
	// on macOS, ask for the password again to install the same bytes.
	installed atomic.Pointer[installedRelease]
	// ctx bounds the background work this gateway owns - so far the
	// desktop-app rebuild child - and Close cancels it. Without it a
	// rebuild outlives the app that started it, still downloading Node and
	// still swapping the directory of an app the user just quit.
	ctx    context.Context
	cancel context.CancelFunc
}

// installedRelease is one release update.apply installed: its tag and
// the binaries it replaced, in order.
type installedRelease struct {
	tag   string
	paths []string
}

// New builds the gateway and mints its per-process token. It binds
// nothing until Start.
func New(cfg Config) (*Gateway, error) {
	if cfg.Backend == nil {
		return nil, errors.New("localgw: config requires a Backend")
	}
	if cfg.Static == nil {
		sub, err := fs.Sub(web.Dist, "dist")
		if err != nil {
			return nil, fmt.Errorf("localgw: embedded spa: %w", err)
		}
		cfg.Static = sub
	}
	if cfg.Update == nil {
		cfg.Update = selfupdate.DefaultChecker()
	}
	token, err := mintToken()
	if err != nil {
		return nil, fmt.Errorf("localgw: mint token: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{
		cfg:     cfg,
		local:   newLocalState(cfg),
		token:   token,
		exit:    make(chan struct{}),
		rebuild: newRebuildState(),
		ctx:     ctx,
		cancel:  cancel,
	}
	g.mux = http.NewServeMux()
	g.mux.HandleFunc("POST /api/v1/{method}", g.handleAPI)
	g.mux.HandleFunc("GET /api/v1/run/{run}/patch", g.handlePatch)
	g.mux.HandleFunc("GET /api/v1/disk", g.handleDisk)
	g.mux.HandleFunc("GET /api/v1/capabilities", g.handleCapabilities)
	g.mux.HandleFunc("GET /ws/events", g.handleEvents)
	g.mux.HandleFunc("GET /ws/attach/{run}", g.handleAttach)
	g.mux.HandleFunc("GET /ws/shell", g.handleShell)
	g.mux.HandleFunc("GET /ws/envscan", g.handleEnvScan)
	g.mux.HandleFunc("POST /local/v1/{verb}", g.handleLocal)
	static := webgate.StaticHandler(cfg.Static)
	g.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An /api, /ws, or /local request that misses every
		// method-qualified pattern lands here; answering it with the SPA
		// would turn a wrong-verb client bug into a silent 200.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") || strings.HasPrefix(r.URL.Path, "/local/") {
			webgate.WriteError(w, http.StatusMethodNotAllowed, &protocol.Error{
				Code:    protocol.CodeInvalidRequest,
				Message: "method not allowed",
			})
			return
		}
		static.ServeHTTP(w, r)
	}))
	g.srv = &http.Server{
		Handler:           g.mux,
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}
	return g, nil
}

// mintToken returns the per-process bearer token: 32 random bytes,
// base64url without padding.
func mintToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// authorized reports whether r carries the gateway token, as a Bearer
// header always and as ?token= only when allowQuery is set - the query
// form exists for WebSocket handshakes and the initial browser tab, which
// cannot set headers.
func (g *Gateway) authorized(r *http.Request, allowQuery bool) bool {
	token := ""
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		token = strings.TrimSpace(h[7:])
	}
	if token == "" && allowQuery {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(g.token)) == 1
}

// deny answers an unauthorized request with the JSON 401 the SPA's API
// client decodes.
func (g *Gateway) deny(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	webgate.WriteError(w, http.StatusUnauthorized, &protocol.Error{
		Code:    protocol.CodeDenied,
		Message: "a valid gateway token is required; restart `aether gui` for a fresh URL",
	})
}

// Start binds 127.0.0.1 and serves in the background. The context bounds
// setup only; Close stops the gateway.
func (g *Gateway) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(g.cfg.Port)))
	if err != nil {
		return fmt.Errorf("localgw: listen: %w", err)
	}
	g.ln = ln
	go func() { _ = g.srv.Serve(ln) }()
	return nil
}

// Addr returns the bound host:port, empty before Start.
func (g *Gateway) Addr() string {
	if g.ln == nil {
		return ""
	}
	return g.ln.Addr().String()
}

// Token returns the per-process bearer token, valid from New.
func (g *Gateway) Token() string { return g.token }

// Exit is closed when a verb asks the process to stop, so far only
// update.apply on a supervised gateway. It stays open otherwise.
func (g *Gateway) Exit() <-chan struct{} { return g.exit }

// ExitCode is the status the process should exit with, valid once Exit is
// closed. Zero means an ordinary stop the desktop shell answers by
// respawning the sidecar; ExitRelaunch means the app on disk was rebuilt
// and the shell has to relaunch itself to pick it up.
func (g *Gateway) ExitCode() int { return g.exitCode }

// requestExit closes Exit with the status the process should carry, at
// most once however many verbs ask.
func (g *Gateway) requestExit(code int) {
	g.exitOnce.Do(func() {
		g.exitCode = code
		close(g.exit)
	})
}

// Close stops serving, draining in-flight requests briefly before cutting
// them off, and stops the background work the gateway owns. Safe before
// Start, and safe to call twice.
func (g *Gateway) Close() error {
	g.cancel()
	if g.ln == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	err := g.srv.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = g.srv.Close()
	}
	return err
}
