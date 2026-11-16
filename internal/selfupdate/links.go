package selfupdate

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/3xDevOps/Aether/internal/shellquote"
)

// newNames maps each binary to the second name it answers to. The real
// files keep the first name on every host: an updater from before the
// second name writes its files at the first, and would otherwise replace a
// link with a file.
var newNames = []struct{ bin, link string }{
	{"aether", "cloudfield"},
	{"aether-server", "cloudfield-server"},
}

// windowsShim is the text of cloudfield.cmd: a per-user Windows install
// cannot create symlinks, so the second name is a batch file that runs
// target beside it with the same arguments and exits with its status.
func windowsShim(target string) string { return "@\"%~dp0" + target + "\" %*\r\n" }

// Beside returns the file named name in self's directory with its
// symlinks resolved, and false when there is none. An update replaces the
// file at that path, so a link stays a link and its target is what changes.
func Beside(self, name string) (string, bool) {
	path, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(self), name))
	return path, err == nil
}

// Link gives each binary in dir its second name: a relative symlink
// beside it, or cloudfield.cmd on Windows. A name that is already ours is
// left alone, and so is one that holds anything else - another program, a
// regular file, a link to another file or to nothing - which is never
// replaced. The error names every link this user may not create with the
// one command that creates them.
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
			err = Symlink(dir, bin, link)
		}
		switch {
		case err == nil, errors.Is(err, ErrNameTaken):
		case errors.Is(err, fs.ErrPermission) && runtime.GOOS != "windows":
			cmds = append(cmds, lnCommand(dir, bin, link))
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

// secondName is where Link puts the second name of the binary at path,
// empty for a binary that has none.
func secondName(path string) string {
	for _, n := range newNames {
		if filepath.Base(path) == n.bin {
			return filepath.Join(filepath.Dir(path), n.link)
		}
	}
	return ""
}

func lnCommand(dir, target, link string) string {
	cmd := "ln -s " + shellquote.Quote(target) + " " + shellquote.Quote(filepath.Join(dir, link))
	if os.Geteuid() != 0 {
		cmd = "sudo " + cmd
	}
	return cmd
}

// ErrNameTaken is a second name that already holds something other than
// a link to its binary.
var ErrNameTaken = errors.New("the name is taken by something else; left as it is")

// Symlink makes name in dir a relative symlink to target in dir when
// nothing is at name. A link that already resolves to target is kept, and
// anything else at name is ErrNameTaken: it is never replaced.
func Symlink(dir, target, name string) error {
	path := filepath.Join(dir, name)
	err := os.Symlink(target, path)
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return err
	}
	if linksTo(path, filepath.Join(dir, target)) {
		return nil
	}
	return fmt.Errorf("%s: %w", path, ErrNameTaken)
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

// writeShim writes windowsShim(target) at name in dir when nothing is
// there. A file with exactly that text is ours and kept.
func writeShim(dir, target, name string) error {
	path := filepath.Join(dir, name)
	shim := windowsShim(target)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		if got, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(got, []byte(shim)) {
			return nil
		}
		return fmt.Errorf("%s: %w", path, ErrNameTaken)
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(shim)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", path, err), os.Remove(path))
	}
	return nil
}
