// Package edgeagent keeps an Aether server's control connection to an edge:
// it enrolls with the host key, pins the edge's grant key, relays the SSH
// and claim connections the edge opens, pushes the server's directory and
// reports its owner. docs/edge.md describes the edge.
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

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/version"
	"golang.org/x/crypto/ssh"
)

// DefaultURL is the edge the project runs.
const DefaultURL = "https://edge.onaether.dev"

// SSH is the part of the SSH server the agent hands relayed connections
// and the edge's notices to. *sshd.Server implements it.
type SSH interface {
	// ServeEdgeConn serves one relayed connection whose ssh grant the
	// agent verified, and returns when the connection ends.
	ServeEdgeConn(ctx context.Context, nc net.Conn, grant edgeproto.Grant)
	// ServeEdgeClaim serves one relayed connection whose claim grant the
	// agent verified. attempt checks the claim code the client presents
	// inside SSH and runs claim when it matches.
	ServeEdgeClaim(ctx context.Context, nc net.Conn, grant edgeproto.Grant, attempt func(code string, claim func() error) error)
	// EdgeAccountDeleted removes the identity of an account the edge
	// deleted, and the devices registered through it.
	EdgeAccountDeleted(ctx context.Context, provider, subject string) error
	EdgeDirectory(ctx context.Context) ([]edgeproto.DirectoryEntry, error)
	EdgeDirectoryChanged() <-chan struct{}
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
	// SSH receives relayed connections and the edge's notices. Leave
	// needs none.
	SSH SSH
	// AccessPolicy is the server's edge-access setting, which the agent
	// announces in its hello for display at the edge. Empty is
	// PolicyApprovedDevices. The server enforces it; the edge's copy is
	// never an input to that.
	AccessPolicy edgeproto.AccessPolicy
}

const (
	handshakeTimeout = 10 * time.Second
	writeTimeout     = 10 * time.Second
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
	policy   edgeproto.AccessPolicy
	state    *State

	// Timings are fields so tests can shorten them.
	minBackoff, maxBackoff    time.Duration
	pingInterval, idleTimeout time.Duration
	attachDeadline            time.Duration

	// slots holds one entry per relayed connection.
	slots chan struct{}

	mu     sync.Mutex
	runCtx context.Context
	conns  map[*relayConn]struct{}
	seen   map[string]time.Time
	// live is the enrolled control connection, nil between connections.
	live *session
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
	policy, err := edgeproto.ParseAccessPolicy(string(cfg.AccessPolicy))
	if err != nil {
		return nil, fmt.Errorf("edgeagent: %w", err)
	}
	state, err := OpenState(cfg.DataDir, origin)
	if err != nil {
		return nil, err
	}
	// The hostname only labels the server for its owner at the edge.
	name, _ := os.Hostname()
	// Encoding a hello now turns a hostname the edge would refuse into a
	// startup error instead of a retry loop.
	probe := edgeproto.Hello{Version: edgeproto.Version, HostKey: cfg.HostKey.PublicKey().Marshal(),
		AgentVersion: version.Version, Name: name, AccessPolicy: policy}
	if _, err := edgeproto.EncodeControl(probe); err != nil {
		return nil, fmt.Errorf("edgeagent: hostname %q or version %q: %w", name, version.Version, err)
	}
	return &Agent{
		cfg:            cfg,
		name:           name,
		origin:         origin,
		serverID:       edgeproto.ServerID(cfg.HostKey.PublicKey()),
		policy:         policy,
		state:          state,
		minBackoff:     edgeproto.ReconnectMinBackoff,
		maxBackoff:     edgeproto.ReconnectMaxBackoff,
		pingInterval:   edgeproto.PingInterval,
		idleTimeout:    edgeproto.ControlIdleTimeout,
		attachDeadline: edgeproto.AttachDeadline,
		slots:          make(chan struct{}, edgeproto.MaxSSHConnsPerServer),
		conns:          make(map[*relayConn]struct{}),
		seen:           make(map[string]time.Time),
	}, nil
}

// ServerID is this server's id at the edge.
func (a *Agent) ServerID() string { return a.serverID }

// Run keeps the control connection until ctx is done, reconnecting with
// jittered exponential backoff. It never gives up: an unreachable or
// misbehaving edge is logged and retried, and leaves the rest of the server
// untouched. Run closes the relayed connections when it returns.
func (a *Agent) Run(ctx context.Context) {
	a.mu.Lock()
	a.runCtx = ctx
	a.mu.Unlock()
	defer a.stop()
	a.logPolicy()

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

// logPolicy writes the access policy to the server's log, with the
// previous one when it changed since the last start. The policy changes
// only on this host, so this is the record of every change.
func (a *Agent) logPolicy() {
	prev, err := swapPolicy(a.cfg.DataDir, a.policy)
	switch {
	case err != nil:
		slog.Error("edge: record the access policy", "policy", a.policy, "error", err)
	case prev != "" && prev != a.policy:
		slog.Warn("edge: access policy changed since the last start", "from", prev, "to", a.policy)
	default:
		slog.Info("edge: access policy", "policy", a.policy)
	}
}

func pinMismatch(pinned, offered ed25519.PublicKey) error {
	return fmt.Errorf("edge key changed: pinned %s, edge presents %s; if the edge operator rotated its key, run `aether-server edge trust`",
		edgeproto.EdgeKeyFingerprint(pinned), edgeproto.EdgeKeyFingerprint(offered))
}
