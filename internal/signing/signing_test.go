package signing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/memberhome"
)

const member domain.MemberID = "member-1"

var identity = domain.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}

// newService keeps its directory short: a unix socket path is capped at
// 107 bytes, and t.TempDir carries the test name.
func newService(t *testing.T) (*Service, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sign")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	homes, err := memberhome.New(filepath.Join(dir, "homes"), filepath.Join(dir, "caches"), nil)
	if err != nil {
		t.Fatalf("memberhome.New: %v", err)
	}
	home, err := homes.Path(member)
	if err != nil {
		t.Fatalf("home path: %v", err)
	}
	s, err := New(filepath.Join(dir, "keys"), filepath.Join(dir, "sock"), homes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, home
}

func requireBinaries(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is not on PATH", name)
		}
	}
}

func dialAgent(t *testing.T, s *Service) agent.ExtendedAgent {
	t.Helper()
	dir, err := s.Serve(member)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, socketName))
	if err != nil {
		t.Fatalf("dial the agent: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agent.NewClient(conn)
}

func TestAgentSignsButRefusesEveryMutation(t *testing.T) {
	s, _ := newService(t)
	key, err := generate(member)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store(member, key); err != nil {
		t.Fatal(err)
	}
	client := dialAgent(t, s)

	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func() error{
		"add":        func() error { return client.Add(agent.AddedKey{PrivateKey: other, Comment: "planted"}) },
		"remove all": func() error { return client.RemoveAll() },
		"lock":       func() error { return client.Lock([]byte("passphrase")) },
		"unlock":     func() error { return client.Unlock([]byte("passphrase")) },
	} {
		if mutate() == nil {
			t.Errorf("%s: the agent accepted it", name)
		}
	}
	keys, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0].Comment != "aether "+string(member) {
		t.Fatalf("keys = %v, want only the member's", keys)
	}
	if client.Remove(keys[0]) == nil {
		t.Error("remove: the agent accepted it")
	}

	data := []byte("commit object")
	sig, err := client.Sign(keys[0], data)
	if err != nil {
		t.Fatalf("Sign after the refusals: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = signer.PublicKey().Verify(data, sig); err != nil {
		t.Fatalf("signature does not verify with the stored key: %v", err)
	}

	// A connection opened before the key was removed signs nothing after,
	// and the agent no longer listens.
	if err = s.Remove(member); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if keys, err = client.List(); err != nil || len(keys) != 0 {
		t.Fatalf("keys after Remove = %v (%v), want none", keys, err)
	}
	if _, err = client.Sign(signer.PublicKey(), data); err == nil {
		t.Fatal("the agent signed with a removed key")
	}
	if _, err = os.Stat(s.socketDir(member)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("agent socket directory after Remove: stat = %v, want none", err)
	}
	if _, err = s.Serve(member); err != nil {
		t.Fatalf("Serve after Remove: %v", err)
	}
}

// A data directory where a run's coordination socket just fits must also
// fit the agent of a member with an older, 26-character ID.
func TestServeFitsWhereCoordinationDoes(t *testing.T) {
	base, err := os.MkdirTemp("", "sign")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	coordSuffix := len("/coord/run-0123456789/" + coordtransport.SocketName)
	data := filepath.Join(base, strings.Repeat("d", maxSocketPath-coordSuffix-len(base)-1))
	homes, err := memberhome.New(filepath.Join(data, "homes"), filepath.Join(data, "caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(data, "signing-keys"), filepath.Join(data, "sign"), homes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err = s.Serve("01m2w6cky0mwwgxmr2sdgev2gs"); err != nil {
		t.Fatalf("Serve for an older member ID: %v", err)
	}
}

// Serve racing a stop never leaves a listener whose socket is gone, which
// later containers would reuse and fail on.
func TestServeAndStopLeaveAListenerOnlyWithItsSocket(t *testing.T) {
	s, _ := newService(t)
	for range 200 {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = s.Serve(member) }()
		go func() { defer wg.Done(); _ = s.StopAgent(member) }()
		wg.Wait()
		s.mu.Lock()
		listening := s.listeners[member] != nil
		s.mu.Unlock()
		_, err := os.Stat(filepath.Join(s.socketDir(member), socketName))
		if listening != (err == nil) {
			t.Fatalf("listening = %v, socket stat = %v; want both or neither", listening, err)
		}
	}
}

func TestArchiveMovesTheKeyAndBack(t *testing.T) {
	s, _ := newService(t)
	dest := filepath.Join(t.TempDir(), "signing-key")
	if moved, err := s.Archive(member, dest); err != nil || moved {
		t.Fatalf("Archive without a key = (%v, %v), want (false, nil)", moved, err)
	}
	key, err := generate(member)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store(member, key); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Serve(member); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	moved, err := s.Archive(member, dest)
	if err != nil || !moved {
		t.Fatalf("Archive = (%v, %v), want (true, nil)", moved, err)
	}
	if _, err = os.Stat(filepath.Join(s.socketDir(member), socketName)); err != nil {
		t.Fatalf("agent socket after Archive: %v, want it kept for a rollback", err)
	}
	held, err := s.Key(member)
	if err != nil || held != nil {
		t.Fatalf("key after Archive = %q (%v), want none", held, err)
	}
	if err = s.Unarchive(member, dest); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if held, err = s.Key(member); err != nil || !bytes.Equal(held, key) {
		t.Fatalf("key after Unarchive = %q (%v), want the archived key", held, err)
	}
}

// What a run does: the home's git config and SSH_AUTH_SOCK are all it has,
// and the commit comes out signed with the key only the server holds.
func TestGitCommitSignsThroughTheAgent(t *testing.T) {
	requireBinaries(t, "git", "ssh-keygen")
	s, home := newService(t)
	// Serving before the key exists is what a member who connects GitHub
	// from an open environment has.
	dir, err := s.Serve(member)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	line, err := s.Ensure(t.Context(), member, identity)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	repo := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1", "SSH_AUTH_SOCK=" + filepath.Join(dir, socketName),
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir, cmd.Env = repo, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "from the run")
	allowed := filepath.Join(t.TempDir(), "allowed_signers")
	if err := os.WriteFile(allowed, []byte(identity.Email+" "+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(git("-c", "gpg.ssh.allowedSignersFile="+allowed, "log", "-1", "--format=%G?")); got != "G" {
		t.Fatalf("signature verdict = %q, want G", got)
	}
}

func TestEnsureKeepsThePrivateKeyOutOfTheHome(t *testing.T) {
	requireBinaries(t, "git")
	s, home := newService(t)
	line, err := s.Ensure(t.Context(), member, identity)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") || !strings.HasSuffix(line, " aether "+string(member)) {
		t.Fatalf("public key line = %q, want an ssh-ed25519 line commented aether %s", line, member)
	}
	info, err := os.Stat(filepath.Join(s.keys, string(member)))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("server-held key = %v (%v), want mode 0600", info, err)
	}
	assertNoPrivateKey(t, home)
	assertGitSigningKey(t, home, line)

	pubPath := filepath.Join(home, ".ssh", "aether_signing.pub")
	if err = os.WriteFile(pubPath, []byte("ssh-ed25519 AAAAsomeoneelse agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := s.Ensure(t.Context(), member, identity)
	if err != nil || again != line {
		t.Fatalf("second Ensure = %q (%v), want the stored key %q", again, err, line)
	}
	if pub, err := os.ReadFile(pubPath); err != nil || strings.TrimSpace(string(pub)) != line {
		t.Errorf("public key file = %q (%v), want it rewritten to %q", pub, err, line)
	}

	if err := s.Remove(member); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if key, err := s.Key(member); err != nil || key != nil {
		t.Fatalf("key after Remove = %q (%v), want none", key, err)
	}
}

func TestMigrateMovesTheHomeKeyOnce(t *testing.T) {
	requireBinaries(t, "git")
	s, home := newService(t)
	legacy, err := generate(member)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, ".ssh", "aether_signing")
	plantKey(t, keyPath, legacy)
	members := []*domain.Member{{ID: member, GitName: identity.Name, GitEmail: identity.Email}}

	if err = s.Migrate(t.Context(), members); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	held, err := s.Key(member)
	if err != nil || !bytes.Equal(held, legacy) {
		t.Fatalf("server-held key = %q (%v), want the key from the home", held, err)
	}
	assertNoPrivateKey(t, home)
	line, err := publicLine(legacy, member)
	if err != nil {
		t.Fatal(err)
	}
	assertGitSigningKey(t, home, line)

	// A key a run plants afterwards is never adopted.
	planted, err := generate(member)
	if err != nil {
		t.Fatal(err)
	}
	plantKey(t, keyPath, planted)
	if err = s.Migrate(t.Context(), members); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if held, err = s.Key(member); err != nil || !bytes.Equal(held, legacy) {
		t.Fatalf("server-held key after a planted one = %q (%v), want the migrated key", held, err)
	}
}

// A .gitconfig the server cannot rewrite keeps the setting earlier releases
// wrote, and commits still sign through the agent: ssh-keygen falls back to
// the .pub beside the moved key.
func TestMigrateKeepsSigningWhenGitConfigCannotBeRewritten(t *testing.T) {
	requireBinaries(t, "git", "ssh-keygen")
	s, home := newService(t)
	legacy, err := generate(member)
	if err != nil {
		t.Fatal(err)
	}
	plantKey(t, filepath.Join(home, ".ssh", "aether_signing"), legacy)
	outside := filepath.Join(t.TempDir(), "gitconfig")
	old := "[user]\n\tname = Ada\n\temail = " + identity.Email + "\n\tsigningkey = ~/.ssh/aether_signing\n[gpg]\n\tformat = ssh\n[commit]\n\tgpgsign = true\n"
	if err = os.WriteFile(outside, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(home, ".gitconfig")); err != nil {
		t.Fatal(err)
	}

	if err = s.Migrate(t.Context(), []*domain.Member{{ID: member}}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	assertNoPrivateKey(t, home)
	if got, rerr := os.ReadFile(outside); rerr != nil || string(got) != old {
		t.Fatalf("symlinked .gitconfig = %q (%v), want it untouched", got, rerr)
	}
	dir, err := s.Serve(member)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	line, err := publicLine(legacy, member)
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1", "SSH_AUTH_SOCK=" + filepath.Join(dir, socketName),
	}
	allowed := filepath.Join(t.TempDir(), "allowed_signers")
	if err = os.WriteFile(allowed, []byte(identity.Email+" "+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"commit", "-q", "--allow-empty", "-m", "after the upgrade"},
		{"-c", "gpg.ssh.allowedSignersFile=" + allowed, "verify-commit", "HEAD"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir, cmd.Env = repo, env
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			t.Fatalf("git %v: %v: %s", args, cerr, out)
		}
	}
}

// A member without a home keeps having none.
func TestMigrateCreatesNoHome(t *testing.T) {
	s, _ := newService(t)
	if err := s.Migrate(t.Context(), []*domain.Member{{ID: "mem-nohome"}}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if has, err := s.homes.HasHome("mem-nohome"); err != nil || has {
		t.Fatalf("HasHome after Migrate = (%v, %v), want (false, nil)", has, err)
	}
}

// A key that cannot be moved is not deleted either: it may be the only
// copy of something.
func TestMigrateLeavesAnUnparsableKey(t *testing.T) {
	s, home := newService(t)
	keyPath := filepath.Join(home, ".ssh", "aether_signing")
	plantKey(t, keyPath, []byte("not a key at all\n"))
	if err := s.Migrate(t.Context(), []*domain.Member{{ID: member}}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if key, err := s.Key(member); err != nil || key != nil {
		t.Fatalf("server-held key = %q (%v), want none", key, err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("unparsable home key stat = %v, want it left in place", err)
	}
}

func plantKey(t *testing.T, path string, key []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoPrivateKey(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(home, ".ssh", "aether_signing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("private key in the home: stat = %v, want none", err)
	}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("PRIVATE KEY")) {
			t.Errorf("%s holds a private key", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertGitSigningKey(t *testing.T, home, line string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "config", "--file", filepath.Join(home, ".gitconfig"), "user.signingkey")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config user.signingkey: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "key::"+line {
		t.Errorf("user.signingkey = %q, want key::%s", got, line)
	}
}
