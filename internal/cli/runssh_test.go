package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

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

func TestTrustRunHostKeyRecordsEachKeyOnce(t *testing.T) {
	home := testhome.Isolate(t)
	keyLine := func() string {
		signer, err := ssh.NewSignerFromKey(testhome.Ed25519Key(t))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	}
	one, two := keyLine(), keyLine()
	for _, key := range []string{one, one, two} {
		if err := TrustRunHostKey(key); err != nil {
			t.Fatal(err)
		}
	}
	known, err := os.ReadFile(filepath.Join(home, ".ssh", "aether_known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(known) != "aether "+one+"\naether "+two+"\n" {
		t.Fatalf("known hosts:\n%s", known)
	}
	if err = TrustRunHostKey("not a key"); err == nil {
		t.Fatal("an unreadable host key was recorded")
	}
}
