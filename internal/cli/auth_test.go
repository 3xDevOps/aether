package cli

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/3xDevOps/Aether/internal/testhome"
)

// The reported bug: a locked default key next to a usable one, no agent,
// and a server that needs no key. The link must succeed and say nothing.
func TestDialKeylessServerIsQuietWithLockedDefaultKey(t *testing.T) {
	home := testhome.Isolate(t)
	disableAgent(t)
	testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_ed25519"), testhome.Ed25519Key(t), "secret")
	testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_rsa"), testhome.RSAKey(t), "")
	addr, done := serveOnce(t, &ssh.ServerConfig{NoClientAuth: true})

	stderr := captureStderr(t)
	conn, err := Dial(Config{Addr: addr, KnownHosts: filepath.Join(home, "known_hosts")})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	// The TOFU line is the only permitted output, and only because the
	// host was never seen before.
	for _, line := range strings.Split(strings.TrimSpace(stderr()), "\n") {
		if line != "" && !strings.Contains(line, "trusting new host") {
			t.Errorf("stderr line %q, want only the trusting-new-host notice", line)
		}
	}
}

// A locked id_ed25519 must not stop discovery from offering the id_rsa
// after it when the server insists on a key.
func TestDiscoveryOffersLaterCandidateAfterLockedKey(t *testing.T) {
	home := testhome.Isolate(t)
	disableAgent(t)
	testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_ed25519"), testhome.Ed25519Key(t), "secret")
	rsaKey := testhome.RSAKey(t)
	testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_rsa"), rsaKey, "")
	addr, done := serveOnce(t, acceptOnly(t, rsaKey))

	conn, err := Dial(Config{Addr: addr, KnownHosts: filepath.Join(home, "known_hosts")})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
}

// --key is a promise: with it set, an agent holding some other key must
// not be used to get in as a different identity.
func TestExplicitKeyIgnoresUnrelatedAgentKeys(t *testing.T) {
	home := testhome.Isolate(t)
	agentKey := testhome.Ed25519Key(t)
	serveAgent(t, agentKey)
	chosen := testhome.Ed25519Key(t)
	keyPath := filepath.Join(home, "work_key")
	testhome.WriteSSHKey(t, keyPath, chosen, "")

	addr, done := serveOnce(t, acceptOnly(t, agentKey))
	_, err := Dial(Config{Addr: addr, Key: keyPath, KnownHosts: filepath.Join(home, "known_hosts")})
	if err == nil {
		t.Fatal("Dial authenticated with an agent key although --key chose another")
	}
	if !strings.Contains(err.Error(), keyPath+": offered") {
		t.Errorf("error = %v, want it to list the chosen key", err)
	}
	<-done

	addr, done = serveOnce(t, acceptOnly(t, chosen))
	conn, err := Dial(Config{Addr: addr, Key: keyPath, KnownHosts: filepath.Join(home, "known_hosts")})
	if err != nil {
		t.Fatalf("Dial with the chosen key: %v", err)
	}
	_ = conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
}

// A passphrase-protected --key still works when ssh-add has loaded that
// same key: the agent copy is matched by public key.
func TestExplicitLockedKeyIsUnlockedByMatchingAgentKey(t *testing.T) {
	home := testhome.Isolate(t)
	chosen := testhome.Ed25519Key(t)
	serveAgent(t, testhome.Ed25519Key(t), chosen)
	keyPath := filepath.Join(home, "locked_key")
	testhome.WriteSSHKey(t, keyPath, chosen, "secret")
	addr, done := serveOnce(t, acceptOnly(t, chosen))

	conn, err := Dial(Config{Addr: addr, Key: keyPath, KnownHosts: filepath.Join(home, "known_hosts")})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
}

// When the server refuses every method, the error has to tell a new user
// what was looked at, what the server said, and both ways out.
func TestAuthFailureExplainsWhatWasTried(t *testing.T) {
	home := testhome.Isolate(t)
	disableAgent(t)
	testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_ed25519"), testhome.Ed25519Key(t), "secret")
	testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_rsa"), testhome.RSAKey(t), "")
	addr, done := serveOnce(t, &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, &ssh.BannerError{Err: errors.New("unknown key"), Message: "no Aether member for this key\n"}
		},
	})

	_, err := Dial(Config{Addr: addr, KnownHosts: filepath.Join(home, "known_hosts")})
	<-done
	if err == nil {
		t.Fatal("Dial succeeded against a server that refuses every key")
	}
	for _, want := range []string{
		"ssh-agent: not running",
		"id_ed25519: passphrase protected; unlock it with: ssh-add",
		"id_ecdsa: not found",
		"id_rsa: offered",
		"server said: no Aether member for this key",
		"--key <private-key>",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// A refused --key names the way back: only that file was tried, and the
// advice includes --key auto as well as ssh-add and another --key.
func TestExplicitKeyFailureOffersAuto(t *testing.T) {
	home := testhome.Isolate(t)
	disableAgent(t)
	keyPath := filepath.Join(home, "work_key")
	testhome.WriteSSHKey(t, keyPath, testhome.Ed25519Key(t), "")
	testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_ed25519"), testhome.Ed25519Key(t), "")
	addr, done := serveOnce(t, &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errors.New("unknown key")
		},
	})

	_, err := Dial(Config{Addr: addr, Key: keyPath, KnownHosts: filepath.Join(home, "known_hosts")})
	<-done
	if err == nil {
		t.Fatal("Dial succeeded against a server that refuses the chosen key")
	}
	for _, want := range []string{keyPath + ": offered", "--key auto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "id_ed25519") {
		t.Errorf("explicit key error mentions a default key file:\n%v", err)
	}
}

// Invites must authenticate, so with nothing to offer the failure is
// reported before any connection is made and still explains itself.
func TestDialInviteWithoutAnyKeyExplains(t *testing.T) {
	home := testhome.Isolate(t)
	disableAgent(t)
	_, err := DialInvite(Config{Addr: "127.0.0.1:1", KnownHosts: filepath.Join(home, "known_hosts")}, "code", "")
	if err == nil {
		t.Fatal("DialInvite with no key succeeded")
	}
	for _, want := range []string{"no usable SSH key", "ssh-agent: not running", "id_ed25519: not found", "ssh-add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestCheckKey(t *testing.T) {
	dir := t.TempDir()
	key := testhome.Ed25519Key(t)
	private := filepath.Join(dir, "id_ed25519")
	testhome.WriteSSHKey(t, private, key, "")
	locked := filepath.Join(dir, "locked")
	testhome.WriteSSHKey(t, locked, key, "secret")
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	public := private + ".pub"
	if err := os.WriteFile(public, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CheckKey(private); err != nil {
		t.Errorf("CheckKey(private) = %v", err)
	}
	if err := CheckKey(locked); err != nil {
		t.Errorf("CheckKey(locked) = %v, want nil: the agent may unlock it", err)
	}
	if err := CheckKey(public); err == nil || !strings.Contains(err.Error(), "is a public key") {
		t.Errorf("CheckKey(public) = %v, want public-key rejection", err)
	}
	if err := CheckKey(filepath.Join(dir, "nope")); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("CheckKey(missing) = %v, want not found", err)
	}
	if err := CheckKey(garbage); err == nil || !strings.Contains(err.Error(), "parse ssh key") {
		t.Errorf("CheckKey(garbage) = %v, want parse error", err)
	}
}

// serveAgent stands in for ssh-agent with an in-memory keyring holding
// keys, reachable through the package's dialAgent seam.
func serveAgent(t *testing.T, keys ...any) {
	t.Helper()
	keyring := agent.NewKeyring()
	for _, k := range keys {
		if err := keyring.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	setDialAgent(t, func(time.Duration) (net.Conn, error) {
		client, server := net.Pipe()
		go func() { _ = agent.ServeAgent(keyring, server) }()
		return client, nil
	})
}

// acceptOnly is a server config that admits exactly one client key.
func acceptOnly(t *testing.T, key any) *ssh.ServerConfig {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	want := string(signer.PublicKey().Marshal())
	return &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, pub ssh.PublicKey) (*ssh.Permissions, error) {
			if string(pub.Marshal()) == want {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("wrong key")
		},
	}
}

// serveOnce runs a single-handshake SSH server on a loopback port and
// reports the handshake outcome on done.
func serveOnce(t *testing.T, cfg *ssh.ServerConfig) (string, <-chan error) {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(testhome.Ed25519Key(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()
		serverConn, _, _, hsErr := ssh.NewServerConn(conn, cfg)
		if hsErr == nil {
			_ = serverConn.Close()
		}
		done <- hsErr
	}()
	return listener.Addr().String(), done
}
