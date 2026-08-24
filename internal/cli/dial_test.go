package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
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
	type dialResult struct {
		conn *Conn
		err  error
	}
	result := make(chan dialResult, 1)
	go func() {
		conn, err := Dial(Config{
			Addr:       listener.Addr().String(),
			Key:        filepath.Join(dir, "missing-key"),
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
	knownHosts := filepath.Join(dir, "known_hosts")
	stderr := captureStderr(t)
	conn, err := Dial(Config{
		Addr:       listener.Addr().String(),
		Key:        filepath.Join(dir, "missing-key"),
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

func TestRequiredAuthMethodsWithoutAgentOrKey(t *testing.T) {
	setDialAgent(t, func(time.Duration) (net.Conn, error) { return nil, nil })
	cfg := Config{Key: filepath.Join(t.TempDir(), "missing-key")}

	if _, _, err := requiredAuthMethods(cfg); err == nil ||
		!strings.Contains(err.Error(), "no SSH key or agent available") {
		t.Fatalf("requiredAuthMethods error = %v, want no SSH key or agent available", err)
	}

	stderr := captureStderr(t)
	methods, closeAuth := optionalAuthMethods(cfg)
	if closeAuth != nil {
		closeAuth()
	}
	if len(methods) != 0 {
		t.Errorf("optionalAuthMethods returned %d methods, want 0", len(methods))
	}
	if got := stderr(); got != "" {
		t.Errorf("stderr = %q, want nothing when no agent is configured", got)
	}
}

// A broken agent must not fail silently: optionalAuthMethods swallows the
// error so the only trace the user gets is the diagnostic on stderr.
func TestBrokenAgentIsReported(t *testing.T) {
	dir := t.TempDir()
	notASocket := filepath.Join(dir, "agent.sock")
	if err := os.WriteFile(notASocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", notASocket)
	cfg := Config{Key: filepath.Join(dir, "missing-key")}

	_, _, err := requiredAuthMethods(cfg)
	if err == nil || !strings.Contains(err.Error(), "connect ssh agent") {
		t.Fatalf("requiredAuthMethods error = %v, want connect ssh agent", err)
	}

	stderr := captureStderr(t)
	methods, closeAuth := optionalAuthMethods(cfg)
	if closeAuth != nil {
		closeAuth()
	}
	if len(methods) != 0 {
		t.Errorf("optionalAuthMethods returned %d methods, want 0", len(methods))
	}
	if got := stderr(); !strings.Contains(got, "connect ssh agent") {
		t.Errorf("stderr = %q, want it to mention connect ssh agent", got)
	}
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
