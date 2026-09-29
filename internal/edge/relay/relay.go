// Package relay is the edge's relay. It enrolls servers over their control
// channel and splices client SSH connections to them without decrypting
// them. docs/edge.md describes the edge; internal/edge/proto holds the wire
// contract.
package relay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/coder/websocket"
)

// Directory is what the relay needs from the edge's account service.
// Every error it returns for a refusal is an edgeproto.Refusal; any other
// error is logged and answered as an internal error.
type Directory interface {
	// Authenticate returns the account and device a device token belongs
	// to, or edgeproto.RefusalTokenRevoked for a token it does not hold.
	Authenticate(ctx context.Context, token string) (edgeproto.Account, edgeproto.Device, error)
	// Admit returns nil when account may open an ssh connection to
	// serverID, edgeproto.RefusalUnknownServer when serverID is not
	// claimed, and edgeproto.RefusalNotMember otherwise.
	Admit(ctx context.Context, serverID string, account edgeproto.Account) error
	// AdmitClaim returns nil when account, connecting from addr, may open
	// a claim connection to serverID: edgeproto.RefusalTooMany past a
	// limit per account, per address or per server, and
	// edgeproto.RefusalClaimed when serverID has an owner.
	AdmitClaim(ctx context.Context, serverID string, account edgeproto.Account, addr netip.Addr) error
	// Enroll reports whether serverID is claimed, and records name and
	// policy, from its hello, for a server that is. It returns
	// edgeproto.RefusalServerBlocked for a server id the operator
	// blocked.
	Enroll(ctx context.Context, serverID, name string, policy edgeproto.AccessPolicy) (claimed bool, err error)
	// ReplaceDirectory stores the directory a claimed server pushed.
	ReplaceDirectory(ctx context.Context, serverID string, entries []edgeproto.DirectoryEntry) error
	// Unenroll forgets serverID after its operator ran
	// `aether-server edge leave`.
	Unenroll(ctx context.Context, serverID string) error
	// RecordClaim records owner as the owner of serverID, which has none,
	// after serverID reported that a claim connection the relay opened
	// for owner presented its claim code. name and policy are from its
	// hello.
	RecordClaim(ctx context.Context, serverID, name string, policy edgeproto.AccessPolicy, owner edgeproto.Account) error
	// TransferOwner records owner as the owner of the claimed server
	// serverID, which reported the transfer.
	TransferOwner(ctx context.Context, serverID string, owner edgeproto.Principal) error
	// DropOwner records that the claimed server serverID reported it has
	// no owner.
	DropOwner(ctx context.Context, serverID string) error
	// PendingDeletions returns the account deletions owed to serverID,
	// and DeletionApplied forgets one once serverID applied it.
	PendingDeletions(ctx context.Context, serverID string) ([]edgeproto.AccountDeleted, error)
	DeletionApplied(ctx context.Context, serverID string, d edgeproto.AccountDeleted) error
}

// EgressStore persists the monthly egress counter so the budget survives
// a restart. A month is "2006-01" in UTC.
type EgressStore interface {
	Egress(ctx context.Context, month string) (int64, error)
	AddEgress(ctx context.Context, month string, n int64) error
}

// Config configures a Relay.
type Config struct {
	// Origin is the edge's relay origin. Servers sign it to enroll, and
	// grants name it as their issuer.
	Origin string
	// EdgeKey signs grants. Servers pin its public half.
	EdgeKey   ed25519.PrivateKey
	Directory Directory
	Egress    EgressStore
	// EgressBudget is how many bytes the relay sends in a calendar month
	// (UTC) before it throttles every splice. Zero means no budget.
	EgressBudget int64
}

const (
	handshakeTimeout = 10 * time.Second
	writeTimeout     = 10 * time.Second
	directoryTimeout = 10 * time.Second
	// directoryInterval is the least time between two stores of one
	// server's directory. A store holds the edge database's write lock,
	// which sign-ins and connections of every account also take, so no
	// server may take it at will; a push in between waits, and only the
	// latest waiting push is stored.
	directoryInterval = 5 * time.Second
	egressFlushEvery  = time.Minute
	// throttledRate is the bytes per second every splice together may
	// carry once the egress budget is spent: dozens of terminals, no bulk
	// transfer, and at most about 650 GiB over a 31-day month.
	throttledRate = 256 << 10
	// throttledChunk bounds one throttled read, and so how long a read of
	// one server holds back another server's.
	throttledChunk = 4 << 10
	// maxUnclaimed bounds unclaimed registrations edge-wide. Each holds a
	// control socket for up to UnclaimedTTL, and the limits per address
	// block do not bound a sender holding many blocks.
	maxUnclaimed = 10000
	// maxConnsPerAddress bounds the public listener's open connections
	// from one edgeproto.RateLimitKey block. It sits far above what an
	// office behind one NAT address holds: MaxConnsPerDevice SSH streams
	// per install, plus sign-in pages.
	maxConnsPerAddress = 1024
)

// Relay is the edge relay. Register its HTTP endpoints on the edge's mux
// and serve the edge's HTTPS on Listener.
type Relay struct {
	origin  string
	key     ed25519.PrivateKey
	pub     ed25519.PublicKey
	dir     Directory
	store   EgressStore
	budget  int64
	ctx     context.Context
	stop    context.CancelFunc
	flushed chan struct{}

	// Durations, rates and limits that tests shorten.
	attachDeadline     time.Duration
	unclaimedTTL       time.Duration
	pingInterval       time.Duration
	idleTimeout        time.Duration
	directoryInterval  time.Duration
	throttleRate       int64
	maxUnclaimed       int
	maxConnsPerAddress int

	mu      sync.Mutex
	closing bool
	servers map[string]*registration
	conns   map[string]*relayConn
	// claims holds, by connection id, the claim connections a server may
	// still report as claimed.
	claims map[string]claimGrant
	// directories holds, per server id, the directory waiting to be
	// stored.
	directories map[string]*directoryWrite

	addrMu    sync.Mutex
	addrConns map[netip.Prefix]int

	pace pacer

	egressMu    sync.Mutex
	month       string
	monthBytes  atomic.Int64
	unflushed   atomic.Int64
	bytes       atomic.Uint64
	refusalsMu  sync.Mutex
	refusalsMap map[string]uint64
}

// New returns a relay and starts its egress accounting.
func New(ctx context.Context, cfg Config) (*Relay, error) {
	origin, err := edgeproto.Origin(cfg.Origin)
	if err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	switch {
	case len(cfg.EdgeKey) != ed25519.PrivateKeySize:
		return nil, fmt.Errorf("relay: edge key is %d bytes, want %d", len(cfg.EdgeKey), ed25519.PrivateKeySize)
	case cfg.Directory == nil || cfg.Egress == nil:
		return nil, errors.New("relay: directory and egress store are required")
	case cfg.EgressBudget < 0:
		return nil, fmt.Errorf("relay: egress budget %d is negative", cfg.EgressBudget)
	}
	month := monthOf(time.Now())
	used, err := cfg.Egress.Egress(ctx, month)
	if err != nil {
		return nil, fmt.Errorf("relay: load egress for %s: %w", month, err)
	}
	r := &Relay{
		origin:             origin,
		key:                cfg.EdgeKey,
		pub:                cfg.EdgeKey.Public().(ed25519.PublicKey),
		dir:                cfg.Directory,
		store:              cfg.Egress,
		budget:             cfg.EgressBudget,
		flushed:            make(chan struct{}),
		attachDeadline:     edgeproto.AttachDeadline,
		unclaimedTTL:       edgeproto.UnclaimedTTL,
		pingInterval:       edgeproto.PingInterval,
		idleTimeout:        edgeproto.ControlIdleTimeout,
		directoryInterval:  directoryInterval,
		throttleRate:       throttledRate,
		maxUnclaimed:       maxUnclaimed,
		maxConnsPerAddress: maxConnsPerAddress,
		servers:            map[string]*registration{},
		conns:              map[string]*relayConn{},
		claims:             map[string]claimGrant{},
		directories:        map[string]*directoryWrite{},
		addrConns:          map[netip.Prefix]int{},
		month:              month,
		refusalsMap:        map[string]uint64{},
	}
	r.monthBytes.Store(used)
	r.ctx, r.stop = context.WithCancel(context.Background())
	go r.flushLoop()
	return r, nil
}

// Register adds the control, data, connect and claim endpoints to mux,
// which serves the relay origin.
func (r *Relay) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+edgeproto.PathServerControl, r.serveControl)
	mux.HandleFunc("GET "+edgeproto.PathServerData, r.serveData)
	mux.HandleFunc("GET "+edgeproto.PathConnect, func(w http.ResponseWriter, req *http.Request) {
		r.serveConnect(w, req, edgeproto.KindSSH)
	})
	mux.HandleFunc("GET "+edgeproto.PathClaimConnect, func(w http.ResponseWriter, req *http.Request) {
		r.serveConnect(w, req, edgeproto.KindClaim)
	})
}

// Compression is off: the streams are already encrypted end to end, and
// compressing attacker-influenced data beside secrets is how CRIME works.
var acceptOptions = &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled}

// Online reports whether serverID is claimed and holds a control channel.
func (r *Relay) Online(serverID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg := r.servers[serverID]
	return reg != nil && reg.claimed
}

// Metrics is a snapshot of the relay's counters.
type Metrics struct {
	// Servers counts claimed servers holding a control channel;
	// UnclaimedServers the unclaimed ones.
	Servers          int
	UnclaimedServers int
	// Splices counts attached connections.
	Splices int
	// BytesRelayed counts bytes sent to either side since the relay
	// started; EgressThisMonth the calendar month's total, persisted.
	BytesRelayed    uint64
	EgressThisMonth int64
	Throttled       bool
	// Refusals counts refused connections and enrollments by reason.
	Refusals map[string]uint64
}

// Metrics returns the relay's counters.
func (r *Relay) Metrics() Metrics {
	var m Metrics
	r.mu.Lock()
	for _, reg := range r.servers {
		if reg.claimed {
			m.Servers++
		} else {
			m.UnclaimedServers++
		}
	}
	for _, c := range r.conns {
		if c.spliced {
			m.Splices++
		}
	}
	r.mu.Unlock()
	m.BytesRelayed = r.bytes.Load()
	m.EgressThisMonth = r.monthBytes.Load()
	m.Throttled = r.throttled()
	r.refusalsMu.Lock()
	m.Refusals = make(map[string]uint64, len(r.refusalsMap))
	for k, v := range r.refusalsMap {
		m.Refusals[k] = v
	}
	r.refusalsMu.Unlock()
	return m
}

func (r *Relay) countRefusal(reason string) {
	r.refusalsMu.Lock()
	r.refusalsMap[reason]++
	r.refusalsMu.Unlock()
}

// RevokeDevice closes every live connection of a device whose token was
// revoked and tells each server it reached.
func (r *Relay) RevokeDevice(deviceID string) {
	servers := r.closeConns(func(c *relayConn) bool { return c.deviceID == deviceID }, edgeproto.RefusalTokenRevoked)
	for _, reg := range servers {
		if err := reg.send(edgeproto.DeviceRevoked{DeviceID: deviceID}); err != nil {
			slog.Warn("relay: device revocation not delivered", "server", reg.id, "error", err)
		}
	}
}

// AccountDeleted closes every live connection of the deleted account a and
// sends each connected server among servers the deletions it is owed.
func (r *Relay) AccountDeleted(a edgeproto.Account, servers []string) {
	r.closeConns(func(c *relayConn) bool { return sameAccount(c.account, a) }, edgeproto.RefusalTokenRevoked)
	r.mu.Lock()
	var regs []*registration
	for _, id := range servers {
		if reg := r.servers[id]; reg != nil {
			regs = append(regs, reg)
		}
	}
	r.mu.Unlock()
	for _, reg := range regs {
		go r.deliverDeletions(reg)
	}
}

func sameAccount(a, b edgeproto.Account) bool {
	return a.Provider == b.Provider && a.Subject == b.Subject
}

// CloseServer closes every live connection to serverID.
func (r *Relay) CloseServer(serverID string) {
	r.closeConns(func(c *relayConn) bool { return c.serverID == serverID }, edgeproto.RefusalNotConnected)
}

// Unenroll tells serverID that its owner removed it at the edge, closes
// its control channel and closes its connections.
func (r *Relay) Unenroll(serverID string) {
	r.mu.Lock()
	reg := r.servers[serverID]
	r.mu.Unlock()
	if reg != nil {
		if err := reg.send(edgeproto.Unenroll{}); err != nil {
			slog.Warn("relay: unenroll not delivered", "server", serverID, "error", err)
		}
		reg.cancel(errUnenrolled)
	}
	r.CloseServer(serverID)
}

// closeConns cancels the connections match selects with cause and returns
// the registrations of the servers they were for.
func (r *Relay) closeConns(match func(*relayConn) bool, cause error) []*registration {
	r.mu.Lock()
	defer r.mu.Unlock()
	var regs []*registration
	seen := map[string]bool{}
	for _, c := range r.conns {
		if !match(c) {
			continue
		}
		c.cancel(cause)
		if reg := r.servers[c.serverID]; reg != nil && !seen[c.serverID] {
			seen[c.serverID] = true
			regs = append(regs, reg)
		}
	}
	return regs
}

// Shutdown sends drain to every server, closes every control channel and
// connection, and saves the egress counter.
func (r *Relay) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.closing = true
	regs := make([]*registration, 0, len(r.servers))
	for _, reg := range r.servers {
		regs = append(regs, reg)
	}
	for _, c := range r.conns {
		c.cancel(edgeproto.RefusalNotConnected)
	}
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, reg := range regs {
		wg.Go(func() {
			if err := reg.send(edgeproto.Drain{}); err != nil {
				slog.Warn("relay: drain not delivered", "server", reg.id, "error", err)
			}
			reg.cancel(errDraining)
		})
	}
	wg.Wait()
	r.stop()
	<-r.flushed
	return r.flushEgress(ctx, time.Now())
}
