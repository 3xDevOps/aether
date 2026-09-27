package servergw

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/webgate"
)

const (
	// touchInterval bounds how often a session's last use is written.
	touchInterval = time.Minute
	// revalidateInterval is how often the live WebSockets of each session
	// are checked against its device, so a revoked session or a removed
	// member loses them within that time.
	revalidateInterval = 3 * time.Second
)

// EdgeAgent is the part of the edge agent the edge gateway uses.
// *edgeagent.Agent implements it.
type EdgeAgent interface {
	ServerID() string
	// WebListener yields the TLS connections the edge passes through.
	WebListener() net.Listener
	// RedeemWebCode exchanges a web sign-in code and its PKCE verifier for
	// a verified web grant.
	RedeemWebCode(ctx context.Context, code, verifier string) (edgeproto.Grant, error)
}

// EdgeConfig wires the edge gateway to the server it fronts.
type EdgeConfig struct {
	// SSH serves members' calls. Required.
	SSH *sshd.Server
	// Store holds browser sessions as browser devices. Required.
	Store store.IdentityStore
	// Agent is the server's edge agent. Required.
	Agent EdgeAgent
	// EdgeURL is the edge the agent enrolled with; sign-in redirects there.
	EdgeURL string
	// ServerDomain is the domain the edge serves dashboards under: this
	// server answers <server id>.<ServerDomain>.
	ServerDomain string
	// DeviceAutoApprove accepts every new browser of a member, as it does
	// every new device key.
	DeviceAutoApprove bool
	// CertDir caches the ACME account and certificate. Required unless
	// Certificate is set.
	CertDir string
	// ACMEDirectory is the ACME directory URL; empty is Let's Encrypt.
	ACMEDirectory string
	// Certificate, when set, is served instead of issuing one over ACME.
	// Tests use it.
	Certificate *tls.Certificate
	// Static is the built SPA; nil means the embedded web/dist.
	Static fs.FS
}

// Edge is the dashboard gateway browsers reach through the edge's TLS
// passthrough. It terminates TLS itself with a certificate for exactly
// its own hostname and identifies every request by a session cookie only:
// no tailnet WhoIs, no bearer token. docs/edge.md describes the sign-in.
type Edge struct {
	core     *webgate.Gateway
	ssh      *sshd.Server
	ids      store.IdentityStore
	agent    EdgeAgent
	edge     string
	serverID string
	host     string
	approve  bool
	certs    *edgeCerts
	handler  http.Handler

	now        func() time.Time
	revalidate time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	live map[domain.DeviceID]map[*liveSocket]struct{}
}

// NewEdge builds the edge gateway. It binds nothing and issues nothing
// until Start; Close releases it.
func NewEdge(cfg EdgeConfig) (*Edge, error) {
	return newEdge(cfg, time.Now, revalidateInterval)
}

// newEdge is NewEdge with the clock and the revalidation interval tests
// set.
func newEdge(cfg EdgeConfig, now func() time.Time, revalidate time.Duration) (*Edge, error) {
	switch {
	case cfg.SSH == nil || cfg.Store == nil || cfg.Agent == nil:
		return nil, errors.New("servergw: edge config requires SSH, Store and Agent")
	case cfg.Certificate == nil && cfg.CertDir == "":
		return nil, errors.New("servergw: edge config requires CertDir")
	case !edgeproto.ValidServerDomain(cfg.ServerDomain):
		return nil, fmt.Errorf("servergw: edge server domain %q is not a lowercase DNS name such as servers.example.com", cfg.ServerDomain)
	}
	origin, err := edgeproto.Origin(cfg.EdgeURL)
	if err != nil {
		return nil, fmt.Errorf("servergw: %w", err)
	}
	host := edgeproto.ServerHostname(cfg.Agent.ServerID(), cfg.ServerDomain)
	ctx, cancel := context.WithCancel(context.Background())
	e := &Edge{
		ssh: cfg.SSH, ids: cfg.Store, agent: cfg.Agent,
		edge: origin, serverID: cfg.Agent.ServerID(), host: host, approve: !cfg.DeviceAutoApprove,
		certs:      newEdgeCerts(host, cfg.CertDir, cfg.ACMEDirectory, cfg.Certificate),
		now:        now,
		revalidate: revalidate,
		ctx:        ctx, cancel: cancel,
		live: make(map[domain.DeviceID]map[*liveSocket]struct{}),
	}
	core, err := webgate.New(webgate.Config{
		Authorize: e.authorize,
		Capabilities: protocol.GatewayCapabilities{
			Gateway: "edge",
			WS:      []string{"events", "attach", "terminal", "dev/browser"},
		},
		Static: cfg.Static,
		Wrap:   e.wrap,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	e.handler = e.wrap(core)
	core.HandleFunc("GET "+pathLogin, e.handleLogin)
	core.HandleFunc("GET "+edgeproto.PathAuthCallback, e.handleCallback)
	core.HandleFunc("POST "+pathLogout, e.handleLogout)
	e.core = core
	go e.revalidateLoop()
	return e, nil
}

// Start serves the gateway on the agent's web listener and starts issuing
// the certificate in the background. Neither can stop the server: the
// listener blocks through edge outages, a failed handshake ends only its
// own connection, and issuance retries with backoff.
func (e *Edge) Start() {
	e.core.Serve(tls.NewListener(e.agent.WebListener(), &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: e.certs.get,
		NextProtos:     []string{"http/1.1", acme.ALPNProto},
	}))
	if e.certs.fixed == nil {
		go e.certs.issue(e.ctx)
	}
}

// Host is the hostname the gateway answers.
func (e *Edge) Host() string { return e.host }

// ServeHTTP serves the gateway's routes exactly as its listener does; a
// test can drive it without the edge.
func (e *Edge) ServeHTTP(w http.ResponseWriter, r *http.Request) { e.handler.ServeHTTP(w, r) }

// wrap adds the dashboard's security headers to every response, and gives
// every WebSocket handshake a context its session's revocation cancels.
func (e *Edge) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=63072000")
		h.Set("Content-Security-Policy", "base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		// The callback URL carries a sign-in code.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			// webgate ends a socket when its handshake request's context
			// ends.
			ctx, cancel := context.WithCancel(r.Context())
			l := &liveSocket{cancel: cancel}
			defer e.untrack(l)
			defer cancel()
			r = r.WithContext(context.WithValue(ctx, liveKey{}, l))
		}
		next.ServeHTTP(w, r)
	})
}

// Close stops serving, ends every live WebSocket, and stops certificate
// issuance and session revalidation. Safe before Start and safe to call twice.
func (e *Edge) Close() error {
	e.cancel()
	return e.core.Close()
}

// authorize admits a request by its session cookie alone. A request that
// can change state must carry an Origin header, which webgate has already
// checked names this host: with a cookie credential, a missing Origin is
// the one case the same-origin rule does not cover.
func (e *Edge) authorize(r *http.Request, handshake bool) (webgate.Backend, *webgate.Refusal) {
	if (handshake || (r.Method != http.MethodGet && r.Method != http.MethodHead)) && r.Header.Get("Origin") == "" {
		return nil, &webgate.Refusal{Status: http.StatusForbidden, Error: &protocol.Error{
			Code:    protocol.CodeDenied,
			Message: "request without an Origin header refused: the dashboard over the edge accepts changes only from its own pages",
		}}
	}
	dev, refusal := e.session(r)
	if refusal != nil {
		return nil, refusal
	}
	if l, ok := r.Context().Value(liveKey{}).(*liveSocket); ok && handshake {
		e.track(dev.ID, l)
	}
	return backend{local: e.ssh.Local(dev.Member)}, nil
}

// session looks the request's session up on every request, so a revoked
// session or a removed member is refused at once.
func (e *Edge) session(r *http.Request) (*domain.Device, *webgate.Refusal) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || !edgeproto.ValidToken(c.Value) {
		return nil, signIn("sign-in required")
	}
	dev, err := e.ids.GetDeviceByCredential(r.Context(), edgeproto.HashToken(c.Value))
	if errors.Is(err, store.ErrNotFound) {
		return nil, signIn("this browser's session no longer exists on this server")
	}
	if err != nil {
		return nil, unavailable(fmt.Errorf("look up session: %w", err))
	}
	now := e.now()
	if refusal := e.check(dev, now); refusal != nil {
		return nil, refusal
	}
	if now.Sub(lastUse(dev)) >= touchInterval {
		if err := e.ids.TouchDevice(r.Context(), dev.ID, now.UTC()); err != nil {
			return nil, unavailable(fmt.Errorf("record session use: %w", err))
		}
	}
	return dev, nil
}

// check refuses a session that is revoked, pending or idle too long.
func (e *Edge) check(dev *domain.Device, now time.Time) *webgate.Refusal {
	switch {
	case dev.Status == domain.DeviceRevoked:
		return signIn("this browser's session was revoked")
	case dev.Status == domain.DevicePending:
		return pendingRefusal(dev)
	case now.Sub(lastUse(dev)) > edgeproto.SessionIdle:
		return signIn(fmt.Sprintf("this browser's session expired after %d days without use", int(edgeproto.SessionIdle/(24*time.Hour))))
	}
	return nil
}

func lastUse(dev *domain.Device) time.Time {
	if dev.LastSeenAt != nil {
		return *dev.LastSeenAt
	}
	return dev.CreatedAt
}

// loginData is the refusal data that tells the dashboard where to send the
// browser to sign in.
var loginData = json.RawMessage(`{"login":"` + pathLogin + `"}`)

func signIn(reason string) *webgate.Refusal {
	return &webgate.Refusal{Status: http.StatusUnauthorized, Error: &protocol.Error{
		Code:    protocol.CodeDenied,
		Message: reason + "; sign in at " + pathLogin,
		Data:    loginData,
	}}
}

func pendingRefusal(dev *domain.Device) *webgate.Refusal {
	data, _ := json.Marshal(struct {
		ApprovalCode string `json:"approval_code"`
	}{dev.ApprovalCode}) // a struct of one string always encodes
	return &webgate.Refusal{Status: http.StatusForbidden, Error: &protocol.Error{
		Code:    protocol.CodeDenied,
		Message: pendingText(dev),
		Data:    data,
	}}
}

func pendingText(dev *domain.Device) string {
	return fmt.Sprintf("This browser is waiting for approval with code %s. From a device this account already uses, or as an admin, run:\n"+
		"  aether device approve %s\nor on the server:\n  sudo aether-server device approve %s\nThen reload this page.\n",
		dev.ApprovalCode, dev.ApprovalCode, dev.ApprovalCode)
}

func unavailable(err error) *webgate.Refusal {
	return &webgate.Refusal{Status: http.StatusServiceUnavailable, Error: &protocol.Error{
		Code: protocol.CodeUnavailable, Message: err.Error(),
	}}
}

// liveKey carries a WebSocket handshake's liveSocket from ServeHTTP to
// authorize.
type liveKey struct{}

// liveSocket is one WebSocket of a session; cancel ends it.
type liveSocket struct {
	cancel context.CancelFunc
	device domain.DeviceID
}

func (e *Edge) track(id domain.DeviceID, l *liveSocket) {
	e.mu.Lock()
	defer e.mu.Unlock()
	l.device = id
	if e.live[id] == nil {
		e.live[id] = make(map[*liveSocket]struct{})
	}
	e.live[id][l] = struct{}{}
}

func (e *Edge) untrack(l *liveSocket) {
	e.mu.Lock()
	defer e.mu.Unlock()
	set := e.live[l.device]
	delete(set, l)
	if len(set) == 0 {
		delete(e.live, l.device)
	}
}

// end closes every live WebSocket of a session.
func (e *Edge) end(id domain.DeviceID) {
	e.mu.Lock()
	set := e.live[id]
	delete(e.live, id)
	e.mu.Unlock()
	for l := range set {
		l.cancel()
	}
}

// revalidateLoop re-reads the device of every session with a live
// WebSocket. Revocation by `aether device revoke` and member removal
// happen in sshd, which does not know these sockets.
func (e *Edge) revalidateLoop() {
	t := time.NewTicker(e.revalidate)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
		}
		e.mu.Lock()
		ids := make([]domain.DeviceID, 0, len(e.live))
		for id := range e.live {
			ids = append(ids, id)
		}
		e.mu.Unlock()
		for _, id := range ids {
			dev, err := e.ids.GetDevice(e.ctx, id)
			switch {
			case errors.Is(err, store.ErrNotFound):
				e.end(id)
			case err != nil:
				slog.Warn("servergw: revalidate edge session", "device", id, "error", err)
			case e.check(dev, e.now()) != nil:
				e.end(id)
			}
		}
	}
}
