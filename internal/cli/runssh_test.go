package cli

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/testhome"
)

const userSSHConfig = "ServerAliveInterval 60\n\nHost bastion\n  HostName bastion.example.com\n  User ops\n\nHost *\n  ProxyJump bastion\n"

func TestInstallSSHConfigIncludesItsOwnFileAndLeavesTheRestAlone(t *testing.T) {
	home := testhome.Isolate(t)
	config := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(userSSHConfig), 0o640); err != nil {
		t.Fatal(err)
	}

	first, err := InstallSSHConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Included || first.Config != config {
		t.Fatalf("first install = %+v, want an Include added to %s", first, config)
	}
	hosts, err := os.ReadFile(first.Hosts)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nHost *.aether\n  HostName %h\n  ProxyCommand ",
		" ssh --stdio %h\n",
		"\n  UserKnownHostsFile \"" + filepath.ToSlash(filepath.Join(home, ".ssh", "aether_known_hosts")) + "\"\n",
		"\n  HostKeyAlias aether\n",
		"\n  StrictHostKeyChecking yes\n",
	} {
		if !strings.Contains(string(hosts), want) {
			t.Errorf("%s lacks %q:\n%s", first.Hosts, want, hosts)
		}
	}
	written, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	include := "Include \"" + filepath.ToSlash(first.Hosts) + "\"\n"
	// The Include precedes the user's own lines, so their Host * cannot
	// set ProxyCommand first, and every one of those lines survives.
	if at := strings.Index(string(written), include); at < 0 || !strings.HasSuffix(string(written), "\n"+userSSHConfig) ||
		at+len(include) > len(written)-len(userSSHConfig) {
		t.Fatalf("config after install:\n%s", written)
	}
	if runtime.GOOS != "windows" {
		info, serr := os.Stat(config)
		if serr != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("config mode = %v %v, want the 0640 it had", info.Mode().Perm(), serr)
		}
	}

	second, err := InstallSSHConfig()
	if err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if second.Included || string(again) != string(written) {
		t.Fatalf("second install = %+v, changed the config:\n%s", second, again)
	}
}

func TestInstallSSHConfigCreatesAMissingConfig(t *testing.T) {
	home := testhome.Isolate(t)
	result, err := InstallSSHConfig()
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Included || !strings.HasSuffix(string(written), "Include \""+filepath.ToSlash(result.Hosts)+"\"\n") {
		t.Fatalf("created config = %+v:\n%s", result, written)
	}
}

func TestInstallSSHConfigWritesThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege Windows test runners lack")
	}
	home := testhome.Isolate(t)
	dotfiles := filepath.Join(home, "dotfiles", "ssh_config")
	if err := os.MkdirAll(filepath.Dir(dotfiles), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dotfiles, []byte(userSSHConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".ssh", "config")
	if err := os.Symlink(dotfiles, config); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSSHConfig(); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(config); err != nil || target != dotfiles {
		t.Fatalf("config link = %q %v, want it still pointing at %s", target, err, dotfiles)
	}
	if written, err := os.ReadFile(dotfiles); err != nil || !strings.Contains(string(written), "Include ") {
		t.Fatalf("linked config = %q %v, want the Include written into it", written, err)
	}
}

// A % in a path is a token to ssh, and no escape for it works in both
// UserKnownHostsFile and Include on every OpenSSH.
func TestInstallSSHConfigRefusesAHomeSSHCannotName(t *testing.T) {
	home := filepath.Join(testhome.Isolate(t), "50% off")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if _, err := InstallSSHConfig(); err == nil || !strings.Contains(err.Error(), "an ssh config cannot name") ||
		!strings.Contains(err.Error(), "50% off") {
		t.Fatalf("install under %s = %v, want the path refused by name", home, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh")); !os.IsNotExist(err) {
		t.Fatalf("a refused install wrote into %s: %v", home, err)
	}
}

func newHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(testhome.Ed25519Key(t))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestTrustRunHostKeyRecordsEachKeyOnce(t *testing.T) {
	home := testhome.Isolate(t)
	one, two := newHostKey(t).PublicKey(), newHostKey(t).PublicKey()
	for _, key := range []ssh.PublicKey{one, one, two} {
		if err := TrustRunHostKey(key); err != nil {
			t.Fatal(err)
		}
	}
	known, err := os.ReadFile(filepath.Join(home, ".ssh", "aether_known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(known) != "aether "+string(ssh.MarshalAuthorizedKey(one))+"aether "+string(ssh.MarshalAuthorizedKey(two)) {
		t.Fatalf("known hosts:\n%s", known)
	}
}

// serveRunSSHAck is a server that admits anyone and answers the run-ssh
// subsystem with a host key line of its choosing.
func serveRunSSHAck(t *testing.T, host ssh.Signer, ackKey string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(host)
	go func() {
		raw, aerr := listener.Accept()
		if aerr != nil {
			return
		}
		conn, chans, reqs, serr := ssh.NewServerConn(raw, config)
		if serr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			ch, requests, cerr := nc.Accept()
			if cerr != nil {
				return
			}
			for req := range requests {
				if req.Type != "subsystem" {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				if _, rerr := protocol.ReadLine(bufio.NewReader(ch)); rerr != nil {
					return
				}
				ack, _ := json.Marshal(protocol.RunSSHResponse{OK: true, HostKey: ackKey})
				_, _ = ch.Write(append(ack, '\n'))
			}
		}
	}()
	return listener.Addr().String()
}

func TestRunSSHAcceptsOnlyTheHostKeyTheLinkVerified(t *testing.T) {
	disableAgent(t)
	host, other := newHostKey(t), newHostKey(t)
	line := func(s ssh.Signer) string { return string(ssh.MarshalAuthorizedKey(s.PublicKey())) }
	for name, tc := range map[string]struct {
		ack  string
		want string
	}{
		"the server's own key": {line(host), ""},
		"another key":          {line(other), "not the key this link verified"},
		"no key":               {"not a key", "unreadable host key"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			conn, err := Dial(Config{
				Addr: serveRunSSHAck(t, host, tc.ack), Key: filepath.Join(dir, "missing-key"), KnownHosts: filepath.Join(dir, "known_hosts"),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			stream, key, err := conn.RunSSH("run-gejd5cbk74")
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("RunSSH = %v, want an error naming %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			if string(key.Marshal()) != string(host.PublicKey().Marshal()) {
				t.Fatalf("RunSSH returned %s, want the server's key", ssh.FingerprintSHA256(key))
			}
		})
	}
}
