package cli

import (
	"errors"
	"fmt"
	"io"
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
	auth := loadAuth(cfg)
	if auth.close != nil {
		defer auth.close()
	}
	if requireAuth {
		if err := auth.require(); err != nil {
			return nil, err
		}
	}
	known := cfg.knownHostsPath()
	cb, err := hostKeyCallback(known)
	if err != nil {
		return nil, err
	}
	conf := &ssh.ClientConfig{
		User:            user,
		Auth:            auth.methods,
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
		return nil, auth.explain(fmt.Errorf("cli: ssh handshake with %s: %w", cfg.Addr, err))
	}
	// The handshake got through without the unusable methods, so they are
	// a warning here rather than the failure: the next dial may need them.
	auth.warn(os.Stderr)
	return &Conn{client: ssh.NewClient(cc, chans, reqs), cfg: cfg}, nil
}

// Close tears down the SSH connection.
func (c *Conn) Close() error { return c.client.Close() }

// SSH is the underlying SSH client (port-forwards, extra sessions).
func (c *Conn) SSH() *ssh.Client { return c.client }

// sshAuth is the set of SSH authentication methods a dial offers, plus the
// reason any configured method was left out.
type sshAuth struct {
	methods []ssh.AuthMethod
	close   func()
	// problem is why a configured method is unusable - an agent that
	// cannot be reached, a key file that cannot be read or decrypted. It
	// is a warning while another method remains and the whole failure
	// when none does. errors.Join carries both causes at once.
	problem error
}

// loadAuth collects the agent and key-file methods for cfg. Neither source
// is required: an unconfigured agent and an absent key file are not
// problems, an unusable one is.
func loadAuth(cfg Config) sshAuth {
	var a sshAuth
	method, closeAgent, agentErr := agentAuthMethod()
	if method != nil {
		a.methods = append(a.methods, method)
	}
	a.close = closeAgent
	keyMethod, keyErr := keyAuthMethod(cfg.keyPath())
	if keyMethod != nil {
		a.methods = append(a.methods, keyMethod)
	}
	a.problem = errors.Join(agentErr, keyErr)
	return a
}

// require rejects a dial that must authenticate with nothing to offer.
func (a sshAuth) require() error {
	if len(a.methods) > 0 {
		return nil
	}
	if a.problem != nil {
		return a.problem
	}
	return errors.New("cli: no SSH key or agent available")
}

// warn prints each unusable method on its own line.
func (a sshAuth) warn(w io.Writer) {
	for _, cause := range causes(a.problem) {
		_, _ = fmt.Fprintf(w, "aether: %v\n", cause)
	}
}

// explain appends the unusable methods to a handshake failure, so a
// rejection names the key it could not offer instead of leaving the user
// with an empty list of attempted methods.
func (a sshAuth) explain(err error) error {
	for _, cause := range causes(a.problem) {
		err = fmt.Errorf("%w; %v", err, cause)
	}
	return err
}

// causes splits an errors.Join result into its parts so each prints on its
// own line rather than as one embedded newline.
func causes(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}

// keyAuthMethod loads the private key at path. A nil method with a nil
// error means there is no key file there, which is not a failure: agent
// auth may still succeed.
func keyAuthMethod(path string) (ssh.AuthMethod, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cli: read ssh key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		var locked *ssh.PassphraseMissingError
		if errors.As(err, &locked) {
			return nil, fmt.Errorf(
				"cli: %s is passphrase-protected; add it to ssh-agent (ssh-add %s) or pass --key <unencrypted key>: %w",
				path, path, err)
		}
		return nil, fmt.Errorf("cli: parse ssh key %s: %w", path, err)
	}
	return ssh.PublicKeys(signer), nil
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
