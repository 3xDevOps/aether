package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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

// Dial connects to cfg.Addr as cfg.User (default aether) with the
// configured key and/or SSH agent, verifying the host against known_hosts
// (TOFU on first contact).
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
	var (
		auth      []ssh.AuthMethod
		closeAuth func()
		err       error
	)
	if requireAuth {
		auth, closeAuth, err = requiredAuthMethods(cfg)
	} else {
		auth, closeAuth = optionalAuthMethods(cfg)
	}
	if err != nil {
		return nil, err
	}
	if closeAuth != nil {
		defer closeAuth()
	}
	known := cfg.knownHostsPath()
	cb, err := hostKeyCallback(known)
	if err != nil {
		return nil, err
	}
	conf := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: cb,
		Timeout:         dialTimeout,
	}
	nc, err := (&net.Dialer{Timeout: dialTimeout}).Dial("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("cli: dial %s: %w", cfg.Addr, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, cfg.Addr, conf)
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("cli: ssh handshake with %s: %w", cfg.Addr, err)
	}
	return &Conn{client: ssh.NewClient(cc, chans, reqs), cfg: cfg}, nil
}

// Close tears down the SSH connection.
func (c *Conn) Close() error { return c.client.Close() }

// SSH is the underlying SSH client (port-forwards, extra sessions).
func (c *Conn) SSH() *ssh.Client { return c.client }

func optionalAuthMethods(cfg Config) ([]ssh.AuthMethod, func()) {
	methods, closeAuth, err := loadAuthMethods(cfg)
	if len(methods) == 0 && err != nil {
		fmt.Fprintf(os.Stderr, "aether: %v\n", err)
	}
	return methods, closeAuth
}

func requiredAuthMethods(cfg Config) ([]ssh.AuthMethod, func(), error) {
	methods, closeAuth, err := loadAuthMethods(cfg)
	if len(methods) > 0 {
		return methods, closeAuth, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, errors.New("cli: no SSH key or agent available")
}

func loadAuthMethods(cfg Config) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	method, closeAuth, authErr := agentAuthMethod()
	if method != nil {
		methods = append(methods, method)
	}
	keyPath := cfg.keyPath()
	if keyPath == "" {
		return methods, closeAuth, authErr
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return methods, closeAuth, authErr
		}
		return methods, closeAuth, fmt.Errorf("cli: read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return methods, closeAuth, fmt.Errorf("cli: parse ssh key %s: %w", keyPath, err)
	}
	methods = append(methods, ssh.PublicKeys(signer))
	return methods, closeAuth, authErr
}

// agentAuthMethod dials the local SSH agent and collects its signers. A nil
// method with a nil error means no agent is configured, which is not a
// failure: key-file auth may still succeed.
func agentAuthMethod() (ssh.AuthMethod, func(), error) {
	conn, err := dialAgent(sshAgentTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("cli: connect ssh agent: %w", err)
	}
	if conn == nil {
		return nil, nil, nil
	}
	// The deadline guards every exchange with an agent that accepts the
	// connection and then never answers.
	if err = conn.SetDeadline(time.Now().Add(sshAgentTimeout)); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("cli: set ssh agent deadline: %w", err)
	}
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("cli: load ssh agent keys: %w", err)
	}
	if len(signers) == 0 {
		_ = conn.Close()
		return nil, nil, errors.New("cli: SSH agent has no signing keys")
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("cli: clear ssh agent deadline: %w", err)
	}
	return ssh.PublicKeys(signers...), func() { _ = conn.Close() }, nil
}

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
