package memberhome

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
)

// Paths inside a member home. The container mounts the home as $HOME, so
// the signing key is at ~/.ssh/aether_signing for git and gh alike.
const (
	sshDirName     = ".ssh"
	signingKeyName = ".ssh/aether_signing"
	signingPubName = ".ssh/aether_signing.pub"
	gitConfigName  = ".gitconfig"
)

// EnsureSigningKey returns the member's commit signing public key line,
// generating the ed25519 pair on first call. The returned line is derived
// from the private key rather than read back from the .pub file: the home
// is writable from inside the member's containers, so only the private
// key decides what the public half is.
func (m *Manager) EnsureSigningKey(member domain.MemberID) (string, error) {
	root, err := m.openHome(member)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	existing, err := readRegularFile(root, signingKeyName)
	if err != nil {
		return "", fmt.Errorf("memberhome: read signing key for %q: %w", member, err)
	}
	if existing != nil {
		line, perr := publicKeyLine(existing, member)
		if perr != nil {
			return "", fmt.Errorf("memberhome: parse signing key for %q: %w", member, perr)
		}
		return line, nil
	}

	if dirErr := ensureDir(root, sshDirName); dirErr != nil {
		return "", fmt.Errorf("memberhome: prepare %s for %q: %w", sshDirName, member, dirErr)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", fmt.Errorf("memberhome: generate signing key for %q: %w", member, err)
	}
	comment := signingComment(member)
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", fmt.Errorf("memberhome: encode signing key for %q: %w", member, err)
	}
	if writeErr := writeNew(root, signingKeyName, pem.EncodeToMemory(block), 0o600); writeErr != nil {
		return "", fmt.Errorf("memberhome: write signing key for %q: %w", member, writeErr)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("memberhome: encode signing public key for %q: %w", member, err)
	}
	line := authorizedLine(sshPub, comment)
	// A .pub left behind without its private key describes nothing; the
	// pair is written together or not at all.
	if err := root.Remove(signingPubName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("memberhome: replace signing public key for %q: %w", member, err)
	}
	if err := writeNew(root, signingPubName, []byte(line+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("memberhome: write signing public key for %q: %w", member, err)
	}
	if err := chownLikeHome(root, sshDirName, signingKeyName, signingPubName); err != nil {
		return "", fmt.Errorf("memberhome: hand signing key to the home owner for %q: %w", member, err)
	}
	return line, nil
}

// HasSigningKey reports whether the member's home holds a signing key.
func (m *Manager) HasSigningKey(member domain.MemberID) (bool, error) {
	root, err := m.openHome(member)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(signingKeyName)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memberhome: inspect signing key for %q: %w", member, err)
	}
	return info.Mode().IsRegular(), nil
}

// SigningKey returns the member's private signing key bytes, or nil when
// they have none.
func (m *Manager) SigningKey(member domain.MemberID) ([]byte, error) {
	root, err := m.openHome(member)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	key, err := readRegularFile(root, signingKeyName)
	if err != nil {
		return nil, fmt.Errorf("memberhome: read signing key for %q: %w", member, err)
	}
	return key, nil
}

// ConfigureGit writes the member's git identity and commit signing
// settings into the home's .gitconfig. git edits a copy of the file, so
// sections written by other tools - gh's credential helper - survive.
//
// git --file follows a symlink to wherever it points, and the home is
// writable from inside the member's containers, so git never touches the
// home's own path: the current .gitconfig is read through the root, edited
// in a private temp file, and the result is renamed into place through the
// root. A .gitconfig swapped for a symlink between those steps is replaced
// by the rename, never written through.
func (m *Manager) ConfigureGit(ctx context.Context, member domain.MemberID, identity domain.GitIdentity) error {
	root, err := m.openHome(member)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	current, err := readRegularFile(root, gitConfigName)
	if err != nil {
		return fmt.Errorf("memberhome: read %s for %q: %w", gitConfigName, member, err)
	}
	scratch, err := os.CreateTemp("", "aether-gitconfig-*")
	if err != nil {
		return fmt.Errorf("memberhome: stage %s for %q: %w", gitConfigName, member, err)
	}
	path := scratch.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err = scratch.Write(current); err != nil {
		_ = scratch.Close()
		return fmt.Errorf("memberhome: stage %s for %q: %w", gitConfigName, member, err)
	}
	if err = scratch.Close(); err != nil {
		return fmt.Errorf("memberhome: stage %s for %q: %w", gitConfigName, member, err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
	for _, setting := range [][2]string{
		{"user.name", identity.Name},
		{"user.email", identity.Email},
		{"gpg.format", "ssh"},
		{"user.signingkey", "~/" + signingKeyName},
		{"commit.gpgsign", "true"},
	} {
		cmd := exec.CommandContext(ctx, "git", "config", "--file", path, setting[0], setting[1])
		cmd.Env = env
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("memberhome: set %s in %s for %q: %w: %s",
				setting[0], gitConfigName, member, runErr, strings.TrimSpace(string(out)))
		}
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("memberhome: read staged %s for %q: %w", gitConfigName, member, err)
	}
	staged := gitConfigName + ".aether-tmp"
	if err := root.Remove(staged); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("memberhome: clear staged %s for %q: %w", gitConfigName, member, err)
	}
	if err := writeNew(root, staged, edited, 0o644); err != nil {
		return fmt.Errorf("memberhome: write %s for %q: %w", gitConfigName, member, err)
	}
	if err := chownLikeHome(root, staged); err != nil {
		return fmt.Errorf("memberhome: hand %s to the home owner for %q: %w", gitConfigName, member, err)
	}
	if err := root.Rename(staged, gitConfigName); err != nil {
		return fmt.Errorf("memberhome: install %s for %q: %w", gitConfigName, member, err)
	}
	return nil
}

// openHome opens the member's home as an os.Root. Every signing path is
// reached through it, so a symlink planted from inside the member's
// container cannot lead a server-side read or write out of the home.
func (m *Manager) openHome(member domain.MemberID) (*os.Root, error) {
	home, err := m.Path(member)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, fmt.Errorf("memberhome: open home for %q: %w", member, err)
	}
	return root, nil
}

// readRegularFile returns the contents of name, nil when it is absent,
// and an error when it exists as anything but a regular file. The type
// check is made on the open descriptor so the file that was read is the
// file that was checked.
func readRegularFile(root *os.Root, name string) ([]byte, error) {
	link, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !link.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	f, err := root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return io.ReadAll(f)
}

// ensureDir creates name unless it already exists as a directory;
// anything else there - a symlink included - is refused, never replaced.
func ensureDir(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("%s is not a directory", name)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return root.Mkdir(name, 0o700)
}

// writeNew creates name and fails when anything already occupies it, so a
// symlink planted in the home is never written through.
func writeNew(root *os.Root, name string, data []byte, perm os.FileMode) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func signingComment(member domain.MemberID) string { return "aether " + string(member) }

func authorizedLine(key ssh.PublicKey, comment string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " " + comment
}

// publicKeyLine derives the authorized-keys line of an existing private
// key file.
func publicKeyLine(privatePEM []byte, member domain.MemberID) (string, error) {
	signer, err := ssh.ParsePrivateKey(privatePEM)
	if err != nil {
		return "", err
	}
	return authorizedLine(signer.PublicKey(), signingComment(member)), nil
}
