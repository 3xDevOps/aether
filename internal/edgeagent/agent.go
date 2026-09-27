// Package edgeagent keeps an Aether server's control connection to an edge:
// it enrolls with the host key, pins the edge's grant key, relays SSH and
// dashboard connections the edge opens, answers claim attempts and pushes
// the server's directory. docs/edge.md describes the edge.
package edgeagent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/edgeproto"
	"github.com/3xDevOps/Aether/internal/version"
	"golang.org/x/crypto/ssh"
)

// DefaultURL is the edge the project runs.
const DefaultURL = "https://edge.onaether.dev"

// SSH is the part of the SSH server the agent hands relayed connections,
// claims and revocations to. *sshd.Server implements it.
type SSH interface {
	// ServeEdgeConn serves one relayed SSH connection whose grant the agent
	// verified, and returns when the connection ends.
	ServeEdgeConn(ctx context.Context, nc net.Conn, grant edgeproto.Grant)
	ClaimByEdge(ctx context.Context, account edgeproto.Account) (domain.Member, error)
	EdgeDirectory(ctx context.Context) ([]edgeproto.DirectoryEntry, error)
	EdgeDirectoryChanged() <-chan struct{}
	CloseEdgeDevice(deviceKey string)
}

// Config configures an Agent.
type Config struct {
	// EdgeURL is the edge to enroll with, https://host[:port].
	EdgeURL string
	// DataDir is the server data directory; the agent keeps its state in
	// OpenState(DataDir, EdgeURL).
	DataDir string
	// HostKey is the server's SSH host key. It derives the server id and
	// signs enrollment.
	HostKey ssh.Signer
	// SSH receives relayed connections and claims. Leave needs none.
	SSH SSH
}

const (
	handshakeTimeout = 10 * time.Second
	writeTimeout     = 10 * time.Second
	// redeemTimeout bounds the wait for the edge's answer to a web sign-in
	// code.
	redeemTimeout = 10 * time.Second
	// maxSeenGrants bounds the replay cache: an edge cannot grow the
	// server's memory without limit by opening connections.
	maxSeenGrants = 4096
)

// Agent keeps one server's control connection to an edge.
type Agent struct {
	cfg      Config
	name     string
	origin   string
	serverID string
	state    *State
	web      *webListener

	// Timings are fields so tests can shorten them.
	minBackoff, maxBackoff    time.Duration
	pingInterval, idleTimeout time.Duration
	attachDeadline            time.Duration

	// SSH and the dashboard take connection slots from separate budgets:
	// reaching the dashboard host name needs no sign-in, so filling its
	// budget must not lock SSH out.
	sshSlots, webSlots chan struct{}

	// enrolled is closed at the first enrollment, once domain holds the
	// server domain that enrollment's ready announced.
	enrolled chan struct{}
	// claimed is closed once the edge holds this server claimed: at an
	// enrollment it reports claimed, or at a claim this server accepted.
	claimed chan struct{}

	mu      sync.Mutex
	domain  string
	runCtx  context.Context
	sess    *session
	conns   map[*relayConn]struct{}
	seen    map[string]time.Time
	waiters map[string]chan edgeproto.WebRedeemResult
}

// New checks cfg and returns an agent that has not dialed yet.
func New(cfg Config) (*Agent, error) {
	origin, err := edgeproto.Origin(cfg.EdgeURL)
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	if cfg.HostKey == nil || cfg.DataDir == "" {
		return nil, errors.New("edgeagent: config requires HostKey and DataDir")
	}
	state, err := OpenState(cfg.DataDir, origin)
	if err != nil {
		return nil, err
	}
	// The hostname only labels the server for its owner at the edge.
	name, _ := os.Hostname()
	// Encoding a hello now turns a hostname the edge would refuse into a
	// startup error instead of a retry loop.
	probe := edgeproto.Hello{Version: edgeproto.Version, HostKey: cfg.HostKey.PublicKey().Marshal(), AgentVersion: version.Version, Name: name}
	if _, err := edgeproto.EncodeControl(probe); err != nil {
		return nil, fmt.Errorf("edgeagent: hostname %q or version %q: %w", name, version.Version, err)
	}
	return &Agent{
		cfg:            cfg,
		name:           name,
		origin:         origin,
		serverID:       edgeproto.ServerID(cfg.HostKey.PublicKey()),
		state:          state,
		web:            newWebListener(origin),
		minBackoff:     edgeproto.ReconnectMinBackoff,
		maxBackoff:     edgeproto.ReconnectMaxBackoff,
		pingInterval:   edgeproto.PingInterval,
		idleTimeout:    edgeproto.ControlIdleTimeout,
		attachDeadline: edgeproto.AttachDeadline,
		sshSlots:       make(chan struct{}, edgeproto.MaxSSHConnsPerServer),
		webSlots:       make(chan struct{}, edgeproto.MaxWebConnsPerServer),
		enrolled:       make(chan struct{}),
		claimed:        make(chan struct{}),
		conns:          make(map[*relayConn]struct{}),
		seen:           make(map[string]time.Time),
		waiters:        make(map[string]chan edgeproto.WebRedeemResult),
	}, nil
}

// ServerID is this server's id at the edge.
func (a *Agent) ServerID() string { return a.serverID }

// WebListener yields the dashboard connections the edge passes through.
// Accept blocks through edge outages and fails only after Run has returned
// or the listener was closed.
func (a *Agent) WebListener() net.Listener { return a.web }

// ServerDomain waits for the first enrollment and returns the domain the
// edge passes this server's dashboard through under, as
// <server id>.<domain>. It is empty when the edge passes no dashboard
// through.
func (a *Agent) ServerDomain(ctx context.Context) (string, error) {
	select {
	case <-a.enrolled:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.domain, nil
}

// learnDomain keeps the server domain of the first enrollment: the
// dashboard is served under that hostname until the server restarts. It
// returns the domain being served.
func (a *Agent) learnDomain(d string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case <-a.enrolled:
		if d != a.domain {
			slog.Warn("edge: the edge now passes dashboards through under another domain; restart aether-server to serve the dashboard there",
				"edge", a.origin, "serving", a.domain, "announced", d)
		}
	default:
		a.domain = d
		close(a.enrolled)
	}
	return a.domain
}

// Claimed is closed once the edge holds this server claimed. The edge
// passes no dashboard connection through to an unclaimed server.
func (a *Agent) Claimed() <-chan struct{} { return a.claimed }

func (a *Agent) markClaimed() {
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case <-a.claimed:
	default:
		close(a.claimed)
	}
}

// Run keeps the control connection until ctx is done, reconnecting with
// jittered exponential backoff. It never gives up: an unreachable or
// misbehaving edge is logged and retried, and leaves the rest of the server
// untouched. Run closes the relayed connections and the web listener when
// it returns.
func (a *Agent) Run(ctx context.Context) {
	a.mu.Lock()
	a.runCtx = ctx
	a.mu.Unlock()
	defer a.stop()

	backoff := a.minBackoff
	var down time.Time
	for {
		connected, err := a.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected >= a.maxBackoff {
			backoff = a.minBackoff
		}
		if connected > 0 || down.IsZero() {
			down = time.Now()
		}
		a.state.writeStatus(Status{Edge: a.origin, Error: err.Error(), Since: down})
		// The window is never empty, so servers an edge restart dropped
		// together do not redial together.
		wait := a.minBackoff + rand.N(backoff)
		slog.Warn("edge: control connection ended; reconnecting", "edge", a.origin, "error", err, "retry_in", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, a.maxBackoff-a.minBackoff)
	}
}

func (a *Agent) stop() {
	_ = a.web.Close()
	a.mu.Lock()
	conns := make([]*relayConn, 0, len(a.conns))
	for rc := range a.conns {
		conns = append(conns, rc)
	}
	a.mu.Unlock()
	for _, rc := range conns {
		_ = rc.Close()
	}
	a.state.writeStatus(Status{Edge: a.origin, Error: "server stopped", Since: time.Now()})
}

func (a *Agent) current() *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess
}

func (a *Agent) setSession(s *session) {
	a.mu.Lock()
	a.sess = s
	a.mu.Unlock()
}

// firstUse records a grant's connection id and refuses one already
// recorded. An id stays recorded for as long as any grant naming it could
// still verify.
func (a *Agent) firstUse(id string, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, until := range a.seen {
		if now.After(until) {
			delete(a.seen, k)
		}
	}
	if _, ok := a.seen[id]; ok {
		return fmt.Errorf("grant for %s was already used", id)
	}
	if len(a.seen) >= maxSeenGrants {
		return edgeproto.RefusalConnLimit
	}
	a.seen[id] = now.Add(edgeproto.GrantTTL + 2*edgeproto.ClockSkew)
	return nil
}

// RedeemWebCode exchanges a web sign-in code and the PKCE verifier the
// server kept for a web grant, over the control connection. The grant is
// verified against the pinned edge key before it is returned.
func (a *Agent) RedeemWebCode(ctx context.Context, code, verifier string) (edgeproto.Grant, error) {
	s := a.current()
	if s == nil {
		return edgeproto.Grant{}, fmt.Errorf("edgeagent: server is not connected to %s", a.origin)
	}
	id := edgeproto.NewConnID()
	reply := make(chan edgeproto.WebRedeemResult, 1)
	a.mu.Lock()
	a.waiters[id] = reply
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.waiters, id)
		a.mu.Unlock()
	}()
	if err := s.send(edgeproto.WebRedeem{ID: id, Code: code, Verifier: verifier}); err != nil {
		return edgeproto.Grant{}, fmt.Errorf("edgeagent: redeem web sign-in code: %w", err)
	}
	timer := time.NewTimer(redeemTimeout)
	defer timer.Stop()
	select {
	case r := <-reply:
		if r.Error != "" {
			return edgeproto.Grant{}, fmt.Errorf("edgeagent: %s refused the web sign-in code: %s", a.origin, r.Error)
		}
		g, err := edgeproto.VerifyGrant(s.edgeKey, r.Grant,
			edgeproto.GrantScope{ServerID: a.serverID, ConnID: id, Kind: edgeproto.KindWeb}, time.Now())
		if err != nil {
			return edgeproto.Grant{}, fmt.Errorf("edgeagent: web grant from %s: %w", a.origin, err)
		}
		return g, nil
	case <-s.done:
		return edgeproto.Grant{}, fmt.Errorf("edgeagent: control connection to %s closed before the edge answered", a.origin)
	case <-timer.C:
		return edgeproto.Grant{}, fmt.Errorf("edgeagent: %s did not answer the web sign-in within %s", a.origin, redeemTimeout)
	case <-ctx.Done():
		return edgeproto.Grant{}, ctx.Err()
	}
}

func (a *Agent) deliverRedeem(r edgeproto.WebRedeemResult) {
	a.mu.Lock()
	reply, ok := a.waiters[r.ID]
	delete(a.waiters, r.ID)
	a.mu.Unlock()
	if !ok {
		slog.Warn("edge: web sign-in answer for no pending request", "id", r.ID)
		return
	}
	reply <- r
}

func pinMismatch(pinned, offered ed25519.PublicKey) error {
	return fmt.Errorf("edge key changed: pinned %s, edge presents %s; if the edge operator rotated its key, run `aether-server edge trust`",
		edgeproto.EdgeKeyFingerprint(pinned), edgeproto.EdgeKeyFingerprint(offered))
}
