// Package localgw serves the dashboard gateway (internal/webgate) on a tokened
// loopback port over the linked server's SSH connection, plus the /local/v1
// verbs that need the user's repository and SSH key.
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

const closeTimeout = 5 * time.Second

type Backend interface {
	webgate.Backend
	Sync(runID string, force bool) (io.ReadWriteCloser, error)
	Forward(target string, port uint32) (io.ReadWriteCloser, error)
	Relink(cfg cli.Config, conn *cli.Conn)
	Close() error
}

func validForwardTarget(target string) bool {
	return target == cli.TerminalForwardTarget ||
		(strings.HasPrefix(target, "run:") && len(strings.TrimPrefix(target, "run:")) > 0)
}

type Config struct {
	Port    int
	Backend Backend
	// Static is the built SPA; nil means the embedded web/dist.
	Static fs.FS
	// CLI is the saved link config the /local/v1 verbs operate on.
	CLI cli.Config
	// Update nil installs selfupdate.DefaultChecker().
	Update *selfupdate.Checker
	// Supervised marks a gateway the desktop shell spawned: update.apply
	// exits the process because the shell restarts it.
	Supervised bool
}

type Gateway struct {
	cfg      Config
	local    *localState
	token    string
	core     *webgate.Gateway
	ln       net.Listener
	exit     chan struct{}
	exitOnce sync.Once
	// exitCode is written before exit closes and read only after.
	exitCode int
	// builds lets Close wait for a killed rebuild child to be reaped and
	// its outcome recorded.
	rebuild *rebuildState
	builds  sync.WaitGroup
	// updating stops a second update.apply - and a second administrator
	// dialog - from starting under a running swap.
	updating atomic.Bool
	// installed exists because the release check keeps reporting this
	// process's own version, so a second tab would reinstall the same bytes.
	installed atomic.Pointer[installedRelease]
	// ctx stops a rebuild from outliving the app the user just quit.
	ctx    context.Context
	cancel context.CancelFunc
}

// installedRelease paths are the binaries it replaced, in order.
type installedRelease struct {
	tag   string
	paths []string
}

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
			WS:      []string{"events", "attach", "acp", "terminal", "dev/browser"},
			Local:   localVerbs,
		},
		Static: cfg.Static,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	g.core = core
	core.HandleFunc("POST /local/v1/{verb}", g.handleLocal)
	// 405 like the core's own routes, not the core's 404.
	core.HandleFunc("/local/", func(w http.ResponseWriter, _ *http.Request) {
		webgate.WriteError(w, http.StatusMethodNotAllowed, &protocol.Error{
			Code:    protocol.CodeInvalidRequest,
			Message: "method not allowed",
		})
	})
	return g, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.core.ServeHTTP(w, r) }

func mintToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// authorize accepts ?token= only on a handshake, which cannot set headers.
// The token guards the loopback port; the SSH key behind the backend is the
// identity.
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

func (g *Gateway) authorized(w http.ResponseWriter, r *http.Request) bool {
	_, ok := g.core.Authorize(w, r, false)
	return ok
}

func (g *Gateway) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(g.cfg.Port)))
	if err != nil {
		return fmt.Errorf("localgw: listen: %w", err)
	}
	g.ln = ln
	g.core.Serve(ln)
	// A listener that dies leaves a gateway the desktop shell believes is
	// healthy; exiting nonzero is what makes the shell respawn it.
	go func() {
		select {
		case <-g.core.Done():
			g.requestExit(1)
		case <-g.ctx.Done():
		}
	}()
	return nil
}

func (g *Gateway) Addr() string {
	if g.ln == nil {
		return ""
	}
	return g.ln.Addr().String()
}

func (g *Gateway) Token() string { return g.token }

func (g *Gateway) Exit() <-chan struct{} { return g.exit }

// ExitCode is valid once Exit is closed. ExitRelaunch tells the desktop shell
// to relaunch itself rather than respawn the sidecar.
func (g *Gateway) ExitCode() int { return g.exitCode }

func (g *Gateway) requestExit(code int) {
	g.exitOnce.Do(func() {
		g.exitCode = code
		close(g.exit)
	})
}

// Close is safe before Start and safe to call twice.
func (g *Gateway) Close() error {
	g.cancel()
	g.local.forward.Close()
	backendErr := g.cfg.Backend.Close()
	serveErr := g.core.Close()
	// Wait even if never served: a rebuild's outcome must be recorded by
	// this gateway, not whatever runs after it.
	built := make(chan struct{})
	go func() {
		g.builds.Wait()
		g.local.edge.waits.Wait()
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
