// Package syncd is the local git sync daemon: a pure SSH client that
// follows the server's event stream and mirrors server-owned run branches
// (aether/run-*) into the local clone's remote-tracking namespace as they
// move, pushes local base-branch updates up on a poll, and reconnects with
// exponential backoff. It only ever moves refs - the working tree is never
// touched.
package syncd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	dialTimeout            = 10 * time.Second
	keepaliveInterval      = 30 * time.Second
	defaultPushInterval    = 15 * time.Second
	defaultCatchupInterval = 5 * time.Minute
	backoffBase            = time.Second
	backoffMax             = time.Minute
	// stableSession is how long an acknowledged session must last before
	// the reconnect backoff resets; an ack followed by an immediate drop
	// keeps the backoff climbing instead of hammering the server.
	stableSession = 30 * time.Second
)

// Config configures a Daemon. Server and RepoPath are required; everything
// else has a working default.
type Config struct {
	// Server is the aether-server SSH address, host:port.
	Server string
	// KeyPath is the member's SSH private key file. Empty means the same
	// discovery the CLI uses: the SSH agent, then the default files under
	// ~/.ssh.
	KeyPath string
	// KnownHostsPath is the OpenSSH known_hosts file used to verify the
	// server host key.
	KnownHostsPath string
	// User is the SSH username; the server authenticates by key only.
	User string
	// RepoPath is the local git repository to sync.
	RepoPath string
	// Remote is the git remote name pointing at the server.
	Remote string
	// BaseBranch is the local branch pushed up to the server.
	BaseBranch string
	// WorkspaceID, when set, restricts event-driven fetches to git.branch
	// events of that workspace; empty reacts to all of them.
	WorkspaceID string
	// GitPath is the git binary; default "git".
	GitPath string
	// PushInterval is the base-branch push poll interval.
	PushInterval time.Duration
	// CatchupInterval is the periodic full catch-up fetch interval.
	CatchupInterval time.Duration
}

// Daemon is one sync loop instance. Create with New, drive with Run.
type Daemon struct {
	cfg Config

	// fetch and push are the sync actions; function fields so tests can
	// observe decisions without a git binary or an SSH server.
	fetch func(ctx context.Context) error
	push  func(ctx context.Context) error

	lastSeq    uint64 // event-stream resume cursor, touched only in runSession
	lastPushed string // base tip last pushed, touched only in pushBase
}

// New validates cfg, fills defaults, and returns a Daemon.
func New(cfg Config) (*Daemon, error) {
	if cfg.Server == "" {
		return nil, errors.New("syncd: server address required")
	}
	if cfg.RepoPath == "" {
		return nil, errors.New("syncd: repo path required")
	}
	if cfg.User == "" {
		cfg.User = "aether"
	}
	if cfg.Remote == "" {
		cfg.Remote = "aether"
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "main"
	}
	if cfg.GitPath == "" {
		cfg.GitPath = "git"
	}
	if cfg.KnownHostsPath == "" {
		cfg.KnownHostsPath = defaultPath(".ssh", "known_hosts")
	}
	if cfg.PushInterval <= 0 {
		cfg.PushInterval = defaultPushInterval
	}
	if cfg.CatchupInterval <= 0 {
		cfg.CatchupInterval = defaultCatchupInterval
	}
	d := &Daemon{cfg: cfg}
	d.fetch = d.fetchRuns
	d.push = d.pushBase
	return d, nil
}

// Run connects and syncs until ctx is canceled, reconnecting with
// exponential backoff plus jitter on any failure. The backoff resets only
// after a session that was acknowledged and held for stableSession, so a
// long-lived connection dropping reconnects promptly while an ack-then-
// drop server still backs off.
func (d *Daemon) Run(ctx context.Context) error {
	attempt := 0
	for {
		started := time.Now()
		subscribed, err := d.connectOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		attempt = nextAttempt(attempt, subscribed, time.Since(started))
		delay := backoffDelay(attempt, rand.Float64())
		slog.Warn("syncd: disconnected; reconnecting", "error", err, "delay", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// nextAttempt is the consecutive-failure count after a session ends: it
// resets only when the session was acknowledged and lasted at least
// stableSession; anything shorter - including an ack followed by an
// immediate drop - keeps the backoff climbing.
func nextAttempt(attempt int, subscribed bool, lasted time.Duration) int {
	if subscribed && lasted >= stableSession {
		return 0
	}
	return attempt + 1
}

// backoffDelay is the reconnect delay for the given consecutive-failure
// count: exponential from backoffBase, capped at backoffMax, with jitter
// spreading the result over [d/2, d) so daemon herds do not reconnect in
// lockstep. jitter must be in [0, 1).
func backoffDelay(attempt int, jitter float64) time.Duration {
	d := backoffMax
	if attempt < 6 {
		d = backoffBase << uint(attempt)
	}
	if d > backoffMax {
		d = backoffMax
	}
	return d/2 + time.Duration(jitter*float64(d/2))
}

// connectOnce runs one full connect-subscribe-follow cycle; it returns
// whether the subscription was acknowledged.
func (d *Daemon) connectOnce(ctx context.Context) (subscribed bool, err error) {
	client, stream, err := d.connect(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go keepalive(ctx, client)
	return d.runSession(ctx, stream)
}

// sshStream is the aether-events subsystem channel as a byte stream; Close
// tears down the whole connection so a blocked read unblocks.
type sshStream struct {
	io.Reader
	io.Writer
	client *ssh.Client
}

func (s *sshStream) Close() error { return s.client.Close() }

// connect dials the server and opens the aether-events subsystem. Keys
// come from the same resolver as the CLI and GUI, so `aether link --key`
// and automatic discovery behave identically here; the daemon differs
// only in host-key policy, where an unattended process pins nothing and
// needs the host already in known_hosts.
func (d *Daemon) connect(ctx context.Context) (*ssh.Client, io.ReadWriteCloser, error) {
	hostKeys, err := knownhosts.New(d.cfg.KnownHostsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("known_hosts (run aether link once, or point --known-hosts at a file listing the server's host key): %w", err)
	}
	auth := cli.ResolveAuth(cli.Config{Key: d.cfg.KeyPath})
	defer auth.Close()
	conf := &ssh.ClientConfig{
		User:            d.cfg.User,
		Auth:            auth.Methods(),
		HostKeyCallback: hostKeys,
		BannerCallback:  auth.Banner,
		Timeout:         dialTimeout,
	}
	nc, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", d.cfg.Server)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", d.cfg.Server, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, d.cfg.Server, conf)
	if err != nil {
		_ = nc.Close()
		return nil, nil, auth.Explain(fmt.Errorf("ssh handshake with %s: %w", d.cfg.Server, err))
	}
	client := ssh.NewClient(cc, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("open session: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemEvents); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("subsystem %s: %w", protocol.SubsystemEvents, err)
	}
	return client, &sshStream{Reader: stdout, Writer: stdin, client: client}, nil
}

// keepalive probes the connection so a silently dead network surfaces as a
// failed request, which closes the client and unblocks the event read.
func keepalive(ctx context.Context, client *ssh.Client) {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				_ = client.Close()
				return
			}
		}
	}
}

// defaultPath joins elems under the user home; empty when the home is
// unknown, which surfaces as a clear open error at connect time.
func defaultPath(elems ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{home}, elems...)...)
}
