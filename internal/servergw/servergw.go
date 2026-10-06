// Package servergw serves the dashboard gateway from aether-server over
// HTTPS on its tailnet addresses, identifying each request by Tailscale WhoIs.
package servergw

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/webgate"
)

// identityTimeout bounds the WhoIs lookup a request waits on, so a
// stalled tailscaled cannot pin handlers.
const identityTimeout = 10 * time.Second

type Config struct {
	// Required.
	SSH *sshd.Server
	// Static is the built SPA; nil means the embedded web/dist.
	Static fs.FS
}

type Gateway struct {
	core *webgate.Gateway
	ssh  *sshd.Server
	lns  []net.Listener
	// Bounds the certificate refresh Start begins; Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc
}

// New refuses a server that cannot identify HTTP callers (no tailscaled at
// startup, or tailnet-require-key). It binds nothing until Start.
func New(cfg Config) (*Gateway, error) {
	if cfg.SSH == nil {
		return nil, errors.New("servergw: config requires SSH")
	}
	if err := cfg.SSH.WebIdentity(); err != nil {
		return nil, fmt.Errorf("servergw: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{ssh: cfg.SSH, ctx: ctx, cancel: cancel}
	core, err := webgate.New(webgate.Config{
		Authorize: g.authorize,
		Capabilities: protocol.GatewayCapabilities{
			Gateway: "server",
			WS:      []string{"events", "attach", "acp", "terminal", "dev/browser"},
		},
		Static: cfg.Static,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	g.core = core
	return g, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.core.ServeHTTP(w, r) }

// authorize resolves every request afresh so revocation follows the tailnet
// immediately, with no session to outlive it.
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

// Done is closed when every tailnet listener has died with an error.
func (g *Gateway) Done() <-chan struct{} { return g.core.Done() }

// Err is valid once Done is closed.
func (g *Gateway) Err() error { return g.core.Err() }

// Close is safe before Start and safe to call twice.
func (g *Gateway) Close() error {
	g.cancel()
	err := g.core.Close()
	for _, ln := range g.lns {
		_ = ln.Close()
	}
	return err
}
