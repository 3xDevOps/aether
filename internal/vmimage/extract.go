package vmimage

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	// maxImageBytes and maxImageEntries leave a session at least 16 GiB and
	// half the inodes of its root filesystem.
	maxImageBytes   = rootSize - 16<<30
	maxImageEntries = 2 << 20
	maxID           = 1<<32 - 2
)

// extract writes a flattened image filesystem, as mutate.Extract streams it,
// into the empty directory root. Every path is resolved through root, so no
// entry, symlink or hard link reaches outside it. Device files are dropped
// under /dev, where the guest mounts devtmpfs, and refused elsewhere.
// Ownership needs the caller to be root unless every entry is its own.
func extract(r io.Reader, root *os.Root) error {
	tr := tar.NewReader(r)
	var dirs, links []*tar.Header
	var entries, size int64
	made := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("vmimage: read image filesystem: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if entries++; entries > maxImageEntries {
			return fmt.Errorf("vmimage: image has more than %d files", maxImageEntries)
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "/"))
		if name == "." {
			continue
		}
		hdr.Name = name
		if hdr.Uid < 0 || hdr.Uid > maxID || hdr.Gid < 0 || hdr.Gid > maxID {
			return fmt.Errorf("vmimage: image entry %s has owner %d:%d", name, hdr.Uid, hdr.Gid)
		}
		if err = makeParents(root, path.Dir(name), made); err != nil {
			return fmt.Errorf("vmimage: image entry %s: %w", name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = makeDir(root, name)
			made[name] = true
			dirs = append(dirs, hdr)
		case tar.TypeReg:
			if size += hdr.Size; size > maxImageBytes {
				return fmt.Errorf("vmimage: image is larger than %d GiB", maxImageBytes>>30)
			}
			err = writeFile(root, hdr, tr)
		case tar.TypeSymlink:
			if err = root.Symlink(hdr.Linkname, name); err == nil {
				err = root.Lchown(name, hdr.Uid, hdr.Gid)
			}
		case tar.TypeLink:
			hdr.Linkname = path.Clean(strings.TrimPrefix(hdr.Linkname, "/"))
			links = append(links, hdr)
		case tar.TypeFifo:
			err = makeFifo(root, hdr)
		case tar.TypeChar, tar.TypeBlock:
			if name != "dev" && !strings.HasPrefix(name, "dev/") {
				return fmt.Errorf("vmimage: image entry %s is a device file outside /dev", name)
			}
		default:
			return fmt.Errorf("vmimage: image entry %s has unsupported type %q", name, hdr.Typeflag)
		}
		if err != nil {
			return fmt.Errorf("vmimage: image entry %s: %w", name, err)
		}
	}
	// A flattened image lists upper layers first, so a hard link can come
	// before its target.
	for _, hdr := range links {
		if err := root.Link(hdr.Linkname, hdr.Name); err != nil {
			return fmt.Errorf("vmimage: image entry %s: %w", hdr.Name, err)
		}
	}
	// Directory metadata goes last: writing into a directory changes its
	// times, and a directory may be listed after its contents.
	for _, hdr := range slices.Backward(dirs) {
		if err := setDirMeta(root, hdr); err != nil {
			return fmt.Errorf("vmimage: image entry %s: %w", hdr.Name, err)
		}
	}
	return nil
}

// makeParents creates the directories above an entry that the image does
// not list, as Docker does: owned by root, mode 0755 whatever the umask.
func makeParents(root *os.Root, dir string, made map[string]bool) error {
	var missing []string
	for d := dir; d != "." && !made[d]; d = path.Dir(d) {
		missing = append(missing, d)
	}
	if len(missing) == 0 {
		return nil
	}
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, d := range missing {
		if err := root.Chmod(d, 0o755); err != nil {
			return err
		}
		made[d] = true
	}
	return nil
}

func makeDir(root *os.Root, name string) error {
	err := root.Mkdir(name, 0o700)
	if !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("listed twice, as a directory and as another type")
	}
	return nil
}

func writeFile(root *os.Root, hdr *tar.Header, data io.Reader) error {
	f, err := root.OpenFile(hdr.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, data, hdr.Size); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := setMeta(f, hdr); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Chtimes(hdr.Name, hdr.AccessTime, hdr.ModTime)
}

func makeFifo(root *os.Root, hdr *tar.Header) error {
	dir, err := root.Open(path.Dir(hdr.Name))
	if err != nil {
		return err
	}
	err = unix.Mkfifoat(int(dir.Fd()), path.Base(hdr.Name), 0o600)
	if err = errors.Join(err, dir.Close()); err != nil {
		return err
	}
	f, err := root.OpenFile(hdr.Name, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	return errors.Join(setMeta(f, hdr), f.Close())
}

func setDirMeta(root *os.Root, hdr *tar.Header) error {
	f, err := root.Open(hdr.Name)
	if err != nil {
		return err
	}
	if err := errors.Join(setMeta(f, hdr), f.Close()); err != nil {
		return err
	}
	return root.Chtimes(hdr.Name, hdr.AccessTime, hdr.ModTime)
}

// setMeta applies owner, then mode, then extended attributes: a chown clears
// set-user-ID bits and file capabilities.
func setMeta(f *os.File, hdr *tar.Header) error {
	if err := f.Chown(hdr.Uid, hdr.Gid); err != nil {
		return err
	}
	fd := int(f.Fd())
	if err := unix.Fchmod(fd, uint32(hdr.Mode&0o7777)); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	for key, value := range hdr.PAXRecords {
		attr, ok := strings.CutPrefix(key, "SCHILY.xattr.")
		if !ok {
			continue
		}
		if err := unix.Fsetxattr(fd, attr, []byte(value), 0); err != nil {
			return fmt.Errorf("set extended attribute %s: %w", attr, err)
		}
	}
	return nil
}
