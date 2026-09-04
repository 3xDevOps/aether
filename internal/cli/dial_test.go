package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestDialAllowsKeylessNoneAuthenticationWithInvalidKey(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		serverConfig := &ssh.ServerConfig{NoClientAuth: true}
		serverConfig.AddHostKey(signer)
		serverConn, _, _, handshakeErr := ssh.NewServerConn(conn, serverConfig)
		if handshakeErr == nil {
			_ = serverConn.Close()
		}
		serverDone <- handshakeErr
	}()

	disableAgent(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "invalid-key")
	if writeErr := os.WriteFile(keyPath, []byte("not an SSH private key"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	conn, err := Dial(Config{
		Addr:       listener.Addr().String(),
		Key:        keyPath,
		KnownHosts: filepath.Join(dir, "known_hosts"),
	})
	if err != nil {
		t.Fatalf("keyless Dial: %v", err)
	}
	_ = conn.Close()
	if err := <-serverDone; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
}

func TestDialContinuesWithoutUnresponsiveAgent(t *testing.T) {
	agentSocket, stopAgent := startStallingAgent(t)
	t.Setenv("SSH_AUTH_SOCK", agentSocket)

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()
		serverConfig := &ssh.ServerConfig{NoClientAuth: true}
		serverConfig.AddHostKey(signer)
		serverConn, _, _, handshakeErr := ssh.NewServerConn(conn, serverConfig)
		if handshakeErr == nil {
			_ = serverConn.Close()
		}
		serverDone <- handshakeErr
	}()

	dir := t.TempDir()
	noKeyFile(t)
	type dialResult struct {
		conn *Conn
		err  error
	}
	result := make(chan dialResult, 1)
	go func() {
		conn, err := Dial(Config{
			Addr:       listener.Addr().String(),
			KnownHosts: filepath.Join(dir, "known_hosts"),
		})
		result <- dialResult{conn: conn, err: err}
	}()

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var dialed dialResult
	select {
	case dialed = <-result:
	case <-timer.C:
		stopAgent()
		dialed = <-result
		if dialed.conn != nil {
			_ = dialed.conn.Close()
		}
		t.Fatal("Dial blocked on an unresponsive SSH agent")
	}
	stopAgent()
	if dialed.err != nil {
		t.Fatalf("keyless Dial with unresponsive agent: %v", dialed.err)
	}
	_ = dialed.conn.Close()
	if err := <-serverDone; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
}

func TestDialInviteRejectsInvalidKeyBeforeDialing(t *testing.T) {
	disableAgent(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "invalid-key")
	if writeErr := os.WriteFile(keyPath, []byte("not an SSH private key"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, err = DialInvite(Config{
		Addr:       listener.Addr().String(),
		Key:        keyPath,
		KnownHosts: filepath.Join(dir, "known_hosts"),
	}, "invite-code", "Ada")
	if err == nil || !strings.Contains(err.Error(), "parse ssh key") {
		t.Fatalf("DialInvite error = %v, want invalid-key error", err)
	}

	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	accepted, acceptErr := listener.Accept()
	if acceptErr == nil {
		_ = accepted.Close()
		t.Fatal("DialInvite dialed the server before rejecting the invalid key")
	}
	if netErr, ok := acceptErr.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("accept after DialInvite = %v, want timeout", acceptErr)
	}
}

// First contact is trust-on-first-use, so the key it silently pins is the
// one an attacker in the path would supply: the user must at least be told
// which fingerprint was trusted.
func TestDialAnnouncesNewlyPinnedHostKey(t *testing.T) {
	disableAgent(t)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		serverConfig := &ssh.ServerConfig{NoClientAuth: true}
		serverConfig.AddHostKey(signer)
		if serverConn, _, _, hsErr := ssh.NewServerConn(conn, serverConfig); hsErr == nil {
			_ = serverConn.Close()
		}
	}()

	dir := t.TempDir()
	noKeyFile(t)
	knownHosts := filepath.Join(dir, "known_hosts")
	stderr := captureStderr(t)
	conn, err := Dial(Config{
		Addr:       listener.Addr().String(),
		KnownHosts: knownHosts,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = conn.Close()

	got := stderr()
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	for _, want := range []string{listener.Addr().String(), fingerprint, knownHosts} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr = %q, want it to mention %q", got, want)
		}
	}
}

func TestRequireWithoutAgentOrKey(t *testing.T) {
	setDialAgent(t, func(time.Duration) (net.Conn, error) { return nil, nil })
	noKeyFile(t)
	auth := loadAuth(Config{})
	if auth.close != nil {
		auth.close()
	}

	if err := auth.require(); err == nil ||
		!strings.Contains(err.Error(), "no SSH key or agent available") {
		t.Fatalf("require error = %v, want no SSH key or agent available", err)
	}
	if len(auth.methods) != 0 {
		t.Errorf("loadAuth returned %d methods, want 0", len(auth.methods))
	}
	// A missing key file and an unconfigured agent are both ordinary, so
	// neither may be reported as a problem.
	if auth.problem != nil {
		t.Errorf("problem = %v, want none when no agent is configured", auth.problem)
	}
}

// A broken agent must not fail silently: with no method left, the dial that
// follows has to name it.
func TestBrokenAgentIsReported(t *testing.T) {
	dir := t.TempDir()
	notASocket := filepath.Join(dir, "agent.sock")
	if err := os.WriteFile(notASocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", notASocket)
	noKeyFile(t)
	auth := loadAuth(Config{})
	if auth.close != nil {
		auth.close()
	}

	if len(auth.methods) != 0 {
		t.Errorf("loadAuth returned %d methods, want 0", len(auth.methods))
	}
	err := auth.require()
	if err == nil || !strings.Contains(err.Error(), "connect ssh agent") {
		t.Fatalf("require error = %v, want connect ssh agent", err)
	}
	if got := auth.explain(errors.New("handshake failed")).Error(); !strings.Contains(got, "connect ssh agent") {
		t.Errorf("explain = %q, want it to mention connect ssh agent", got)
	}
}

// The common failure: a passphrase-protected ~/.ssh/id_ed25519 with no
// agent to unlock it. The dial error must name the key and the remedy, not
// just the empty list of methods the server rejected.
func TestDialNamesPassphraseProtectedKey(t *testing.T) {
	disableAgent(t)
	dir := t.TempDir()
	keyPath := writeEncryptedKey(t, dir)
	addr := startRejectingServer(t)

	_, err := Dial(Config{
		Addr:       addr,
		Key:        keyPath,
		KnownHosts: filepath.Join(dir, "known_hosts"),
	})
	if err == nil {
		t.Fatal("Dial succeeded, want an authentication failure")
	}
	for _, want := range []string{
		keyPath + " is passphrase-protected",
		"ssh-add " + keyPath,
		"pass --key <unencrypted key>",
		"ssh: this private key is passphrase protected",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Dial error = %q, want it to mention %q", err, want)
		}
	}
}

// The --key path is what `aether link --key` saves, so a usable key there
// must be offered to the server.
func TestDialOffersConfiguredKey(t *testing.T) {
	disableAgent(t)
	dir := t.TempDir()
	keyPath, signer := writeKey(t, dir)
	addr, _, offered := startAcceptingServer(t)

	conn, err := Dial(Config{
		Addr:       addr,
		Key:        keyPath,
		KnownHosts: filepath.Join(dir, "known_hosts"),
	})
	if err != nil {
		t.Fatalf("Dial with --key: %v", err)
	}
	_ = conn.Close()
	select {
	case got := <-offered:
		if want := ssh.FingerprintSHA256(signer.PublicKey()); got != want {
			t.Errorf("server saw key %s, want %s", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never saw the configured key")
	}
}

// A key path the user chose that is not there must fail on the path, not
// fall back to whatever the agent happens to hold: the wrong path would
// otherwise sit in the config unnoticed. The agent here has keys, so the
// dial would have gone through without the check.
func TestDialRejectsMissingChosenKey(t *testing.T) {
	dir := t.TempDir()
	keyPath, signer := writeKey(t, dir)
	setDialAgent(t, keyringAgent(t, signer))
	missing := filepath.Join(dir, "not-here")
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	addr, _, _ := startAcceptingServer(t)

	_, err := Dial(Config{
		Addr:       addr,
		Key:        missing,
		KnownHosts: filepath.Join(dir, "known_hosts"),
	})
	if err == nil {
		t.Fatal("Dial succeeded, want the missing key to fail the dial")
	}
	if got := err.Error(); !strings.Contains(got, "ssh key "+missing) {
		t.Errorf("Dial error = %q, want it to name %s", got, missing)
	}
}

// An agent that is running but holds no keys is the normal desktop state.
// Every command redials, so a dial that authenticates with the key file
// must say nothing at all.
func TestDialWithEmptyAgentIsQuiet(t *testing.T) {
	setDialAgent(t, keyringAgent(t))
	dir := t.TempDir()
	keyPath, _ := writeKey(t, dir)
	addr, hostKey, _ := startAcceptingServer(t)
	// Trust the host up front so nothing but a diagnostic could reach
	// stderr.
	knownHosts := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{addr}, hostKey) + "\n"
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t)
	conn, err := Dial(Config{Addr: addr, Key: keyPath, KnownHosts: knownHosts})
	if err != nil {
		stderr()
		t.Fatalf("Dial with an empty agent: %v", err)
	}
	_ = conn.Close()
	if got := stderr(); got != "" {
		t.Errorf("stderr = %q, want nothing on a successful dial", got)
	}
}

// writeEncryptedKey writes a passphrase-protected ed25519 key into dir and
// returns its path.
func writeEncryptedKey(t *testing.T, dir string) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(privateKey, "", []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// noKeyFile points the default key path at an empty scratch home: it is
// how a test says "this machine has no key file" without reading the
// developer's own ~/.ssh.
func noKeyFile(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// writeKey writes an unencrypted ed25519 key into dir and returns its
// path with the matching signer.
func writeKey(t *testing.T, dir string) (string, ssh.Signer) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "chosen-key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, signer
}

// keyringAgent stands in for a real SSH agent holding exactly the given
// signers - none of them for the empty agent every desktop runs.
func keyringAgent(t *testing.T, signers ...ssh.Signer) func(time.Duration) (net.Conn, error) {
	t.Helper()
	return func(time.Duration) (net.Conn, error) {
		client, server := net.Pipe()
		keyring := agent.NewKeyring()
		for _, signer := range signers {
			if err := keyring.Add(agent.AddedKey{PrivateKey: signer}); err != nil {
				return nil, err
			}
		}
		go func() { _ = agent.ServeAgent(keyring, server) }()
		return client, nil
	}
}

// startAcceptingServer serves one SSH handshake that accepts any key and
// reports the fingerprint it was offered.
func startAcceptingServer(t *testing.T) (string, ssh.PublicKey, <-chan string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	offered := make(chan string, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		serverConfig := &ssh.ServerConfig{
			PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				select {
				case offered <- ssh.FingerprintSHA256(key):
				default:
				}
				return &ssh.Permissions{}, nil
			},
		}
		serverConfig.AddHostKey(hostKey)
		if serverConn, _, _, hsErr := ssh.NewServerConn(conn, serverConfig); hsErr == nil {
			_ = serverConn.Close()
		}
	}()
	return listener.Addr().String(), hostKey.PublicKey(), offered
}

// startRejectingServer serves one SSH handshake that refuses every key,
// which is what a client with nothing to offer runs into.
func startRejectingServer(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		serverConfig := &ssh.ServerConfig{
			PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
				return nil, errors.New("no Aether member for this key")
			},
		}
		serverConfig.AddHostKey(signer)
		if serverConn, _, _, hsErr := ssh.NewServerConn(conn, serverConfig); hsErr == nil {
			_ = serverConn.Close()
		}
	}()
	return listener.Addr().String()
}

// setDialAgent installs a stub agent transport for the duration of the test
// so the outcome does not depend on whether the host runs an SSH agent.
func setDialAgent(t *testing.T, fn func(time.Duration) (net.Conn, error)) {
	t.Helper()
	saved := dialAgent
	dialAgent = fn
	t.Cleanup(func() { dialAgent = saved })
}

// disableAgent pins "no agent configured" for the duration of the test.
// Clearing SSH_AUTH_SOCK alone is not enough: on Windows the agent
// transport falls back to the OpenSSH named pipe, so a runner with
// ssh-agent running would inject signers these tests do not expect.
func disableAgent(t *testing.T) {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", "")
	setDialAgent(t, func(time.Duration) (net.Conn, error) { return nil, nil })
}

// captureStderr redirects os.Stderr until the returned function reads back
// everything written to it.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	return func() string {
		os.Stderr = saved
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
}

func startStallingAgent(t *testing.T) (string, func()) {
	t.Helper()

	// Not t.TempDir(): its directory name embeds the test name, and the
	// resulting socket path can exceed the 108-byte sun_path limit that
	// Windows AF_UNIX shares with POSIX.
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		<-release
	}()

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(release)
			_ = listener.Close()
			<-done
		})
	}
	t.Cleanup(stop)
	return socket, stop
}
