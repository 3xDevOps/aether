package selfupdate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/shellquote"
)

// newNames maps each binary to the second name it answers to. The real
// files keep the first name on every host: an updater from before the
// second name writes its files at the first, and would otherwise replace a
// link with a file.
var newNames = []struct{ bin, link string }{
	{"aether", "cloudfield"},
	{"aether-server", "cloudfield-server"},
}

// Beside returns the file named name in self's directory with its
// symlinks resolved, and false when there is none. An update replaces the
// file at that path, so a link stays a link and its target is what changes.
func Beside(self, name string) (string, bool) {
	path, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(self), name))
	return path, err == nil
}

// Link gives each binary in dir its second name: a relative symlink
// beside it, or on Windows, where a per-user install cannot create
// symlinks, a cloudfield.cmd that runs aether.exe beside it. A name that
// is already ours is kept, and one that holds anything else - another
// program, a regular file, a link to another file or to nothing - is left
// as it is and is no error. The error names every link this user may not
// create with the one command that creates them.
func Link(dir string) error {
	var cmds []string
	var errs []error
	for _, n := range newNames {
		bin, link := n.bin, n.link
		if runtime.GOOS == "windows" {
			bin, link = bin+".exe", link+".cmd"
		}
		if _, err := os.Stat(filepath.Join(dir, bin)); err != nil {
			continue
		}
		var err error
		if runtime.GOOS == "windows" {
			err = writeShim(dir, bin, link)
		} else {
			_, err = Symlink(dir, bin, link)
		}
		switch {
		case err == nil, errors.Is(err, ErrNameTaken):
		case errors.Is(err, fs.ErrPermission) && runtime.GOOS != "windows":
			cmd := "ln -s " + shellquote.Quote(bin) + " " + shellquote.Quote(filepath.Join(dir, link))
			if os.Geteuid() != 0 {
				cmd = "sudo " + cmd
			}
			cmds = append(cmds, cmd)
			errs = append(errs, err)
		default:
			errs = append(errs, err)
		}
	}
	err := errors.Join(errs...)
	if len(cmds) > 0 {
		return fmt.Errorf("%w\nrun: %s", err, strings.Join(cmds, " && "))
	}
	return err
}

// LinkAll runs Link in each directory that holds one of paths, the
// binaries an update just replaced.
func LinkAll(paths []string) error {
	var dirs []string
	var errs []error
	for _, p := range paths {
		if d := filepath.Dir(p); !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
			errs = append(errs, Link(d))
		}
	}
	return errors.Join(errs...)
}

// ErrNameTaken is a second name that already holds something other than
// a link to its binary.
var ErrNameTaken = errors.New("the name is taken by something else; left as it is")

// Symlink makes name in dir a relative symlink to target in dir when
// nothing is at name, and reports whether it did. A link that already
// resolves to target is kept, and anything else at name is ErrNameTaken:
// it is never replaced.
func Symlink(dir, target, name string) (bool, error) {
	path := filepath.Join(dir, name)
	err := os.Symlink(target, path)
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return err == nil, err
	}
	if linksTo(path, filepath.Join(dir, target)) {
		return false, nil
	}
	return false, fmt.Errorf("%s: %w", path, ErrNameTaken)
}

func linksTo(path, target string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	got, err := os.Stat(path)
	if err != nil {
		return false
	}
	want, err := os.Stat(target)
	return err == nil && os.SameFile(got, want)
}

// writeShim puts a batch file at name in dir that runs target beside it
// with the same arguments and exits with its status, when nothing is
// there. The file is written beside it and hard-linked into place, which
// never replaces anything and never leaves a partial file at name. A file
// with exactly that text is ours and kept; anything else, even an empty
// file, is ErrNameTaken.
func writeShim(dir, target, name string) error {
	path := filepath.Join(dir, name)
	shim := []byte("@\"%~dp0" + target + "\" %*\r\n")
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
		if got, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(got, shim) {
			return nil
		}
		return fmt.Errorf("%s: %w", path, ErrNameTaken)
	case err == nil:
		return fmt.Errorf("%s: %w", path, ErrNameTaken)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(shim)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Link(tmp.Name(), path)
		if errors.Is(err, fs.ErrExist) {
			err = fmt.Errorf("%s: %w", path, ErrNameTaken)
		}
	}
	if rerr := os.Remove(tmp.Name()); err == nil {
		err = rerr
	}
	return err
}
