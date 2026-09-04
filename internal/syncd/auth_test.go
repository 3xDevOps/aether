package syncd

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/3xDevOps/Aether/internal/testhome"
)

// The daemon shares the CLI's key discovery: a locked id_ed25519 next to
// a usable id_rsa connects to a keyless server and to one that admits
// only the RSA key, without any --key flag.
func TestConnectDiscoversKeysLikeTheCLI(t *testing.T) {
	home := testhome.Isolate(t)
	testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_ed25519"), testhome.Ed25519Key(t), "secret")
	rsaSigner := testhome.WriteSSHKey(t, filepath.Join(home, ".ssh", "id_rsa"), testhome.RSAKey(t), "")
	want := string(rsaSigner.PublicKey().Marshal())

	for name, cfg := range map[string]*ssh.ServerConfig{
		"keyless server": {NoClientAuth: true},
		"rsa-only server": {PublicKeyCallback: func(_ ssh.ConnMetadata, pub ssh.PublicKey) (*ssh.Permissions, error) {
			if string(pub.Marshal()) == want {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("wrong key")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			addr, known := serveEvents(t, cfg)
			d, err := New(Config{Server: addr, RepoPath: home, KnownHostsPath: known})
			if err != nil {
				t.Fatal(err)
			}
			client, stream, err := d.connect(context.Background())
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			_ = stream.Close()
			_ = client.Close()
		})
	}
}

// An unattended daemon with nothing to offer still explains itself in
// the reconnect log instead of a bare "attempted methods [none]".
func TestConnectWithoutUsableKeyIsActionable(t *testing.T) {
	home := testhome.Isolate(t)
	addr, known := serveEvents(t, &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errors.New("no member")
		},
	})
	d, err := New(Config{Server: addr, RepoPath: home, KnownHostsPath: known})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.connect(context.Background())
	if err == nil {
		t.Fatal("connect succeeded with no usable key")
	}
	for _, want := range []string{"id_ed25519: not found", "id_rsa: not found", "ssh-add", "--key <private-key>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// serveEvents runs a one-connection SSH server that accepts a session
// channel and any subsystem request, which is all connect needs. It
// returns the address and a known_hosts file already trusting the host.
func serveEvents(t *testing.T, cfg *ssh.ServerConfig) (addr, knownHosts string) {
	t.Helper()
	host := testhome.WriteSSHKey(t, filepath.Join(t.TempDir(), "host_key"), testhome.Ed25519Key(t), "")
	cfg.AddHostKey(host)
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
		defer func() { _ = conn.Close() }()
		serverConn, chans, reqs, hsErr := ssh.NewServerConn(conn, cfg)
		if hsErr != nil {
			return
		}
		defer func() { _ = serverConn.Close() }()
		go ssh.DiscardRequests(reqs)
		for ch := range chans {
			channel, chanReqs, acceptErr := ch.Accept()
			if acceptErr != nil {
				continue
			}
			go func() {
				for r := range chanReqs {
					if r.WantReply {
						_ = r.Reply(true, nil)
					}
				}
			}()
			_ = channel
		}
	}()
	addr = listener.Addr().String()
	knownHosts = filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{addr}, host.PublicKey()) + "\n"
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return addr, knownHosts
}
