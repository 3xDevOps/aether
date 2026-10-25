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
	manager, err := New(root, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	home, err := manager.Path("member-1")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	return manager, home
}

const testPublicLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl aether member-1"

// The .pub is what gh ssh-key add uploads, and the container can replace
// it, so every write replaces whatever is there instead of writing through.
func TestWriteSigningPublicKeyReplacesAPlantedFile(t *testing.T) {
	manager, home := newSigningManager(t)
	if err := manager.WriteSigningPublicKey("member-1", testPublicLine); err != nil {
		t.Fatalf("WriteSigningPublicKey: %v", err)
	}
	pubPath := filepath.Join(home, ".ssh", "aether_signing.pub")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pubPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, pubPath); err != nil {
		t.Fatal(err)
	}

	if err := manager.WriteSigningPublicKey("member-1", testPublicLine); err != nil {
		t.Fatalf("second WriteSigningPublicKey: %v", err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(home, ".ssh"): 0o700,
		pubPath:                     0o644,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != want || info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s mode = %v, want %v", path, info.Mode(), want)
		}
	}
	if got, err := os.ReadFile(pubPath); err != nil || string(got) != testPublicLine+"\n" {
		t.Errorf("public key file = %q (%v), want %q", got, err, testPublicLine)
	}
	if got, err := os.ReadFile(elsewhere); err != nil || string(got) != "untouched\n" {
		t.Errorf("symlink target = %q (%v), want it untouched", got, err)
	}
}

func TestRemoveLegacySigningKey(t *testing.T) {
	manager, home := newSigningManager(t)
	if err := manager.RemoveLegacySigningKey("member-1"); err != nil {
		t.Fatalf("RemoveLegacySigningKey without a .ssh: %v", err)
	}
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, ".ssh", "aether_signing")
	if err := os.WriteFile(keyPath, []byte("key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveLegacySigningKey("member-1"); err != nil {
		t.Fatalf("RemoveLegacySigningKey: %v", err)
	}
	if _, err := os.Lstat(keyPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("legacy key stat = %v, want it gone", err)
	}
	if err := manager.RemoveLegacySigningKey("member-1"); err != nil {
		t.Fatalf("RemoveLegacySigningKey on an absent key: %v", err)
	}
}

// A container can write anything into its own home, so a symlink where the
// key belongs must stop the server rather than lead it somewhere else.
// The link points inside the home, where os.Root would follow it happily:
// only the regular-file checks can refuse this one.
func TestLegacySigningKeyRefusesASymlink(t *testing.T) {
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
	if _, err := manager.LegacySigningKey("member-1"); err == nil {
		t.Fatal("LegacySigningKey accepted a symlinked key path")
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
	if err := manager.ConfigureGit(t.Context(), "member-1", identity, testPublicLine); err != nil {
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
		"user.signingkey=key::" + testPublicLine,
		"commit.gpgsign=true",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf(".gitconfig entries = %q, want %q", got, want)
	}

	// A second call rewrites the same five keys rather than appending.
	if err := manager.ConfigureGit(t.Context(), "member-1", identity, testPublicLine); err != nil {
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
	err := manager.ConfigureGit(t.Context(), "member-1", domain.GitIdentity{Name: "Ada", Email: "ada@example.com"}, testPublicLine)
	if err == nil {
		t.Fatal("ConfigureGit accepted a symlinked .gitconfig")
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil || len(contents) != 0 {
		t.Fatalf("symlink target = %q (%v), want it untouched", contents, readErr)
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
	if _, err := manager.LegacySigningKey("member-1"); err == nil || !strings.Contains(err.Error(), "16384 byte limit") {
		t.Errorf("LegacySigningKey on a 2 GiB key = %v, want the 16384 byte cap", err)
	}
	err := manager.ConfigureGit(t.Context(), "member-1", domain.GitIdentity{Name: "Ada", Email: "ada@example.com"}, testPublicLine)
	if err == nil || !strings.Contains(err.Error(), "262144 byte limit") {
		t.Errorf("ConfigureGit on a 2 GiB .gitconfig = %v, want the 262144 byte cap", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the two capped reads took %v, want a refusal without reading 2 GiB", elapsed)
	}
	staged, err := filepath.Glob(filepath.Join(home, ".gitconfig.aether-tmp*"))
	if err != nil || len(staged) != 0 {
		t.Errorf("staged files = %v (%v), want none", staged, err)
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

	if err := manager.ConfigureGit(t.Context(), "member-1", domain.GitIdentity{Name: "Ada", Email: "ada@example.com"}, testPublicLine); err != nil {
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
