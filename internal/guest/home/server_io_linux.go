package home

import (
	"context"
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
)

// openFlags keeps the open flags that mean the same on the host; O_DIRECT
// and O_NOATIME could fail there for reasons the guest cannot see.
func openFlags(flags uint32) int {
	return int(flags) & (unix.O_ACCMODE | unix.O_APPEND | unix.O_TRUNC | unix.O_EXCL | unix.O_SYNC | unix.O_DSYNC)
}

func writes(flags int) bool {
	return flags&unix.O_ACCMODE != unix.O_RDONLY || flags&(unix.O_TRUNC|unix.O_APPEND) != 0
}

func (s *server) Open(_ context.Context, req *guestpb.HomeOpenRequest) (*guestpb.HomeEntryReply, error) {
	h := req.GetHandle()
	if h == nil || req.GetPath() == "" {
		return &guestpb.HomeEntryReply{Errno: int32(unix.EINVAL)}, nil
	}
	e, errno := s.resolve(req.GetPath())
	if errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	defer e.close()
	flags := openFlags(h.GetFlags())
	if req.GetCreate() || writes(flags) {
		if errno = s.writable(e); errno != 0 {
			return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
		}
	}
	f, errno := s.open(e, flags, req.GetCreate(), req.GetMode())
	if errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	var st unix.Statx_t
	if errno = withFD(f, func(fd int) syscall.Errno { st, errno = statFD(fd); return errno }); errno != 0 {
		_ = f.Close()
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	if errno = s.keep(h.GetId(), &openFile{File: f, share: e.share}); errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	return &guestpb.HomeEntryReply{Attr: s.attr(&st)}, nil
}

// openFile is a handle's file and the share it was opened in, whose
// read-only flag it keeps.
type openFile struct {
	*os.File
	share *share
}

// setattr changes the open file itself, which may have been renamed over
// or removed since it was opened.
func (f *openFile) setattr(req *guestpb.HomeSetattrRequest) (unix.Statx_t, syscall.Errno) {
	if f.share.ReadOnly {
		return unix.Statx_t{}, unix.EROFS
	}
	var st unix.Statx_t
	errno := withFD(f.File, func(fd int) syscall.Errno {
		if req.Size != nil {
			if err := unix.Ftruncate(fd, int64(req.GetSize())); err != nil {
				return errnoOf(err)
			}
		}
		if req.Mode != nil {
			if err := unix.Fchmod(fd, req.GetMode()&0o1777); err != nil {
				return errnoOf(err)
			}
		}
		if ts := times(req); ts != nil {
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, procPath(fd), ts, 0); err != nil {
				return errnoOf(err)
			}
		}
		var errno syscall.Errno
		st, errno = statFD(fd)
		return errno
	})
	return st, errno
}

func (s *server) open(e *entry, flags int, create bool, mode uint32) (*os.File, syscall.Errno) {
	if create && !e.isRoot() {
		fd, err := unix.Openat(e.dir, e.name, flags|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err == nil {
			f := os.NewFile(uintptr(fd), "home")
			if errno := s.created(e, mode); errno != 0 {
				_ = f.Close()
				return nil, errno
			}
			return f, 0
		}
		if !errors.Is(err, unix.EEXIST) || flags&unix.O_EXCL != 0 {
			return nil, errnoOf(err)
		}
	}
	obj, errno := e.object()
	if errno != 0 {
		return nil, errno
	}
	defer func() { _ = unix.Close(obj) }()
	return reopen(obj, flags&^unix.O_EXCL|unix.O_NONBLOCK)
}

// known returns the file h names when the service opened it.
func (s *server) known(h *guestpb.HomeHandle) *openFile {
	if h == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handles[h.GetId()]
}

func (s *server) keep(id uint64, f *openFile) syscall.Errno {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.handles[id]; old != nil {
		_ = old.Close()
	} else if len(s.handles) >= maxHandles {
		_ = f.Close()
		return unix.ENFILE
	}
	s.handles[id] = f
	return 0
}

// handle returns the file h names, opening p again when the service does
// not know h, as after a restore; a file gone since then is stale.
func (s *server) handle(p string, h *guestpb.HomeHandle) (*os.File, syscall.Errno) {
	if f := s.known(h); f != nil {
		return f.File, 0
	}
	e, errno := s.resolve(p)
	if errno != 0 {
		return nil, stale(errno)
	}
	defer e.close()
	flags := openFlags(h.GetFlags()) &^ (unix.O_TRUNC | unix.O_EXCL)
	if writes(flags) {
		if errno = s.writable(e); errno != 0 {
			return nil, errno
		}
	}
	f, errno := s.open(e, flags, false, 0)
	if errno != 0 {
		return nil, stale(errno)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if kept := s.handles[h.GetId()]; kept != nil {
		_ = f.Close()
		return kept.File, 0
	}
	if len(s.handles) >= maxHandles {
		_ = f.Close()
		return nil, unix.ENFILE
	}
	s.handles[h.GetId()] = &openFile{File: f, share: e.share}
	return f, 0
}

func stale(errno syscall.Errno) syscall.Errno {
	if errno == unix.ENOENT {
		return unix.ESTALE
	}
	return errno
}

func (s *server) Read(_ context.Context, req *guestpb.HomeReadRequest) (*guestpb.HomeReadReply, error) {
	f, errno := s.handle(req.GetPath(), req.GetHandle())
	if errno != 0 {
		return &guestpb.HomeReadReply{Errno: int32(errno)}, nil
	}
	buf := make([]byte, min(req.GetSize(), maxIO))
	n := 0
	errno = withFD(f, func(fd int) syscall.Errno {
		var err error
		n, err = unix.Pread(fd, buf, req.GetOffset())
		return errnoOf(err)
	})
	if errno != 0 {
		return &guestpb.HomeReadReply{Errno: int32(errno)}, nil
	}
	return &guestpb.HomeReadReply{Data: buf[:n]}, nil
}

// Write keeps an O_APPEND file appending at the host's end of the file,
// which Linux's pwrite does, so VMs and containers appending to one file
// never overwrite each other.
func (s *server) Write(_ context.Context, req *guestpb.HomeWriteRequest) (*guestpb.HomeWriteReply, error) {
	if len(req.GetData()) > maxIO {
		return &guestpb.HomeWriteReply{Errno: int32(unix.EINVAL)}, nil
	}
	f, errno := s.handle(req.GetPath(), req.GetHandle())
	if errno != 0 {
		return &guestpb.HomeWriteReply{Errno: int32(errno)}, nil
	}
	n := 0
	errno = withFD(f, func(fd int) syscall.Errno {
		var err error
		n, err = unix.Pwrite(fd, req.GetData(), req.GetOffset())
		return errnoOf(err)
	})
	if errno != 0 {
		return &guestpb.HomeWriteReply{Errno: int32(errno)}, nil
	}
	return &guestpb.HomeWriteReply{Written: uint32(n)}, nil
}

func (s *server) Fsync(_ context.Context, req *guestpb.HomeHandleRequest) (*guestpb.HomeErrnoReply, error) {
	f, errno := s.handle(req.GetPath(), req.GetHandle())
	if errno == 0 {
		errno = withFD(f, func(fd int) syscall.Errno {
			if req.GetDatasync() {
				return errnoOf(unix.Fdatasync(fd))
			}
			return errnoOf(unix.Fsync(fd))
		})
	}
	return &guestpb.HomeErrnoReply{Errno: int32(errno)}, nil
}

func (s *server) Release(_ context.Context, req *guestpb.HomeHandleRequest) (*guestpb.HomeErrnoReply, error) {
	s.mu.Lock()
	f := s.handles[req.GetHandle().GetId()]
	delete(s.handles, req.GetHandle().GetId())
	s.mu.Unlock()
	if f != nil {
		_ = f.Close()
	}
	return &guestpb.HomeErrnoReply{}, nil
}

func (s *server) Statfs(_ context.Context, req *guestpb.HomeNodeRequest) (*guestpb.HomeStatfsReply, error) {
	p := req.GetPath()
	if p == "" {
		if len(s.shares) == 0 {
			return &guestpb.HomeStatfsReply{Bsize: 4096, Frsize: 4096, Namelen: 255}, nil
		}
		p = "0"
	}
	e, errno := s.resolve(p)
	if errno != 0 {
		return &guestpb.HomeStatfsReply{Errno: int32(errno)}, nil
	}
	defer e.close()
	var st unix.Statfs_t
	if err := unix.Fstatfs(e.root, &st); err != nil {
		return &guestpb.HomeStatfsReply{Errno: int32(errnoOf(err))}, nil
	}
	return &guestpb.HomeStatfsReply{
		Blocks: st.Blocks, Bfree: st.Bfree, Bavail: st.Bavail, Files: st.Files, Ffree: st.Ffree,
		Bsize: uint32(st.Bsize), Namelen: uint32(st.Namelen), Frsize: uint32(st.Frsize),
	}, nil
}
