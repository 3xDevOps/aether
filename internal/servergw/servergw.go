// Package servergw is the server-hosted dashboard gateway: the shared
// dashboard gateway (internal/webgate) served by aether-server itself
// over HTTPS on its tailnet addresses, every request identified by
// Tailscale WhoIs and served in-process for that member through the same
// dispatch and subsystem handlers the SSH transport uses. It offers no
// /local/v1 verbs and no environment scan: nothing on the server is the
// member's own machine.
package servergw

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/webgate"
)

const (
	// httpReadHeaderTimeout bounds how long a client may dribble request
	// headers.
	httpReadHeaderTimeout = 10 * time.Second
	// closeTimeout bounds the graceful drain in Close.
	closeTimeout = 5 * time.Second
	// identityTimeout bounds the WhoIs lookup a request waits on, so a
	// stalled tailscaled cannot pin handlers.
	identityTimeout = 10 * time.Second
)

// Config wires the gateway to the server it fronts.
type Config struct {
	// SSH identifies callers and serves their subsystems. Required.
	SSH *sshd.Server
	// Static is the built SPA; nil means the embedded web/dist.
	Static fs.FS
}

// Gateway is the server-hosted HTTP/WebSocket gateway.
type Gateway struct {
	core *webgate.Gateway
	ssh  *sshd.Server
	srv  *http.Server
	lns  []listener
}

// New builds the gateway. It refuses a server that cannot identify HTTP
// callers (no tailscaled at startup, or tailnet-require-key) with the
// error naming what to change. It binds nothing until Start.
func New(cfg Config) (*Gateway, error) {
	if cfg.SSH == nil {
		return nil, errors.New("servergw: config requires SSH")
	}
	if err := cfg.SSH.WebIdentity(); err != nil {
		return nil, fmt.Errorf("servergw: %w", err)
	}
	g := &Gateway{ssh: cfg.SSH}
	core, err := webgate.New(webgate.Config{
		Authorize: g.authorize,
		Capabilities: protocol.GatewayCapabilities{
			Gateway: "server",
			WS:      []string{"events", "attach", "terminal"},
		},
		Static: cfg.Static,
	})
	if err != nil {
		return nil, err
	}
	g.core = core
	g.srv = &http.Server{
		Handler:           core,
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}
	return g, nil
}

// ServeHTTP serves the gateway's routes; a test can drive it without the
// tailnet listener.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.core.ServeHTTP(w, r) }

// authorize identifies the request's tailnet node as a member and hands
// back the in-process backend acting as them. Every request resolves
// afresh: revocation follows the tailnet the moment tailscaled stops
// answering for a node, with no session to outlive it.
func (g *Gateway) authorize(r *http.Request, _ bool) (webgate.Backend, *webgate.Refusal) {
	ctx, cancel := context.WithTimeout(r.Context(), identityTimeout)
	defer cancel()
	m, err := g.ssh.TailnetMember(ctx, r.RemoteAddr)
	if errors.Is(err, sshd.ErrTaggedNode) {
		return nil, &webgate.Refusal{
			Status: http.StatusForbidden,
			Error: &protocol.Error{
				Code:    protocol.CodeDenied,
				Message: "tagged tailnet node; the dashboard identifies members by their tailnet login and a tagged node has none",
			},
		}
	}
	if err != nil {
		return nil, &webgate.Refusal{
			Status: http.StatusServiceUnavailable,
			Error: &protocol.Error{
				Code:    protocol.CodeUnavailable,
				Message: "tailnet identity unavailable: " + err.Error(),
			},
		}
	}
	return backend{local: g.ssh.Local(m.ID)}, nil
}

// Close stops serving, drains in-flight requests briefly, and ends every
// live WebSocket. Safe before Start, and safe to call twice.
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
	g.core.Close()
	for _, ln := range g.lns {
		_ = ln.Close()
	}
	return err
}
