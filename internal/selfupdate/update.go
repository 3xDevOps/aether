package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/3xDevOps/Aether/internal/macinstall"
)

// ErrWindows refuses the in-place swap on Windows: the OS cannot rename
// over a running executable, so the one documented path is a manual
// download rather than a half-supported dance.
var ErrWindows = errors.New("self-update is not supported on Windows; download the release from https://github.com/" + Repo + "/releases")

// adminInstaller installs one staged file through the platform's
// administrator dialog. It is the one step that runs with privileges, and
// it runs outside this process.
type adminInstaller func(ctx context.Context, req macinstall.Request, prompt string) error

// adminInstall is the installer behind UpdateWithAuthorization, a variable
// so tests can stand in for the dialog.
var adminInstall adminInstaller = macinstall.Install

// adminPlatform reports whether this OS has the administrator dialog the
// authorized path relies on. Only macOS does: Linux desktops have polkit,
// but Aether's Linux installs are servers and terminals, where sudo is
// the right tool. A variable so the path is testable off a Mac.
var adminPlatform = runtime.GOOS == "darwin"

// checkRootOwner reports whether an authorized install must leave a
// root-owned file, which it does wherever root made the copy. A variable
// so a test can take the authorized path as an ordinary user.
var checkRootOwner = runtime.GOOS == "darwin"

// hasGUISession reports whether the dialog has a screen to appear on. A
// variable so tests off a Mac can say yes.
var hasGUISession = macinstall.HasGUISession

// canAuthorize reports whether an unwritable dir is one the administrator
// dialog may install into: this is the platform with the dialog, there
// is a GUI session for it, and only root can write the directory or any
// directory above it. That last condition is what the privileged command
// relies on. It stages a temp file in that directory by name and renames
// it over the binary by path, and a directory some other account can
// write - or an ancestor it could swap for a symlink - would let that
// account redirect root's copy, chmod and rename between its steps. Such
// a directory, one user's Homebrew prefix used by another account or a
// root-owned bin directory under a user's home, gets the sudo command
// instead: sudo stages with O_EXCL and renames its own inode, which the
// fixed shell command cannot.
func canAuthorize(dir string) bool {
	return adminPlatform && hasGUISession() && pathIsRootOnly(dir)
}

// pathIsRootOnly reports whether dir and every directory above it are
// root's alone. A variable so tests, which cannot create a root-owned
// directory, can check the same rule against directories of their own.
var pathIsRootOnly = func(dir string) bool { return ownedOnlyBy(dir, 0, "/") }

// ownedOnlyBy walks from dir up to top, inclusive, and reports whether
// every directory on the way is owned by uid, is not a symlink, has no
// group or other write bit, and carries no access control list.
func ownedOnlyBy(dir string, uid uint32, top string) bool {
	for p := dir; ; p = filepath.Dir(p) {
		if !dirOwnedOnlyBy(p, uid) {
			return false
		}
		if p == top || p == filepath.Dir(p) {
			return true
		}
	}
}

func dirOwnedOnlyBy(p string, uid uint32) bool {
	info, err := os.Lstat(p)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	owner, ok := fileOwner(info)
	return ok && owner == uid && !hasACL(p)
}

// hasACL reports whether p carries an access control list, which can
// grant another account write access that the mode bits do not show. Go
// has no ACL API; `ls -ld` marks such an entry with a trailing "+" in the
// mode column on macOS and on Linux alike. An ls that fails counts as an
// ACL: the answer decides whether root may write here, and a guess in
// the permissive direction is the wrong guess.
func hasACL(p string) bool {
	out, err := exec.Command("/bin/ls", "-ld", p).Output()
	if err != nil {
		return true
	}
	fields := strings.Fields(string(out))
	return len(fields) == 0 || strings.HasSuffix(fields[0], "+")
}

// Update replaces the running binary - and aether-server beside it on a
// Linux server host - with tag's release assets from baseURL, returning
// the paths it replaced in order. It prints nothing: callers report. It
// never asks for privileges: a binary this user cannot write is refused
// with the sudo command to run.
func Update(ctx context.Context, baseURL, tag string) ([]string, error) {
	return update(ctx, baseURL, tag, nil)
}

// UpdateWithAuthorization is Update for a caller with a GUI session: on
// macOS, a binary this user cannot write is installed through the
// system's administrator dialog instead of being refused. Everywhere
// else it behaves exactly like Update.
func UpdateWithAuthorization(ctx context.Context, baseURL, tag string) ([]string, error) {
	return update(ctx, baseURL, tag, adminInstall)
}

func update(ctx context.Context, baseURL, tag string, admin adminInstaller) ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate this binary: %w", err)
	}
	// A Linux box with aether-server next to the CLI is a server host;
	// update both so the pair never skews. Elsewhere the server asset does
	// not exist and there is nothing to update.
	var companions []string
	if runtime.GOOS == "linux" {
		companions = append(companions, "aether-server")
	}
	return updateBinaries(ctx, baseURL, tag, self, "aether", companions, admin)
}

// UpdateBinaries replaces the binary at self with tag's release asset
// named primary, along with each companion asset that already sits beside
// it, and returns the paths it replaced in order. Symlinks are resolved so
// the real file is swapped rather than the link. It never asks for
// privileges.
//
// Every asset is downloaded and checksum-verified before any of them is
// replaced, so a bad tag, a network error, or a checksum mismatch leaves
// every binary exactly as it was - a half-updated pair is worse than no
// update, because the CLI and the server would disagree about the
// protocol. Only the renames at the end can leave a partial swap, and a
// rename within one directory fails only when the filesystem does; the
// error then names which binaries were already replaced.
func UpdateBinaries(ctx context.Context, baseURL, tag, self, primary string, companions ...string) ([]string, error) {
	return updateBinaries(ctx, baseURL, tag, self, primary, companions, nil)
}

func updateBinaries(ctx context.Context, baseURL, tag, self, primary string, companions []string, admin adminInstaller) ([]string, error) {
	if runtime.GOOS == "windows" {
		return nil, ErrWindows
	}
	self, err := filepath.EvalSymlinks(self)
	if err != nil {
		return nil, fmt.Errorf("resolve this binary: %w", err)
	}
	dir := filepath.Dir(self)
	stageDir := dir
	authorize := false
	if err := CheckWritable(dir); err != nil {
		// Anything but a permission error - a read-only filesystem, a
		// directory that is gone - is not something a password fixes.
		if admin == nil || !errors.Is(err, os.ErrPermission) || !canAuthorize(dir) {
			return nil, err
		}
		// The tag reaches the dialog's text, so it is checked here even
		// though it came from the release redirect that Check resolved.
		if !ValidTag(tag) {
			return nil, fmt.Errorf("%q is not a release tag", tag)
		}
		if stageDir, err = privateStageDir(); err != nil {
			return nil, err
		}
		authorize = true
	}

	assets := baseURL + "/releases/download/" + tag
	suffix := "-" + runtime.GOOS + "-" + runtime.GOARCH
	targets := []struct{ asset, dst string }{{primary + suffix, self}}
	for _, name := range companions {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		targets = append(targets, struct{ asset, dst string }{name + suffix, path})
	}

	staged := make([]*Staged, 0, len(targets))
	defer func() {
		for _, s := range staged {
			s.Discard()
		}
	}()
	for _, t := range targets {
		s, serr := stageIn(ctx, assets, t.asset, t.dst, stageDir)
		if serr != nil {
			return nil, serr
		}
		staged = append(staged, s)
	}

	replaced := make([]string, 0, len(staged))
	for _, s := range staged {
		var cerr error
		if authorize {
			cerr = s.installWithAuthorization(ctx, admin, tag)
		} else {
			cerr = s.Commit()
		}
		if cerr != nil {
			if len(replaced) == 0 {
				return nil, cerr
			}
			return replaced, fmt.Errorf("replaced %s but %w", strings.Join(replaced, ", "), cerr)
		}
		replaced = append(replaced, s.Path())
	}
	return replaced, nil
}

// installWithAuthorization has root copy the staged file over its
// destination through the administrator dialog, then checks the result
// as this user: root wrote what it was asked to and nothing else. Each
// staged file is one dialog; on macOS there is exactly one, the CLI, so
// the user is asked once. The staged file stays for Discard: root copied
// it rather than renaming it, so committed is never set.
func (s *Staged) installWithAuthorization(ctx context.Context, admin adminInstaller, tag string) error {
	prompt := fmt.Sprintf("Aether wants to replace %s with aether %s. "+
		"macOS shows this request as osascript, the tool Aether asks through. "+
		"Aether never sees your password.", s.dst, tag)
	err := admin(ctx, macinstall.Request{Src: s.tmp, Dst: s.dst, SHA256: s.sum}, prompt)
	switch {
	case err == nil:
		return verifyInstalled(s.dst, s.sum)
	case errors.Is(err, macinstall.ErrCanceled), errors.Is(err, macinstall.ErrNoSession):
		// Nothing was attempted on the file; the dialog's own line is the
		// whole story, and the prompt already named the path.
		return err
	}
	return fmt.Errorf("replace %s: %w", s.dst, err)
}

// verifyInstalled checks the binary root left behind: a regular file,
// executable, root-owned on the platform that asked root, and hashing to
// the release digest. Root already compared its copy, so a mismatch here
// means something replaced the file after that, and the caller must not
// run it.
func verifyInstalled(dst, sum string) error {
	info, err := os.Lstat(dst)
	if err != nil {
		return fmt.Errorf("inspect installed %s: %w", dst, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("installed %s is not a regular file; do not run it", dst)
	}
	if info.Mode().Perm() != 0o755 {
		return fmt.Errorf("installed %s has mode %v, want 0755", dst, info.Mode().Perm())
	}
	if uid, ok := fileOwner(info); checkRootOwner && ok && uid != 0 {
		return fmt.Errorf("installed %s is owned by uid %d, not root; do not run it", dst, uid)
	}
	got, err := fileSHA256(dst)
	if err != nil {
		return fmt.Errorf("hash installed %s: %w", dst, err)
	}
	if got != sum {
		return fmt.Errorf("installed %s does not match the release checksum; do not run it", dst)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// privateStageDir returns a directory of this user's own to stage a
// download in when the binary's directory is root's: <user cache>/aether/
// update, beside the desktop build's directory. It is created 0700 and
// refused when something else is there - a symlink, another user's
// directory - because root will read from it.
func privateStageDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find a directory to stage the update in: %w", err)
	}
	dir := filepath.Join(cache, "aether", "update")
	if err = os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(dir), err)
	}
	if err = os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory; remove it and retry", dir)
	}
	if uid, ok := fileOwner(info); ok && uid != uint32(os.Getuid()) {
		return "", fmt.Errorf("%s is owned by uid %d, not this user; remove it and retry", dir, uid)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("make %s private: %w", dir, err)
		}
	}
	return dir, nil
}

// CheckWritable proves dir accepts a new file before anything is
// downloaded: Apply stages there, and a rename that fails after a
// hundred-megabyte download is a worse way to learn the binary lives in a
// root-owned directory. It is also how the server reports whether it can
// update itself at all.
func CheckWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".aether-update-probe-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			// The wrapped error comes first so the command to copy ends
			// the sentence instead of running into a second colon.
			return fmt.Errorf("%w: %s is not writable by this user; re-run as `sudo aether update`", err, dir)
		}
		return fmt.Errorf("stage an update in %s: %w", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return fmt.Errorf("close probe file in %s: %w", dir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove probe file in %s: %w", dir, err)
	}
	return nil
}
