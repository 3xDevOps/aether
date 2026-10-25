package memberhome

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

// The container mounts the home as $HOME. Only the public half of the
// signing key lives here; gh ssh-key add uploads it from this path.
const (
	sshDirName     = ".ssh"
	signingKeyName = ".ssh/aether_signing"
	signingPubName = "aether_signing.pub"
	gitConfigName  = ".gitconfig"
	// stagedGitConfigPrefix names ConfigureGit's temp files; nothing else in a home carries it.
	stagedGitConfigPrefix = gitConfigName + ".aether-tmp"
)

// Read limits for member-home files, which are agent-written input: a planted
// 2 GiB file must not cost the server its memory.
const (
	maxSigningKeyBytes = 16 << 10
	maxGitConfigBytes  = 256 << 10
)

// WriteSigningPublicKey replaces whatever occupies ~/.ssh/aether_signing.pub
// with line.
func (m *Manager) WriteSigningPublicKey(member domain.MemberID, line string) error {
	root, err := m.openHome(member)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err = ensureDir(root, sshDirName); err != nil {
		return fmt.Errorf("memberhome: prepare %s for %q: %w", sshDirName, member, err)
	}
	sshRoot, err := rootfs.OpenRoot(root, sshDirName)
	if err != nil {
		return fmt.Errorf("memberhome: open %s for %q: %w", sshDirName, member, err)
	}
	defer func() { _ = sshRoot.Close() }()
	if err = sshRoot.RemoveAll(signingPubName); err != nil {
		return fmt.Errorf("memberhome: replace the signing public key for %q: %w", member, err)
	}
	pub, err := sshRoot.OpenFile(signingPubName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("memberhome: write the signing public key for %q: %w", member, err)
	}
	if _, err = pub.Write([]byte(line + "\n")); err == nil {
		err = chownFileLikeHome(root, pub)
	}
	if closeErr := pub.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("memberhome: write the signing public key for %q: %w", member, err)
	}
	return nil
}

// LegacySigningKey returns the private key releases before the server-held
// key kept at ~/.ssh/aether_signing, or nil when there is none. It never
// creates the home.
func (m *Manager) LegacySigningKey(member domain.MemberID) ([]byte, error) {
	root, err := m.openExistingHome(member)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	key, err := readRegularFile(root, signingKeyName, maxSigningKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("memberhome: read %s for %q: %w", signingKeyName, member, err)
	}
	return key, nil
}

// RemoveLegacySigningKey deletes ~/.ssh/aether_signing. Removing an absent
// key is a successful no-op.
func (m *Manager) RemoveLegacySigningKey(member domain.MemberID) error {
	root, err := m.openExistingHome(member)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	parent, err := rootfs.OpenRoot(root, path.Dir(signingKeyName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memberhome: open %s for %q: %w", sshDirName, member, err)
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Remove(path.Base(signingKeyName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("memberhome: remove %s for %q: %w", signingKeyName, member, err)
	}
	return nil
}

// ConfigureGit writes the member's git identity and signing settings into
// .gitconfig, keeping sections other tools wrote. signingKey is the public
// key line git hands ssh-keygen, which signs through the agent holding its
// private half. git --file follows symlinks, so git edits a private temp
// copy and the result is renamed into place through the root.
func (m *Manager) ConfigureGit(ctx context.Context, member domain.MemberID, identity domain.GitIdentity, signingKey string) error {
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
		{"user.signingkey", "key::" + signingKey},
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

// openHome opens the member's home as an os.Root so a symlink planted from a
// container cannot lead a server-side read or write out of the home.
func (m *Manager) openHome(member domain.MemberID) (*os.Root, error) {
	if err := validateMemberID(string(member)); err != nil {
		return nil, fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	if m.isArchived(member) {
		return nil, fmt.Errorf("memberhome: member %q was merged and its home moved aside: %w", member, fs.ErrNotExist)
	}
	roots, err := openManagedRoot(m.root, true)
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
	_ = roots.Close()
	return m.openExistingHome(member)
}

func (m *Manager) openExistingHome(member domain.MemberID) (*os.Root, error) {
	if err := validateMemberID(string(member)); err != nil {
		return nil, fmt.Errorf("memberhome: member %q: %w", member, err)
	}
	roots, err := openManagedRoot(m.root, false)
	if err != nil {
		return nil, fmt.Errorf("memberhome: open root: %w", err)
	}
	home, err := rootfs.OpenRoot(roots, string(member))
	_ = roots.Close()
	if err != nil {
		return nil, fmt.Errorf("memberhome: open home for %q: %w", member, err)
	}
	return home, nil
}

// readRegularFile returns name's contents, or nil when it is absent. It
// refuses anything but a single-link regular file of at most limit bytes.
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

// stageInHome writes data to a fresh O_EXCL file for the caller to rename
// over .gitconfig. Leftovers under the prefix are cleared first, since a
// directory planted there would block every later call.
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
