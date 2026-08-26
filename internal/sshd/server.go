// Package sshd is the embedded SSH server: the single transport every
// client rides. It authenticates members - by Tailscale WhoIs identity
// when a resolver is configured, falling back to public key against the
// store - and multiplexes git transport (exec), the JSON-RPC control
// channel, the event stream, PTY attach (subsystems), and the dashboard
// port-forward (direct-tcpip) over one port. It owns no run lifecycle,
// git, or PTY logic - everything mutating state is delegated through the
// consumer-side seam interfaces or the store.
package sshd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/toolenv"
)

// memberIDExtension is the ssh.Permissions extension carrying the
// authenticated member's ID.
const memberIDExtension = "aether-member-id"

// bootstrapKeyExtension is the ssh.Permissions extension carrying the
// marshaled authorized key of a fresh-server bootstrap candidate. The
// member row is created only after the handshake completes, because the
// publickey callback also fires for unsigned acceptability probes and
// must stay side-effect-free for unproven keys.
const bootstrapKeyExtension = "aether-bootstrap-key"

// inviteCodeExtension / inviteKeyExtension carry a validated invite code
// and the candidate public key until handleConn burns the invite and
// creates the collaborator (same deferred-write pattern as bootstrap).
const (
	inviteCodeExtension = "aether-invite-code"
	inviteKeyExtension  = "aether-invite-key"
)

const (
	// defaultHandshakeTimeout bounds how long an unauthenticated TCP
	// connection may take to complete the SSH handshake (LoginGraceTime
	// analogue).
	defaultHandshakeTimeout = 30 * time.Second
	// defaultMaxHandshakes caps concurrent pre-auth handshakes
	// (MaxStartups analogue); connections over the cap are shed.
	defaultMaxHandshakes = 64
	// authTimeout bounds the store lookup in the publickey callback so a
	// stalled store cannot pin handshakes forever.
	authTimeout = 10 * time.Second
)

// Config wires the server to its collaborators. All collaborators are
// required.
type Config struct {
	Addr          string // default ":2222"
	HostKeyPath   string // <data>/ssh/host_ed25519_key; generated on first start if absent
	DashboardPort int    // direct-tcpip forwards allowed only to 127.0.0.1:this; 0 = deny all forwards
	Store         store.Store
	Bus           events.Bus
	Git           GitTransport
	PTY           PTYAttacher
	Runs          RunController

	// Toolenv provides server-owned snapshot lifecycle operations for SSH
	// control methods. It is optional in narrow unit-test configurations.
	Toolenv *toolenv.Manager

	// WhoIs resolves connections to tailnet identities via tailscaled;
	// nil disables tailnet auth entirely (key auth only). WhoIs runs
	// once per connection, in the "none" auth attempt every SSH client
	// sends first; on any failure auth falls back to the publickey path.
	WhoIs WhoIsResolver
	// TailnetAutoJoin registers unknown tailnet identities as approved
	// members instead of pending ones.
	TailnetAutoJoin bool
	// TailnetRequireKey additionally requires pubkey verification on
	// tailnet connections (second factor for shared machines).
	TailnetRequireKey bool
	// TailnetHostname is the server's MagicDNS name, discovered once at
	// startup by the reachability package; empty when the server is not
	// on a tailnet. Reported verbatim by server.info.
	TailnetHostname string

	// InvitesDir holds one-time invite files (<data>/invites). Empty
	// disables member.invite and invite-code joins.
	InvitesDir string

	// Profiles is the agent-profile snapshot service. Nil disables the
	// profile.push / profile.status / profile.rollback methods.
	Profiles ProfileService

	// Services carries the team-feature service seams; see services.go.
	// Each nil field disables its methods with CodeUnavailable.
	Services Services

	// handshakeTimeout bounds the pre-auth SSH handshake; zero means
	// defaultHandshakeTimeout. Unexported: a knob for tests only.
	handshakeTimeout time.Duration
	// maxHandshakes caps concurrent pre-auth handshakes (MaxStartups
	// analogue); zero means defaultMaxHandshakes. Unexported test knob.
	maxHandshakes int
	// syncHandshakeTimeout bounds the aether-sync setup handshake; zero
	// means defaultSyncHandshakeTimeout. Unexported test knob.
	syncHandshakeTimeout time.Duration
	// syncRevalidateInterval is how often a live sync bridge re-checks
	// its authorization; zero means defaultSyncRevalidateInterval.
	// Unexported test knob.
	syncRevalidateInterval time.Duration
}

// Server is the embedded SSH server.
type Server struct {
	cfg        Config
	sshCfg     *ssh.ServerConfig
	handshakes chan struct{}

	// wg counts every handler goroutine (per-connection, per-channel, and
	// per-subsystem); Close waits on it so no handler outlives shutdown.
	wg sync.WaitGroup

	// registerMu serializes membership writes that depend on a prior read
	// of the member table: two concurrent first contacts must not both
	// observe an empty table and both bootstrap as admin, and two
	// concurrent demotions must not both observe two admins and leave the
	// deployment with none.
	registerMu sync.Mutex

	mu    sync.Mutex
	ln    net.Listener
	conns map[net.Conn]struct{}
	// syncChannels counts each member's live aether-sync channels, for
	// the per-member concurrency cap (see claimSyncChannel).
	syncChannels map[domain.MemberID]int
	// mintBuckets rate-limits each member's dash.token.mint calls;
	// created lazily so the dashboard Bridge's bare Server gets one too.
	mintBuckets map[domain.MemberID]*mintBucket
	closed      bool
	baseCtx     context.Context
}

// New builds a server, loading (or generating) the host key.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = ":2222"
	}
	if cfg.HostKeyPath == "" {
		return nil, errors.New("sshd: config requires HostKeyPath")
	}
	if cfg.Store == nil || cfg.Bus == nil || cfg.Git == nil || cfg.PTY == nil || cfg.Runs == nil {
		return nil, errors.New("sshd: config requires Store, Bus, Git, PTY, and Runs")
	}
	if cfg.handshakeTimeout <= 0 {
		cfg.handshakeTimeout = defaultHandshakeTimeout
	}
	if cfg.maxHandshakes <= 0 {
		cfg.maxHandshakes = defaultMaxHandshakes
	}
	if cfg.syncHandshakeTimeout <= 0 {
		cfg.syncHandshakeTimeout = defaultSyncHandshakeTimeout
	}
	if cfg.syncRevalidateInterval <= 0 {
		cfg.syncRevalidateInterval = defaultSyncRevalidateInterval
	}
	signer, err := LoadOrCreateHostKey(cfg.HostKeyPath)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:          cfg,
		handshakes:   make(chan struct{}, cfg.maxHandshakes),
		conns:        make(map[net.Conn]struct{}),
		syncChannels: make(map[domain.MemberID]int),
		baseCtx:      context.Background(),
	}
	sc := &ssh.ServerConfig{PublicKeyCallback: s.authenticate}
	if cfg.WhoIs != nil {
		sc.NoClientAuth = true
		sc.NoClientAuthCallback = s.authenticateTailnet
	}
	sc.AddHostKey(signer)
	s.sshCfg = sc
	return s, nil
}

func (s *Server) authenticate(cm ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(s.authCtx(), authTimeout)
	defer cancel()
	keyLine := string(ssh.MarshalAuthorizedKey(key))
	m, err := s.cfg.Store.GetMemberByPublicKey(ctx, keyLine)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("sshd: resolve public key: %w", err)
		}
		// Fresh server: accept the key for admin bootstrap, but create
		// nothing yet. x/crypto/ssh also calls this callback for the
		// unsigned acceptability probe, so any store write here would
		// let an attacker wedge bootstrap with a key nobody holds. The
		// registration happens in handleConn, after the handshake has
		// proven possession of the private key.
		fresh, ferr := s.storeIsFresh(ctx)
		if ferr != nil {
			return nil, ferr
		}
		if fresh {
			return &ssh.Permissions{Extensions: map[string]string{bootstrapKeyExtension: keyLine}}, nil
		}
		if cm != nil {
			if code, _, ok := parseInviteUser(cm.User()); ok && s.inviteUsable(code) {
				return &ssh.Permissions{Extensions: map[string]string{
					inviteCodeExtension: code,
					inviteKeyExtension:  keyLine,
				}}, nil
			}
		}
		return nil, &ssh.BannerError{
			Err:     errors.New("sshd: unknown public key"),
			Message: "no Aether member for this key\n",
		}
	}
	return &ssh.Permissions{Extensions: map[string]string{memberIDExtension: string(m.ID)}}, nil
}

// storeIsFresh reports whether no member exists yet (bootstrap window).
func (s *Server) storeIsFresh(ctx context.Context) (bool, error) {
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return false, fmt.Errorf("sshd: bootstrap: %w", err)
	}
	return len(members) == 0, nil
}

// bootstrapKeyMember registers the first key to contact a fresh server as
// the admin member (zero-setup bootstrap; wave 1 contract §9.5). Called
// only after the SSH handshake has verified the key's signature.
// Freshness is re-checked under registerMu: if another identity won the
// race the connection is rejected, exactly as an unknown key would be.
func (s *Server) bootstrapKeyMember(ctx context.Context, user, keyLine string) (*domain.Member, error) {
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	fresh, err := s.storeIsFresh(ctx)
	if err != nil {
		return nil, err
	}
	if !fresh {
		return nil, store.ErrNotFound
	}
	m := &domain.Member{
		DisplayName: "admin",
		PublicKey:   keyLine,
		Color:       attribution.NextColor(nil),
		Role:        domain.RoleAdmin,
	}
	if err := s.cfg.Store.CreateMember(ctx, m); err != nil {
		return nil, fmt.Errorf("sshd: bootstrap: %w", err)
	}
	slog.Info("sshd: bootstrap: first member registered as admin",
		"member", m.ID, "key", fingerprintOf(keyLine), "user", user, "method", "publickey")
	return m, nil
}

// fingerprintOf renders an authorized_keys line as a SHA256 fingerprint
// for audit logs, falling back to the raw line on parse failure.
func fingerprintOf(keyLine string) string {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keyLine))
	if err != nil {
		return keyLine
	}
	return ssh.FingerprintSHA256(pk)
}

// Serve listens on Addr and serves connections until ctx is done or the
// server is closed. Canceling ctx shuts the server down (equivalent to
// Close): the listener and every established connection are closed. A
// subsequent Close blocks until every handler goroutine has returned.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("sshd: listen %s: %w", s.cfg.Addr, err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return errors.New("sshd: server closed")
	}
	s.ln = ln
	s.baseCtx = ctx
	s.mu.Unlock()

	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || s.isClosed() {
				return nil
			}
			return fmt.Errorf("sshd: accept: %w", err)
		}
		if !s.beginHandler() {
			_ = conn.Close()
			continue
		}
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

// beginHandler registers a goroutine with the shutdown WaitGroup unless
// the server is closed. The Add happens under mu — Close flips closed
// before it waits, so an Add can never race its Wait.
func (s *Server) beginHandler() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

// spawn runs fn on a goroutine tracked by the shutdown WaitGroup. Only
// call from a goroutine that already holds a tracked slot, so the counter
// stays positive while the Add happens.
func (s *Server) spawn(fn func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

// Addr returns the listening address, or nil before Serve has bound it.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Close stops listening, closes every active connection, and waits for
// all in-flight handler goroutines to return, so after Close no handler
// can still be calling into the store or the seam collaborators.
func (s *Server) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.ln != nil {
			_ = s.ln.Close()
		}
		for c := range s.conns {
			_ = c.Close()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// authCtx is the context the publickey callback derives its store lookup
// from: the Serve context once serving, Background before.
func (s *Server) authCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseCtx
}

func (s *Server) trackConn(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrackConn(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

func (s *Server) handleConn(ctx context.Context, c net.Conn) {
	if !s.trackConn(c) {
		_ = c.Close()
		return
	}
	defer s.untrackConn(c)
	defer func() { _ = c.Close() }()

	// Shed connections over the concurrent-handshake cap and bound the
	// pre-auth handshake so stalled clients cannot pin goroutines.
	select {
	case s.handshakes <- struct{}{}:
	default:
		return
	}
	_ = c.SetDeadline(time.Now().Add(s.cfg.handshakeTimeout))
	sconn, chans, reqs, err := ssh.NewServerConn(c, s.sshCfg)
	<-s.handshakes
	if err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	defer func() { _ = sconn.Close() }()
	member := domain.MemberID(sconn.Permissions.Extensions[memberIDExtension])

	// Signature is proven now: perform any deferred bootstrap or invite
	// registration. Losing the freshness race mid-handshake is handled
	// exactly like an unknown key - the connection is dropped.
	if member == "" {
		if code, ok := sconn.Permissions.Extensions[inviteCodeExtension]; ok {
			keyLine := sconn.Permissions.Extensions[inviteKeyExtension]
			_, display, _ := parseInviteUser(sconn.User())
			m, jerr := s.joinInviteMember(ctx, code, keyLine, display)
			if jerr != nil {
				slog.Warn("sshd: invite join failed; dropping connection", "error", jerr)
				return
			}
			member = m.ID
		} else if keyLine, ok := sconn.Permissions.Extensions[bootstrapKeyExtension]; ok {
			m, berr := s.bootstrapKeyMember(ctx, sconn.User(), keyLine)
			if berr != nil {
				slog.Warn("sshd: deferred key bootstrap failed; dropping connection", "error", berr)
				return
			}
			member = m.ID
		}
	}

	// Deny every global request, including tcpip-forward (no reverse
	// forwarding).
	s.spawn(func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	})

	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			s.spawn(func() { s.handleSession(ctx, member, nc) })
		case "direct-tcpip":
			s.spawn(func() { s.handleDirectTCPIP(ctx, member, nc) })
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
}

// handleDirectTCPIP serves the dashboard port-forward: the only allowed
// destination is 127.0.0.1:DashboardPort.
func (s *Server) handleDirectTCPIP(ctx context.Context, member domain.MemberID, nc ssh.NewChannel) {
	var p struct {
		DestAddr string
		DestPort uint32
		OrigAddr string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "malformed direct-tcpip request")
		return
	}
	if s.cfg.DashboardPort == 0 || int(p.DestPort) != s.cfg.DashboardPort || !isLoopback(p.DestAddr) {
		_ = nc.Reject(ssh.Prohibited, "forwarding allowed only to the dashboard port")
		return
	}
	if err := s.checkMember(ctx, member); err != nil {
		_ = nc.Reject(ssh.Prohibited, "member no longer authorized")
		return
	}
	dst, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.cfg.DashboardPort)))
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "dashboard unreachable")
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		_ = dst.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	pipe(ch, dst)
}

func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}
