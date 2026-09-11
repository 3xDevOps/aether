package memberhome

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

// Paths inside a member home. The container mounts the home as $HOME, so
// the signing key is at ~/.ssh/aether_signing for git and gh alike.
const (
	sshDirName     = ".ssh"
	signingKeyName = ".ssh/aether_signing"
	signingPubName = ".ssh/aether_signing.pub"
	gitConfigName  = ".gitconfig"
	// stagedGitConfigPrefix names the temporary files ConfigureGit renames
	// into place. Nothing in a home legitimately carries one.
	stagedGitConfigPrefix = gitConfigName + ".aether-tmp"
)

// The most the server reads from a member home. Everything under it is
// written from inside the member's containers, so a file there is
// agent-chosen input: an ed25519 private key is under a kilobyte and a
// hand-grown .gitconfig under a few, and reading a 2 GiB one planted in
// their place would cost the server its memory.
const (
	maxSigningKeyBytes = 16 << 10
	maxGitConfigBytes  = 256 << 10
)

// EnsureSigningKey returns the member's commit signing public key line,
// generating the ed25519 pair on first call. The returned line is derived
// from the private key rather than read back from the .pub file: the home
// is writable from inside the member's containers, so only the private
// key decides what the public half is. For the same reason every call
// rewrites the .pub from the private key and puts the private key back to
// 0600 - what the container left there is not what the next caller reads.
func (m *Manager) EnsureSigningKey(member domain.MemberID) (string, error) {
	root, err := m.openHome(member)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	existing, err := readRegularFile(root, signingKeyName, maxSigningKeyBytes)
	if err != nil {
		return "", fmt.Errorf("memberhome: read signing key for %q: %w", member, err)
	}
	if existing != nil {
		line, perr := publicKeyLine(existing, member)
		if perr != nil {
			return "", fmt.Errorf("memberhome: parse signing key for %q: %w", member, perr)
		}
		if perr := restrictKey(root); perr != nil {
			return "", fmt.Errorf("memberhome: restrict signing key for %q: %w", member, perr)
		}
		if werr := installPublicKey(root, line); werr != nil {
			return "", fmt.Errorf("memberhome: write signing public key for %q: %w", member, werr)
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
	if err := installPublicKey(root, line); err != nil {
		return "", fmt.Errorf("memberhome: write signing public key for %q: %w", member, err)
	}
	if err := chownLikeHome(root, sshDirName); err != nil {
		return "", fmt.Errorf("memberhome: hand signing directory to the home owner for %q: %w", member, err)
	}
	return line, nil
}

// installPublicKey writes line as the home's .pub, replacing whatever
// occupies that path. A .pub the container wrote describes nothing: only
// the private key decides what the public half is.
func installPublicKey(root *os.Root, line string) error {
	sshRoot, err := rootfs.OpenRoot(root, sshDirName)
	if err != nil {
		return err
	}
	defer func() { _ = sshRoot.Close() }()
	if err = sshRoot.RemoveAll("aether_signing.pub"); err != nil {
		return err
	}
	pub, err := sshRoot.OpenFile("aether_signing.pub", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err = pub.Write([]byte(line + "\n")); err == nil {
		err = chownFileLikeHome(root, pub)
	}
	if closeErr := pub.Close(); err == nil {
		err = closeErr
	}
	return err
}

// restrictKey puts the private key back to 0600. The mode is changed
// through the open descriptor so a symlink swapped in mid-call cannot
// take the chmod somewhere else.
func restrictKey(root *os.Root) error {
	f, info, err := openRegular(root, signingKeyName)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if info.Mode().Perm() == 0o600 {
		return nil
	}
	return f.Chmod(0o600)
}

// HasSigningKey reports whether the member's home holds a signing key.
func (m *Manager) HasSigningKey(member domain.MemberID) (bool, error) {
	root, err := m.openHome(member)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	parent, err := rootfs.OpenRoot(root, path.Dir(signingKeyName))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memberhome: inspect signing key for %q: %w", member, err)
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Lstat(path.Base(signingKeyName))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memberhome: inspect signing key for %q: %w", member, err)
	}
	return info.Mode().IsRegular() && !hasMultipleLinks(info), nil
}

// SigningKey returns the member's private signing key bytes, or nil when
// they have none.
//
// A key the home holds but ssh cannot parse counts as none: the container
// can overwrite its own member's key, and the caller signs commits with
// what comes back. Handing git a corrupt key would fail the commit, so the
// signature is what is dropped, with a line in the log naming the member.
func (m *Manager) SigningKey(member domain.MemberID) ([]byte, error) {
	root, err := m.openHome(member)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	key, err := readRegularFile(root, signingKeyName, maxSigningKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("memberhome: read signing key for %q: %w", member, err)
	}
	if key == nil {
		return nil, nil
	}
	if _, perr := ssh.ParsePrivateKey(key); perr != nil {
		slog.Warn("memberhome: signing key cannot be parsed; commits stay unsigned",
			"member", member, "error", perr)
		return nil, nil
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
	current, err := readRegularFile(root, gitConfigName, maxGitConfigBytes)
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
	staged, err := stageInHome(root, edited)
	if err != nil {
		return fmt.Errorf("memberhome: write %s for %q: %w", gitConfigName, member, err)
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
	if err := validateMemberID(string(member)); err != nil {
		return nil, fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return nil, fmt.Errorf("memberhome: create root: %w", err)
	}
	roots, err := os.OpenRoot(m.root)
	if err != nil {
		return nil, fmt.Errorf("memberhome: open root: %w", err)
	}
	if _, err = roots.Lstat(string(member)); errors.Is(err, fs.ErrNotExist) {
		if err = roots.Mkdir(string(member), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			_ = roots.Close()
			return nil, fmt.Errorf("memberhome: create home for %q: %w", member, err)
		}
	} else if err != nil {
		_ = roots.Close()
		return nil, fmt.Errorf("memberhome: stat home for %q: %w", member, err)
	}
	home, err := rootfs.OpenRoot(roots, string(member))
	_ = roots.Close()
	if err != nil {
		return nil, fmt.Errorf("memberhome: open home for %q: %w", member, err)
	}
	return home, nil
}

// readRegularFile returns the contents of name, nil when it is absent, and an
// error when the pinned descriptor is anything but a regular file or holds
// more than limit bytes. rootfs pins every directory component and opens the
// leaf without following a symlink; the size check precedes the bounded read.
func readRegularFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := rootfs.Open(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
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
	if hasMultipleLinks(info) {
		return nil, fmt.Errorf("%s has multiple links", name)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit", name, info.Size(), limit)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// openRegular opens name for reading and refuses anything but a regular file.
func openRegular(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	f, err := rootfs.Open(root, name)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || hasMultipleLinks(info) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", name)
	}
	return f, info, nil
}

// ensureDir creates name unless it already exists as a directory;
// anything else there - a symlink included - is refused, never replaced.
func ensureDir(root *os.Root, name string) error {
	clean := path.Clean(name)
	if clean == "." {
		return nil
	}
	current := root
	var owned *os.Root
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			if owned != nil {
				_ = owned.Close()
			}
			return fmt.Errorf("%s is not a directory", name)
		}
		child, err := rootfs.OpenRoot(current, seg)
		created := false
		if errors.Is(err, fs.ErrNotExist) {
			if mkdirErr := current.Mkdir(seg, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				if owned != nil {
					_ = owned.Close()
				}
				return mkdirErr
			}
			child, err = rootfs.OpenRoot(current, seg)
			created = err == nil
		}
		if err != nil {
			if owned != nil {
				_ = owned.Close()
			}
			return err
		}
		if created {
			if err := chownLikeHomeAt(root, current, seg); err != nil {
				_ = child.Close()
				if owned != nil {
					_ = owned.Close()
				}
				return err
			}
		}
		if owned != nil {
			_ = owned.Close()
		}
		current, owned = child, child
	}
	if owned != nil {
		_ = owned.Close()
	}
	return nil
}

func writeNew(root *os.Root, name string, data []byte, perm os.FileMode) error {
	parent := root
	var owned *os.Root
	if dir := path.Dir(name); dir != "." {
		var err error
		owned, err = rootfs.OpenRoot(root, dir)
		if err != nil {
			return err
		}
		parent = owned
	}
	defer func() {
		if owned != nil {
			_ = owned.Close()
		}
	}()
	f, err := parent.OpenFile(path.Base(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := chownFileLikeHome(root, f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// stageInHome writes data to a fresh file in the home and returns its
// name, for the caller to rename over .gitconfig. The name is random and
// created with O_EXCL, so whatever an agent plants there cannot be
// written through; anything left over under the prefix - by a crashed
// call or by the container - is removed first, since a directory in that
// spot would otherwise block every later call.
func stageInHome(root *os.Root, data []byte) (string, error) {
	if err := clearStaged(root); err != nil {
		return "", err
	}
	var err error
	for range 10 {
		name := stagedGitConfigPrefix + rand.Text()
		if err = writeNew(root, name, data, 0o644); err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", err
}

// clearStaged removes every leftover staged .gitconfig in the home.
func clearStaged(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range entries {
		if !strings.HasPrefix(name, stagedGitConfigPrefix) {
			continue
		}
		if err := root.RemoveAll(name); err != nil {
			return err
		}
	}
	return nil
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
