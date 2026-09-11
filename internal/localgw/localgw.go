// Package localgw is the client-side local gateway: it composes the
// shared dashboard gateway (internal/webgate) on a tokened loopback port,
// proxying the API shape over the linked server's SSH connection, and
// adds the /local/v1 verbs and the environment scan only a machine with
// the user's repository and SSH key can offer. Same SPA, same API shape,
// full SSH authority.
package localgw

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
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
)

// closeTimeout bounds the graceful drain in Close.
const closeTimeout = 5 * time.Second

// Backend is the local gateway's view of the linked server: the shared
// gateway's surface plus the streams and the relink only this gateway
// uses.
type Backend interface {
	webgate.Backend
	// Sync opens the sync subsystem's raw mutagen endpoint stream.
	Sync(runID string, force bool) (io.ReadWriteCloser, error)
	// Forward opens one direct-tcpip channel to a forwarding target.
	Forward(target string, port uint32) (io.ReadWriteCloser, error)
	// Relink swaps the saved config and live connection without restarting.
	Relink(cfg cli.Config, conn *cli.Conn)
	// Close releases the backend's shared connection.
	Close() error
}

func validForwardTarget(target string) bool {
	return target == cli.TerminalForwardTarget ||
		(strings.HasPrefix(target, "run:") && len(strings.TrimPrefix(target, "run:")) > 0)
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
	core  *webgate.Gateway
	ln    net.Listener
	// exit is closed once when a verb asks the process to stop; the
	// command that owns the process waits on it beside its signals.
	exit     chan struct{}
	exitOnce sync.Once
	// exitCode is the status that command should exit with, written
	// before exit closes and read only after. ExitRelaunch tells the
	// desktop shell to relaunch itself rather than respawn the sidecar.
	exitCode int
	// rebuild tracks the desktop-app build update.apply starts, and
	// builds counts the goroutine running it, so Close can wait for the
	// killed child to be reaped and its outcome recorded before the
	// process, or a test, moves on.
	rebuild *rebuildState
	builds  sync.WaitGroup
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
	core, err := webgate.New(webgate.Config{
		Authorize: g.authorize,
		Capabilities: protocol.GatewayCapabilities{
			Gateway: "local",
			WS:      []string{"events", "attach", "terminal", "envscan"},
			Local:   localVerbs,
		},
		Static: cfg.Static,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	g.core = core
	core.HandleFunc("GET /ws/envscan", g.handleEnvScan)
	core.HandleFunc("POST /local/v1/{verb}", g.handleLocal)
	// A local path hit with the wrong method answers 405 like the core's
	// own routes, rather than the core's 404 for a gateway without them.
	core.HandleFunc("/local/", func(w http.ResponseWriter, _ *http.Request) {
		webgate.WriteError(w, http.StatusMethodNotAllowed, &protocol.Error{
			Code:    protocol.CodeInvalidRequest,
			Message: "method not allowed",
		})
	})
	return g, nil
}

// ServeHTTP serves the gateway's routes: the shared core's plus the local
// verbs and the environment scan.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.core.ServeHTTP(w, r) }

// mintToken returns the per-process bearer token: 32 random bytes,
// base64url without padding.
func mintToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// authorize admits a request that carries the gateway token, as a Bearer
// header always and as ?token= only on a WebSocket handshake and the
// initial browser tab, which cannot set headers. Every admitted request
// acts through the one linked-server backend: the token guards the
// loopback port, the SSH key behind the backend is the identity.
func (g *Gateway) authorize(r *http.Request, handshake bool) (webgate.Backend, *webgate.Refusal) {
	token := ""
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		token = strings.TrimSpace(h[7:])
	}
	if token == "" && handshake {
		token = r.URL.Query().Get("token")
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(g.token)) != 1 {
		return nil, &webgate.Refusal{
			Status: http.StatusUnauthorized,
			Error: &protocol.Error{
				Code:    protocol.CodeDenied,
				Message: "a valid gateway token is required; restart `aether gui` for a fresh URL",
			},
		}
	}
	return g.cfg.Backend, nil
}

// authorized reports whether r may run a local verb: the core's
// same-origin rule and then the token, exactly as for every other route.
func (g *Gateway) authorized(w http.ResponseWriter, r *http.Request) bool {
	_, ok := g.core.Authorize(w, r, false)
	return ok
}

// Start binds 127.0.0.1 and serves in the background. The context bounds
// setup only; Close stops the gateway.
func (g *Gateway) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(g.cfg.Port)))
	if err != nil {
		return fmt.Errorf("localgw: listen: %w", err)
	}
	g.ln = ln
	g.core.Serve(ln)
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

// Close releases the backend connection, stops serving, and drains in-flight
// requests briefly before cutting them off. It also stops the background work
// the gateway owns. Safe before Start, and safe to call twice.
func (g *Gateway) Close() error {
	g.cancel()
	g.local.forward.Close()
	backendErr := g.cfg.Backend.Close()
	serveErr := g.core.Close()
	// The cancelled context has killed any rebuild; its goroutine still has
	// to reap the child and record why it stopped. Waiting here keeps that
	// record with this gateway rather than whatever comes after it, which
	// is why it cannot sit behind the listener check: a gateway that never
	// served still runs rebuilds, and the record is read from a path that
	// belongs to whoever is running when it lands. The drain gets its own
	// deadline so a slow shutdown cannot spend it.
	built := make(chan struct{})
	go func() {
		g.builds.Wait()
		close(built)
	}()
	drain, stop := context.WithTimeout(context.Background(), closeTimeout)
	defer stop()
	select {
	case <-built:
	case <-drain.Done():
	}
	return errors.Join(backendErr, serveErr)
}
