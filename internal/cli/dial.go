package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

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
}

// Dial connects to cfg.Addr as cfg.User (default aether) with the key
// ResolveAuth selects, verifying the host against known_hosts (TOFU on
// first contact). A server that needs no key connects even when no key
// was found; one that does gets an error saying what was tried.
func Dial(cfg Config) (*Conn, error) {
	return dial(cfg, cfg.user(), false)
}

// DialInvite connects using SSH user invite:<code> (optionally with a
// display name) so an unknown key can join via a one-time invite.
func DialInvite(cfg Config, code, display string) (*Conn, error) {
	user := "invite:" + code
	if display != "" {
		user += ":" + display
	}
	return dial(cfg, user, true)
}

func dial(cfg Config, user string, requireAuth bool) (*Conn, error) {
	if cfg.Addr == "" {
		return nil, errors.New("cli: server address required")
	}
	auth := ResolveAuth(cfg)
	defer auth.Close()
	if requireAuth && !auth.Offered() {
		return nil, auth.Missing()
	}
	cb, err := hostKeyCallback(cfg.knownHostsPath())
	if err != nil {
		return nil, err
	}
	conf := &ssh.ClientConfig{
		User:            user,
		Auth:            auth.Methods(),
		HostKeyCallback: cb,
		BannerCallback:  auth.Banner,
		Timeout:         dialTimeout,
	}
	nc, err := (&net.Dialer{Timeout: dialTimeout}).Dial("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("cli: dial %s: %w", cfg.Addr, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, cfg.Addr, conf)
	if err != nil {
		_ = nc.Close()
		return nil, auth.Explain(fmt.Errorf("cli: ssh handshake with %s: %w", cfg.Addr, err))
	}
	return &Conn{client: ssh.NewClient(cc, chans, reqs), cfg: cfg}, nil
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
