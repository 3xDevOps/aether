package vmimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/guest"
)

// A larger agent program than these limits gets no disk and keeps loading
// from the home; programEntries and programDepth also bound the walk at
// each cold boot, and the directories it holds open.
const (
	programLimit   = 2 << 30
	programEntries = 100_000
	programDepth   = 128
)

// ProgramDisk links path to the disk built from unit, the home-relative
// file or directory in home that ~/.local/bin/<executable> runs from, as
// unit is now, and records the use. It reports false, linking nothing, when
// no such disk was built.
func (s *Store) ProgramDisk(home, executable, unit, path string) (Disk, bool, error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return Disk{}, false, fmt.Errorf("vmimage: program %s: %w", unit, err)
	}
	defer func() { _ = root.Close() }()
	sum, _, _, err := programSum(root, unit)
	if err != nil {
		return Disk{}, false, fmt.Errorf("vmimage: program %s in %s: %w", unit, home, err)
	}
	disk := filepath.Join(s.programsDir(), programPool(home, executable), sum+".raw")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = os.Stat(disk); errors.Is(err, fs.ErrNotExist) {
		return Disk{}, false, nil
	}
	if err == nil {
		err = s.pinLocked(disk, path)
	}
	if err == nil {
		now := time.Now()
		err = os.Chtimes(disk, now, now)
	}
	if err != nil {
		return Disk{}, false, fmt.Errorf("vmimage: program %s in %s: %w", unit, home, err)
	}
	return Disk{Path: path, Format: FormatRaw}, true, nil
}

// BuildProgram builds the disk ProgramDisk looks for, unless it exists, and
// makes it the newest of executable's in home. Builds run one at a time at
// the lowest CPU and I/O priority. A program over programLimit, or one that
// changes while it is copied, is not built.
func (s *Store) BuildProgram(home, executable, unit string) error {
	s.programs.Lock()
	defer s.programs.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	built := make(chan error, 1)
	go func() {
		// Never unlocked: the thread ends with this goroutine, and its
		// lowered priority with it. mkfs.ext4 inherits it.
		goruntime.LockOSThread()
		err := unix.Setpriority(unix.PRIO_PROCESS, 0, 19)
		if err == nil {
			const whoThread, classIdle = 1, 3 << 13
			if _, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, whoThread, 0, classIdle); errno != 0 {
				err = errno
			}
		}
		if err == nil {
			err = s.buildProgram(ctx, home, executable, unit)
		}
		built <- err
	}()
	if err := <-built; err != nil {
		return fmt.Errorf("vmimage: build program %s in %s: %w", unit, home, err)
	}
	return nil
}

func (s *Store) buildProgram(ctx context.Context, home, executable, unit string) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	sum, size, entries, err := programSum(root, unit)
	if err != nil {
		return err
	}
	pool := filepath.Join(s.programsDir(), programPool(home, executable))
	name := sum + ".raw"
	if _, err = os.Stat(filepath.Join(pool, name)); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	work, err := os.MkdirTemp(s.tmpDir(), "program-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	stage := filepath.Join(work, "root")
	if err = os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	staged, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	err = copyProgram(ctx, root, unit, staged, guest.ProgramEntry)
	if err = errors.Join(err, staged.Close()); err != nil {
		return err
	}
	after, _, _, err := programSum(root, unit)
	if err != nil || after != sum {
		return errors.Join(errors.New("it changed while it was copied"), err)
	}
	// ext4 rounds every file up to a block and needs an inode per entry.
	disk := filepath.Join(work, "disk.raw")
	bytes := (size*11/10 + int64(entries)*4096 + 16<<20) &^ (1<<20 - 1)
	if err = mkfs(ctx, disk, bytes, "aether-program", stage, "-O", "^has_journal", "-N", strconv.Itoa(entries+64)); err != nil {
		return err
	}
	if err = os.Chmod(disk, 0o444); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = os.MkdirAll(pool, 0o700); err != nil {
		return err
	}
	if err = os.Rename(disk, filepath.Join(pool, name)); err != nil {
		return err
	}
	return linkNewest(pool, name)
}

// walkProgram visits unit and, for a directory, everything in it, without
// following a symlink or leaving root. rel is the path below unit, "" for
// unit itself. It opens only directories, never a FIFO or device put in a
// directory's place, and stops past programLimit, programEntries,
// programDepth or a path no program could open, so every pass over unit,
// including the copy, is bounded.
func walkProgram(root *os.Root, unit string, visit func(rel string, info fs.FileInfo) error) error {
	var size int64
	entries := 0
	var walk func(rel string, info fs.FileInfo) error
	walk = func(rel string, info fs.FileInfo) error {
		if size += info.Size(); size > programLimit {
			return fmt.Errorf("it takes over %d GiB", programLimit>>30)
		}
		if entries++; entries > programEntries {
			return fmt.Errorf("it holds over %d files", programEntries)
		}
		if strings.Count(rel, "/") >= programDepth || len(rel) > unix.PathMax {
			return fmt.Errorf("it nests over %d directories or %d bytes deep", programDepth, unix.PathMax)
		}
		if err := visit(rel, info); err != nil || !info.IsDir() {
			return err
		}
		dir, err := root.OpenFile(path.Join(unit, rel), os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer func() { _ = dir.Close() }()
		for {
			batch, err := dir.ReadDir(1024)
			for _, e := range batch {
				child, infoErr := e.Info()
				if infoErr == nil {
					infoErr = walk(path.Join(rel, e.Name()), child)
				}
				if infoErr != nil {
					return infoErr
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	info, err := root.Lstat(unit)
	if err != nil {
		return err
	}
	return walk("", info)
}

// programSum identifies unit as it is now: every entry's name, type,
// permissions, size, times and inode, and each symlink's target. It also
// returns the bytes and number of entries it holds.
func programSum(root *os.Root, unit string) (string, int64, int, error) {
	h := sha256.New()
	var size int64
	entries := 0
	err := walkProgram(root, unit, func(rel string, info fs.FileInfo) error {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("no inode for %s", rel)
		}
		var target string
		if info.Mode()&fs.ModeSymlink != 0 {
			var err error
			if target, err = root.Readlink(path.Join(unit, rel)); err != nil {
				return err
			}
		}
		size += info.Size()
		entries++
		_, err := fmt.Fprintf(h, "%q %v %d %d.%d %d.%d %d %q\n", rel, info.Mode(), info.Size(),
			st.Mtim.Sec, st.Mtim.Nsec, st.Ctim.Sec, st.Ctim.Nsec, st.Ino, target)
		return err
	})
	return hex.EncodeToString(h.Sum(nil)[:16]), size, entries, err
}

// copyProgram copies unit to dst, inside stage: directories, regular files
// and symlinks, each readable by every user and without set-ID bits, as the
// disk's files belong to root. Writing through stage keeps a symlink the
// copy made from leading a later write out of it.
func copyProgram(ctx context.Context, root *os.Root, unit string, stage *os.Root, dst string) error {
	return walkProgram(root, unit, func(rel string, info fs.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		to := path.Join(dst, rel)
		mode := info.Mode().Perm() | 0o444
		if mode&0o111 != 0 {
			mode |= 0o111
		}
		switch {
		case info.IsDir():
			if err := stage.Mkdir(to, 0o700); err != nil {
				return err
			}
			return stage.Chmod(to, mode|0o111)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := root.Readlink(path.Join(unit, rel))
			if err != nil {
				return err
			}
			return stage.Symlink(target, to)
		case info.Mode().IsRegular():
			return copyRegular(root, path.Join(unit, rel), info, stage, to, mode)
		}
		return nil
	})
}

// copyRegular copies the regular file walked as walked, and no more of it
// than the walk saw. It opens the file only once it holds that very file:
// whatever replaced it since could be a FIFO, whose open blocks, or a
// device, whose open alone can act. A leased file fails rather than waits.
func copyRegular(root *os.Root, name string, walked fs.FileInfo, stage *os.Root, to string, mode fs.FileMode) error {
	held, err := root.OpenFile(name, unix.O_PATH|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = held.Close() }()
	info, err := held.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, walked) || !info.Mode().IsRegular() {
		return fmt.Errorf("%s changed while it was copied", name)
	}
	in, err := os.OpenFile("/proc/self/fd/"+strconv.Itoa(int(held.Fd())), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := stage.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.CopyN(out, in, walked.Size())
	if err == nil {
		err = out.Chmod(mode)
	}
	return errors.Join(err, out.Close())
}

// programPool names the disks of one executable in one home.
func programPool(home, executable string) string {
	sum := sha256.Sum256([]byte(home + "\x00" + executable))
	return hex.EncodeToString(sum[:8])
}

func (s *Store) programsDir() string { return filepath.Join(s.dir, "programs") }
