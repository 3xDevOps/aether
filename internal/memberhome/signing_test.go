package memberhome

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func newSigningManager(t *testing.T) (*Manager, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "homes")
	manager, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	home, err := manager.Path("member-1")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	return manager, home
}

func TestEnsureSigningKeyGeneratesOnceAndIsStable(t *testing.T) {
	manager, home := newSigningManager(t)

	has, err := manager.HasSigningKey("member-1")
	if err != nil || has {
		t.Fatalf("HasSigningKey before generation = (%v, %v), want (false, nil)", has, err)
	}
	key, err := manager.SigningKey("member-1")
	if err != nil || key != nil {
		t.Fatalf("SigningKey before generation = (%q, %v), want (nil, nil)", key, err)
	}

	pub, err := manager.EnsureSigningKey("member-1")
	if err != nil {
		t.Fatalf("EnsureSigningKey: %v", err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, " aether member-1") {
		t.Fatalf("public key line = %q, want an ssh-ed25519 line commented aether member-1", pub)
	}

	for path, want := range map[string]os.FileMode{
		filepath.Join(home, ".ssh"):                       0o700,
		filepath.Join(home, ".ssh", "aether_signing"):     0o600,
		filepath.Join(home, ".ssh", "aether_signing.pub"): 0o644,
	} {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), want)
		}
	}
	pubFile, err := os.ReadFile(filepath.Join(home, ".ssh", "aether_signing.pub"))
	if err != nil {
		t.Fatalf("read public key file: %v", err)
	}
	if strings.TrimSpace(string(pubFile)) != pub {
		t.Errorf("public key file = %q, want %q", pubFile, pub)
	}

	private, err := manager.SigningKey("member-1")
	if err != nil {
		t.Fatalf("SigningKey: %v", err)
	}
	if !strings.HasPrefix(string(private), "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Fatalf("private key = %q, want an OpenSSH PEM block", private)
	}
	if has, herr := manager.HasSigningKey("member-1"); err != nil || !has {
		t.Fatalf("HasSigningKey after generation = (%v, %v), want (true, nil)", has, herr)
	}

	again, err := manager.EnsureSigningKey("member-1")
	if err != nil {
		t.Fatalf("second EnsureSigningKey: %v", err)
	}
	if again != pub {
		t.Errorf("second EnsureSigningKey = %q, want the first key %q", again, pub)
	}
	second, err := manager.SigningKey("member-1")
	if err != nil || string(second) != string(private) {
		t.Errorf("private key changed on the second call")
	}
}

// A container can write anything into its own home, so a symlink where the
// key belongs must stop the server rather than lead it somewhere else.
// The link points inside the home, where os.Root would follow it happily:
// only the regular-file checks can refuse this one.
func TestSigningKeyRefusesASymlink(t *testing.T) {
	manager, home := newSigningManager(t)
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".ssh", "planted")
	if err := os.WriteFile(target, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("planted", filepath.Join(home, ".ssh", "aether_signing")); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.EnsureSigningKey("member-1"); err == nil {
		t.Fatal("EnsureSigningKey accepted a symlinked key path")
	}
	if _, err := manager.SigningKey("member-1"); err == nil {
		t.Fatal("SigningKey accepted a symlinked key path")
	}
	if has, err := manager.HasSigningKey("member-1"); err != nil || has {
		t.Fatalf("HasSigningKey on a symlink = (%v, %v), want (false, nil)", has, err)
	}
}

func TestConfigureGitWritesSigningSettingsAndKeepsOtherSections(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	manager, home := newSigningManager(t)
	config := filepath.Join(home, ".gitconfig")
	// What gh auth setup-git leaves behind; it must survive.
	const helper = "[credential \"https://github.com\"]\n\thelper = !gh auth git-credential\n"
	if err := os.WriteFile(config, []byte(helper), 0o644); err != nil {
		t.Fatal(err)
	}

	identity := domain.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}
	if err := manager.ConfigureGit(t.Context(), "member-1", identity); err != nil {
		t.Fatalf("ConfigureGit: %v", err)
	}

	contents, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("read .gitconfig: %v", err)
	}
	if !strings.Contains(string(contents), "helper = !gh auth git-credential") {
		t.Errorf(".gitconfig lost gh's credential helper:\n%s", contents)
	}
	got := gitConfigList(t, config)
	want := []string{
		"credential.https://github.com.helper=!gh auth git-credential",
		"user.name=Ada Lovelace",
		"user.email=ada@example.com",
		"gpg.format=ssh",
		"user.signingkey=~/.ssh/aether_signing",
		"commit.gpgsign=true",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf(".gitconfig entries = %q, want %q", got, want)
	}

	// A second call rewrites the same five keys rather than appending.
	if err := manager.ConfigureGit(t.Context(), "member-1", identity); err != nil {
		t.Fatalf("second ConfigureGit: %v", err)
	}
	if again := gitConfigList(t, config); len(again) != len(want) {
		t.Errorf("entries after a second ConfigureGit = %q, want %d", again, len(want))
	}
}

func TestConfigureGitRefusesASymlinkedConfig(t *testing.T) {
	manager, home := newSigningManager(t)
	target := filepath.Join(t.TempDir(), "server.gitconfig")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".gitconfig")); err != nil {
		t.Fatal(err)
	}
	err := manager.ConfigureGit(t.Context(), "member-1", domain.GitIdentity{Name: "Ada", Email: "ada@example.com"})
	if err == nil {
		t.Fatal("ConfigureGit accepted a symlinked .gitconfig")
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil || len(contents) != 0 {
		t.Fatalf("symlink target = %q (%v), want it untouched", contents, readErr)
	}
}

// The generated key has to be one the system ssh-keygen can sign with;
// that, not its wire shape, is what git needs at commit time.
func TestGeneratedSigningKeySignsWithSSHKeygen(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
	manager, home := newSigningManager(t)
	if _, err := manager.EnsureSigningKey("member-1"); err != nil {
		t.Fatalf("EnsureSigningKey: %v", err)
	}
	payload := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(payload, []byte("commit object\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "ssh-keygen", "-Y", "sign",
		"-f", filepath.Join(home, ".ssh", "aether_signing"), "-n", "git", payload)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v: %s", err, out)
	}
	if _, err := os.Stat(payload + ".sig"); err != nil {
		t.Fatalf("stat signature: %v", err)
	}
}

func gitConfigList(t *testing.T, path string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "config", "--file", path, "--list")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --list: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// A container can overwrite its own member's key. Signing a commit with
// the result would fail the commit, so an unparsable key reads as no key.
func TestSigningKeyIgnoresAnUnparsableKey(t *testing.T) {
	manager, home := newSigningManager(t)
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "aether_signing"), []byte("not a key at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := manager.SigningKey("member-1")
	if err != nil || key != nil {
		t.Fatalf("SigningKey with garbage bytes = (%q, %v), want (nil, nil)", key, err)
	}
}

// The home is agent-writable, so both reads are capped: a huge file
// planted where the key or the config belongs is refused by its size, not
// buffered and not staged.
func TestReadsRefuseAnOversizeFile(t *testing.T) {
	const oversize = 2 << 30
	manager, home := newSigningManager(t)
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	plant := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, oversize); err != nil {
			t.Fatal(err)
		}
	}
	plant(t, filepath.Join(home, ".ssh", "aether_signing"))
	plant(t, filepath.Join(home, ".gitconfig"))

	start := time.Now()
	if _, err := manager.SigningKey("member-1"); err == nil || !strings.Contains(err.Error(), "16384 byte limit") {
		t.Errorf("SigningKey on a 2 GiB key = %v, want the 16384 byte cap", err)
	}
	if _, err := manager.EnsureSigningKey("member-1"); err == nil || !strings.Contains(err.Error(), "16384 byte limit") {
		t.Errorf("EnsureSigningKey on a 2 GiB key = %v, want the 16384 byte cap", err)
	}
	err := manager.ConfigureGit(t.Context(), "member-1", domain.GitIdentity{Name: "Ada", Email: "ada@example.com"})
	if err == nil || !strings.Contains(err.Error(), "262144 byte limit") {
		t.Errorf("ConfigureGit on a 2 GiB .gitconfig = %v, want the 262144 byte cap", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the three capped reads took %v, want a refusal without reading 2 GiB", elapsed)
	}
	staged, err := filepath.Glob(filepath.Join(home, ".gitconfig.aether-tmp*"))
	if err != nil || len(staged) != 0 {
		t.Errorf("staged files = %v (%v), want none", staged, err)
	}
}

// An agent that replaces its own .pub gets it rewritten from the private
// key on the next call, and a key it left world-readable tightened.
func TestEnsureSigningKeyRewritesThePublicKeyAndMode(t *testing.T) {
	manager, home := newSigningManager(t)
	pub, err := manager.EnsureSigningKey("member-1")
	if err != nil {
		t.Fatalf("EnsureSigningKey: %v", err)
	}
	pubPath := filepath.Join(home, ".ssh", "aether_signing.pub")
	keyPath := filepath.Join(home, ".ssh", "aether_signing")
	if werr := os.WriteFile(pubPath, []byte("ssh-ed25519 AAAAsomeoneelse agent\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	if cerr := os.Chmod(keyPath, 0o644); cerr != nil {
		t.Fatal(cerr)
	}

	again, err := manager.EnsureSigningKey("member-1")
	if err != nil {
		t.Fatalf("second EnsureSigningKey: %v", err)
	}
	if again != pub {
		t.Errorf("second EnsureSigningKey = %q, want the stored key %q", again, pub)
	}
	written, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(written)) != pub {
		t.Errorf("public key file = %q, want it rewritten to %q", written, pub)
	}
	info, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("private key mode = %v, want 0600", info.Mode().Perm())
	}
}

// A directory planted where ConfigureGit stages the edited config used to
// block every later call; the staged path is cleared and the name is
// fresh each time.
func TestConfigureGitClearsAStaleStagedConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	manager, home := newSigningManager(t)
	stale := filepath.Join(home, ".gitconfig.aether-tmp")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "planted"), []byte("in the way\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := manager.ConfigureGit(t.Context(), "member-1", domain.GitIdentity{Name: "Ada", Email: "ada@example.com"}); err != nil {
		t.Fatalf("ConfigureGit: %v", err)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("stale staged path stat = %v, want it gone", err)
	}
	left, err := filepath.Glob(filepath.Join(home, ".gitconfig.aether-tmp*"))
	if err != nil || len(left) != 0 {
		t.Errorf("staged files after ConfigureGit = %v (%v), want none", left, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); err != nil {
		t.Errorf("stat .gitconfig: %v", err)
	}
}

// Every write into a member home goes through writeNew, whose O_EXCL is
// what keeps a planted file from being written through.
func TestWriteNewRefusesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	planted := filepath.Join(dir, "target")
	if err := os.WriteFile(planted, []byte("planted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	err = writeNew(root, "target", []byte("server\n"), 0o644)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("writeNew over an existing file = %v, want fs.ErrExist", err)
	}
	contents, err := os.ReadFile(planted)
	if err != nil || string(contents) != "planted\n" {
		t.Fatalf("file = %q (%v), want it untouched", contents, err)
	}
}
