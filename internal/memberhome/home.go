package memberhome

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	profilesvc "github.com/3xDevOps/Aether/internal/profile"
	"github.com/3xDevOps/Aether/internal/rootfs"
)

// HomeEntry is one immediate child of a directory in a member's home. Kind is
// "dir", "file" or "link".
type HomeEntry struct {
	Name   string
	Kind   string
	Size   int64
	Locked bool
}

// HomeError is a refusal whose Reason names only home-relative paths, so it
// can be returned to the member. Err is the ErrConfig* kind it stands for.
type HomeError struct {
	Err    error
	Reason string
}

func (e *HomeError) Error() string { return e.Reason }
func (e *HomeError) Unwrap() error { return e.Err }

func refuse(kind error, format string, args ...any) error {
	return &HomeError{Err: kind, Reason: fmt.Sprintf(format, args...)}
}

func refuseLocked(rel, verb string) error {
	return refuse(ErrConfigDenied, "%s is a credential file; %s it in the Environment terminal", rel, verb)
}

func refusePath(raw string) error {
	return refuse(ErrConfigDenied, "%q is not a path inside the home", raw)
}

var shippedDenyNames = sync.OnceValue(func() []string {
	var names []string
	for _, p := range harness.Profiles() {
		names = append(names, p.DenyNames...)
	}
	return names
})

// homeCredentialPaths are the credential files known by where they are, not
// by name. A trailing slash covers everything below except *.pub.
var homeCredentialPaths = []string{githubCredentialPath, ".git-credentials", ".netrc", ".ssh/"}

// homeLocked reports whether rel is a credential file: listed, never read.
func homeLocked(rel string) bool {
	for _, p := range homeCredentialPaths {
		if rel == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p) && !strings.HasSuffix(rel, ".pub")) {
			return true
		}
	}
	return profilesvc.DeniedBasename(rel, shippedDenyNames())
}

// homeHoldsCredentials reports whether renaming the directory rel would move
// a homeCredentialPaths entry to a path where it is no longer locked.
func homeHoldsCredentials(rel string) bool {
	for _, p := range homeCredentialPaths {
		if strings.HasPrefix(p, rel+"/") {
			return true
		}
	}
	return false
}

// inHome runs op on one path of the member's home under that member's home
// lock. A failure that is only an ErrConfig* kind becomes a refusal naming
// the path.
func (m *Manager) inHome(member domain.MemberID, raw string, allowRoot bool, op func(home *os.Root, rel string) error) error {
	rel, err := relPath(raw, allowRoot)
	if err != nil {
		return refusePath(raw)
	}
	home, err := m.openHome(member)
	if err != nil {
		return err
	}
	defer func() { _ = home.Close() }()
	defer m.lock(string(member))()
	return homeRefusal(rel, op(home, rel))
}

func homeRefusal(rel string, err error) error {
	var refusal *HomeError
	switch {
	case err == nil, errors.As(err, &refusal):
		return err
	case errors.Is(err, ErrConfigConflict):
		return refuse(ErrConfigConflict, "%s changed on the server; reload it and try again", rel)
	case errors.Is(err, ErrConfigNotFound), errors.Is(err, fs.ErrNotExist):
		return refuse(ErrConfigNotFound, "%s was not found", rel)
	case errors.Is(err, ErrConfigTooLarge):
		return refuse(ErrConfigTooLarge, "%s is over the 64 MiB limit", rel)
	case errors.Is(err, ErrConfigBinary):
		return refuse(ErrConfigBinary, "%s is binary or not valid UTF-8, so it cannot be saved as text", rel)
	case errors.Is(err, ErrConfigDenied):
		return refuse(ErrConfigDenied, "%s is refused: only regular files and folders are served, and symlinks or hard links are never followed", rel)
	}
	return err
}

func openHomeParent(home *os.Root, rel string) (*os.Root, error) {
	parent, err := rootfs.OpenRoot(home, path.Dir(rel))
	if err != nil {
		return nil, homeRefusal(path.Dir(rel), configPathError(err))
	}
	return parent, nil
}

// HomeTree lists every immediate child of a home directory, credential files
// and symlinks included. Sockets, FIFOs and devices are omitted.
func (m *Manager) HomeTree(ctx context.Context, member domain.MemberID, raw string) ([]HomeEntry, error) {
	var out []HomeEntry
	err := m.inHome(member, raw, true, func(home *os.Root, rel string) error {
		dir, err := rootfs.OpenRoot(home, rel)
		if err != nil {
			return configPathError(err)
		}
		defer func() { _ = dir.Close() }()
		entries, err := fs.ReadDir(dir.FS(), ".")
		if err != nil {
			return err
		}
		out = make([]HomeEntry, 0, len(entries))
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			// Agents write the home while it is listed.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			var kind string
			switch {
			case info.IsDir():
				kind = "dir"
			case info.Mode().IsRegular():
				kind = "file"
			case info.Mode()&os.ModeSymlink != 0:
				kind = "link"
			default:
				continue
			}
			out = append(out, HomeEntry{
				Name: entry.Name(), Kind: kind, Size: info.Size(),
				Locked: kind == "file" && homeLocked(path.Join(rel, entry.Name())),
			})
		}
		return nil
	})
	return out, err
}

// HomeRead reads one editor file like ConfigRead, from anywhere in the home.
func (m *Manager) HomeRead(ctx context.Context, member domain.MemberID, raw string) (ConfigRead, error) {
	var out ConfigRead
	err := m.inHome(member, raw, false, func(home *os.Root, rel string) (err error) {
		if homeLocked(rel) {
			return refuseLocked(rel, "read")
		}
		out, err = readConfigFile(ctx, home, rel)
		return err
	})
	return out, err
}

// HomeWrite saves one editor file like ConfigWrite, anywhere in the home.
func (m *Manager) HomeWrite(member domain.MemberID, raw string, content []byte, expected string) (ConfigRead, error) {
	var out ConfigRead
	err := m.inHome(member, raw, false, func(home *os.Root, rel string) (err error) {
		switch {
		case len(content) > ConfigMaxFileBytes:
			return ErrConfigTooLarge
		case isConfigBinary(content):
			return ErrConfigBinary
		case homeLocked(rel):
			return refuseLocked(rel, "edit")
		}
		out, err = writeConfigFile(home, home, rel, content, expected)
		if expected == "" && errors.Is(err, ErrConfigConflict) {
			return refuse(ErrConfigConflict, "%s already exists", rel)
		}
		return err
	})
	return out, err
}

// HomeMkdir creates one directory whose parent exists.
func (m *Manager) HomeMkdir(member domain.MemberID, raw string) error {
	return m.inHome(member, raw, false, func(home *os.Root, rel string) error {
		parent, err := openHomeParent(home, rel)
		if err != nil {
			return err
		}
		defer func() { _ = parent.Close() }()
		name := path.Base(rel)
		if err = parent.Mkdir(name, 0o700); errors.Is(err, fs.ErrExist) {
			return refuse(ErrConfigConflict, "%s already exists", rel)
		} else if err != nil {
			return err
		}
		return chownLikeHomeAt(home, parent, name)
	})
}

// HomeRename renames or moves a file, directory or symlink without replacing
// anything at the destination. A credential file stays where it is locked.
func (m *Manager) HomeRename(member domain.MemberID, rawFrom, rawTo string) error {
	return m.inHome(member, rawFrom, false, func(home *os.Root, from string) error {
		to, err := relPath(rawTo, false)
		if err != nil {
			return refusePath(rawTo)
		}
		if homeLocked(from) {
			return refuseLocked(from, "rename or move")
		}
		if homeHoldsCredentials(from) {
			return refuse(ErrConfigDenied, "%s holds credential files; rename or move it in the Environment terminal", from)
		}
		if strings.HasPrefix(to, from+"/") {
			return refuse(ErrConfigDenied, "%s cannot be moved into itself", from)
		}
		source, err := openHomeParent(home, from)
		if err != nil {
			return err
		}
		defer func() { _ = source.Close() }()
		target, err := openHomeParent(home, to)
		if err != nil {
			return err
		}
		defer func() { _ = target.Close() }()
		err = renameCacheEntry(source, path.Base(from), target, path.Base(to))
		if errors.Is(err, fs.ErrExist) {
			return refuse(ErrConfigConflict, "%s already exists", to)
		}
		return err
	})
}

// HomeRemove deletes a file or symlink, or a directory with everything in it.
func (m *Manager) HomeRemove(member domain.MemberID, raw string) error {
	return m.inHome(member, raw, true, func(home *os.Root, rel string) error {
		if rel == "." {
			return refuse(ErrConfigDenied, "the home itself cannot be deleted")
		}
		parent, err := openHomeParent(home, rel)
		if err != nil {
			return err
		}
		defer func() { _ = parent.Close() }()
		name := path.Base(rel)
		if _, err = parent.Lstat(name); err != nil {
			return err
		}
		return parent.RemoveAll(name)
	})
}

// HomeUpload writes one file of arbitrary bytes, creating missing parent
// directories. An existing file is replaced only with overwrite.
func (m *Manager) HomeUpload(member domain.MemberID, raw string, content []byte, overwrite bool) error {
	return m.inHome(member, raw, false, func(home *os.Root, rel string) error {
		if len(content) > ConfigMaxFileBytes {
			return ErrConfigTooLarge
		}
		info, err := configTargetInfo(home, rel)
		var mode os.FileMode = 0o644
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case info.IsDir():
			return refuse(ErrConfigDenied, "%s is a folder", rel)
		case !info.Mode().IsRegular():
			return ErrConfigDenied
		case !overwrite:
			return refuse(ErrConfigConflict, "%s already exists", rel)
		default:
			mode = info.Mode().Perm()
		}
		if err := ensureDirPathOwned(home, home, path.Dir(rel), true); err != nil {
			return err
		}
		return atomicConfigWrite(home, home, rel, content, mode, "", info)
	})
}

// HomeDownload returns the bytes of one regular file up to ConfigMaxFileBytes.
func (m *Manager) HomeDownload(member domain.MemberID, raw string) ([]byte, error) {
	var out []byte
	err := m.inHome(member, raw, false, func(home *os.Root, rel string) (err error) {
		if homeLocked(rel) {
			return refuseLocked(rel, "read")
		}
		out, err = readConfigBytes(home, rel, ConfigMaxFileBytes)
		if err == nil && len(out) > ConfigMaxFileBytes {
			err = ErrConfigTooLarge
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
