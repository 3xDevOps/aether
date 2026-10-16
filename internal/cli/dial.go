package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/3xDevOps/Aether/internal/version"
)

// ClientBanner is the SSH banner of every Aether client. The server reads
// the version from it to refuse a client older than its release.
func ClientBanner() string { return "SSH-2.0-Aether " + version.Version }

const (
	dialTimeout     = 10 * time.Second
	sshAgentTimeout = 500 * time.Millisecond
)

// dialAgent connects to the local SSH agent. Platform files supply the
// transport: a unix socket on POSIX, a named pipe on Windows. It is a var so
// tests can stand in for the host's agent.
var dialAgent = platformDialAgent

// Conn is a live SSH connection to an aether-server.
type Conn struct {
	client *ssh.Client
	cfg    Config
	// hostKey is the key the handshake checked against known_hosts. A link
	// with a server id records none: it accepts only the key deriving the id.
	hostKey ssh.PublicKey
}

// Dial connects to cfg.Addr as cfg.User (default aether) with the key
// ResolveAuth selects, verifying the host against known_hosts (TOFU on
// first contact). A server that needs no key connects even when no key
// was found; one that does gets an error saying what was tried. A link
// with a server id goes through DialLinked instead.
func Dial(cfg Config) (*Conn, error) {
	return DialContext(context.Background(), cfg)
}

// DialContext is Dial ended by ctx: cancelling it closes the connection
// mid-handshake.
func DialContext(ctx context.Context, cfg Config) (*Conn, error) {
	return dial(ctx, cfg, cfg.user(), false)
}

// DialInvite connects using SSH user invite:<code> (optionally with a
// display name) so an unknown key can join via a one-time invite.
func DialInvite(cfg Config, code, display string) (*Conn, error) {
	return dialInvite(context.Background(), cfg, code, display)
}

func dialInvite(ctx context.Context, cfg Config, code, display string) (*Conn, error) {
	user := "invite:" + code
	if display != "" {
		user += ":" + display
	}
	return dial(ctx, cfg, user, true)
}

func dial(ctx context.Context, cfg Config, user string, requireAuth bool) (*Conn, error) {
	if cfg.ServerID != "" {
		client, err := DialLinked(ctx, cfg, user)
		if err != nil {
			return nil, err
		}
		return &Conn{client: client, cfg: cfg}, nil
	}
	if cfg.Addr == "" {
		return nil, errors.New("cli: server address required")
	}
	auth := ResolveAuth(cfg)
	defer auth.Close()
	// An agent waiting for the person to confirm a key holds the handshake
	// until its own connection closes.
	defer context.AfterFunc(ctx, auth.Close)()
	if requireAuth && !auth.Offered() {
		return nil, auth.Missing()
	}
	cb, err := hostKeyCallback(cfg.knownHostsPath())
	if err != nil {
		return nil, err
	}
	var hostKey ssh.PublicKey
	conf := &ssh.ClientConfig{
		User: user,
		Auth: auth.Methods(),
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			hostKey = key
			return cb(hostname, remote, key)
		},
		BannerCallback: auth.Banner,
		Timeout:        dialTimeout,
	}
	nc, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("cli: dial %s: %w", cfg.Addr, err)
	}
	cc, chans, reqs, err := handshake(ctx, nc, cfg.Addr, conf)
	if err != nil {
		_ = nc.Close()
		return nil, auth.Explain(fmt.Errorf("cli: ssh handshake with %s: %w", cfg.Addr, err))
	}
	return &Conn{client: ssh.NewClient(cc, chans, reqs), cfg: cfg, hostKey: hostKey}, nil
}

// handshake runs the SSH handshake over nc until ctx ends, which closes nc.
// The connection it returns remembers when nc last delivered bytes.
func handshake(ctx context.Context, nc net.Conn, addr string, conf *ssh.ClientConfig) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	read := &readClock{Conn: nc}
	conf.ClientVersion = ClientBanner()
	cc, chans, reqs, err := ssh.NewClientConn(read, addr, conf)
	if !stop() {
		if err == nil {
			_ = cc.Close()
		}
		return nil, nil, nil, ctx.Err()
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return &clockedConn{Conn: cc, read: read}, chans, reqs, nil
}

type readClock struct {
	net.Conn
	last atomic.Int64
}

func (r *readClock) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	if n > 0 {
		r.last.Store(time.Now().UnixNano())
	}
	return n, err
}

type clockedConn struct {
	ssh.Conn
	read *readClock
}

// LastRead is when the server last sent anything on the connection: a
// keepalive reply or any channel's data. A busy connection on a slow link
// keeps delivering bytes while a keepalive reply queues behind them.
func (c *Conn) LastRead() time.Time {
	clocked, ok := c.client.Conn.(*clockedConn)
	if !ok {
		return time.Time{}
	}
	return time.Unix(0, clocked.read.last.Load())
}

// Ping waits for the server to answer a keepalive until ctx ends. A
// connection whose network went away without a reset never answers, and
// every channel opened on it waits as long; the caller closes it.
func (c *Conn) Ping(ctx context.Context) error {
	answered := make(chan error, 1)
	go func() {
		_, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil)
		answered <- err
	}()
	select {
	case err := <-answered:
		return err
	case <-ctx.Done():
		return fmt.Errorf("cli: keepalive unanswered: %w", ctx.Err())
	}
}

// Close tears down the SSH connection.
func (c *Conn) Close() error { return c.client.Close() }

// SSH is the underlying SSH client (port-forwards, extra sessions).
func (c *Conn) SSH() *ssh.Client { return c.client }

func hostKeyCallback(path string) (ssh.HostKeyCallback, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cli: known_hosts dir: %w", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return nil, fmt.Errorf("cli: create known_hosts: %w", err)
		}
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("cli: known_hosts: %w", err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			return appendKnownHost(path, hostname, remote, key)
		}
		return err
	}, nil
}

func appendKnownHost(path, hostname string, remote net.Addr, key ssh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cli: update known_hosts: %w", err)
	}
	defer func() { _ = f.Close() }()
	addrs := []string{hostname}
	if remote != nil {
		addrs = append(addrs, remote.String())
	}
	line := knownhosts.Line(addrs, key)
	if _, err := fmt.Fprintln(f, line); err != nil {
		return fmt.Errorf("cli: update known_hosts: %w", err)
	}
	fmt.Fprintf(os.Stderr, "aether: trusting new host %s (%s) - added to %s\n",
		hostname, ssh.FingerprintSHA256(key), path)
	return nil
}
