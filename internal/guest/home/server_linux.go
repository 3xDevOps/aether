package home

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"net"
	"os"
	"path"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
)

const (
	// maxHandles bounds the files one VM keeps open on the host.
	maxHandles = 16384
	// maxIO bounds one read or write; the kernel sends at most 1 MiB.
	maxIO = 1 << 20
	// maxMessage bounds every message either side sends; a directory
	// listing past maxListing fails with EOVERFLOW.
	maxMessage = 16 << 20
	maxListing = 12 << 20
)

// Serve answers the service for one VM on l until l is closed. It runs as
// root on the host and trusts nothing the VM sends: every path is resolved
// beneath its share without following a symlink, nothing but regular files
// and directories is opened, a read-only share is never written, device
// nodes are never created, and new files get cfg's owner and no set-ID bits.
func Serve(l net.Listener, cfg Config) error {
	s := newServer(cfg)
	defer s.close()
	g := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessage), grpc.MaxSendMsgSize(maxMessage),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}),
	)
	guestpb.RegisterHomeServer(g, s)
	return g.Serve(l)
}

type server struct {
	guestpb.UnimplementedHomeServer
	shares   []share
	uid, gid int
	started  time.Time

	mu      sync.Mutex
	handles map[uint64]*os.File
}

// share holds its source open from the start, as a bind mount would. A
// source that could not be opened fails every request for it with errno.
type share struct {
	Share
	fd    int
	errno syscall.Errno
}

func newServer(cfg Config) *server {
	s := &server{uid: int(cfg.UID), gid: int(cfg.GID), started: time.Now(), handles: map[uint64]*os.File{}}
	for _, sh := range cfg.Shares {
		fd, err := unix.Open(sh.Source, unix.O_PATH|unix.O_CLOEXEC, 0)
		s.shares = append(s.shares, share{Share: sh, fd: fd, errno: errnoOf(err)})
	}
	return s
}

func (s *server) close() {
	for _, sh := range s.shares {
		if sh.errno == 0 {
			_ = unix.Close(sh.fd)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, f := range s.handles {
		_ = f.Close()
		delete(s.handles, id)
	}
}

func errnoOf(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return unix.EIO
}

// entry is one file a request names: name in the directory dir, or for a
// share's root, which no directory of the share holds, dir -1 and root.
type entry struct {
	share *share
	root  int
	dir   int
	name  string
	owned []int
}

func (e *entry) close() {
	for _, fd := range e.owned {
		_ = unix.Close(fd)
	}
}

func (e *entry) isRoot() bool { return e.dir < 0 }

// resolve opens the directories leading to p's file. The service root,
// which only lists the shares, is never an entry.
func (s *server) resolve(p string) (*entry, syscall.Errno) {
	i, rel, ok := split(p, len(s.shares))
	if !ok {
		return nil, unix.ENOENT
	}
	if i < 0 {
		return nil, unix.EPERM
	}
	sh := &s.shares[i]
	if sh.errno != 0 {
		return nil, sh.errno
	}
	e := &entry{share: sh, root: sh.fd, dir: -1}
	if sh.Subpath != "" {
		fd, err := unix.Openat2(sh.fd, sh.Subpath, &unix.OpenHow{
			Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
		})
		if err != nil {
			return nil, errnoOf(err)
		}
		e.root = fd
		e.owned = append(e.owned, fd)
	}
	if rel == "" {
		return e, 0
	}
	dir, name := path.Split(rel)
	e.dir, e.name = e.root, name
	if dir != "" {
		fd, err := unix.Openat2(e.root, path.Clean(dir), &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if err != nil {
			e.close()
			return nil, errnoOf(err)
		}
		e.dir = fd
		e.owned = append(e.owned, fd)
	}
	return e, 0
}

// object opens the entry's file itself as O_PATH, never through a symlink.
func (e *entry) object() (int, syscall.Errno) {
	if e.isRoot() {
		fd, err := unix.Dup(e.root)
		return fd, errnoOf(err)
	}
	fd, err := unix.Openat(e.dir, e.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	return fd, errnoOf(err)
}

func statFD(fd int) (unix.Statx_t, syscall.Errno) {
	var st unix.Statx_t
	err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &st)
	return st, errnoOf(err)
}

func (e *entry) stat() (unix.Statx_t, syscall.Errno) {
	if e.isRoot() {
		return statFD(e.root)
	}
	var st unix.Statx_t
	err := unix.Statx(e.dir, e.name, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &st)
	return st, errnoOf(err)
}

func procPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

// reopen opens an O_PATH descriptor's file for I/O. Only regular files and
// directories are opened: a device node in a home would reach the host's
// devices, and a FIFO would block.
func reopen(opath int, flags int) (*os.File, syscall.Errno) {
	st, errno := statFD(opath)
	if errno != 0 {
		return nil, errno
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG, unix.S_IFDIR:
	case unix.S_IFLNK:
		return nil, unix.ELOOP
	default:
		return nil, unix.ENXIO
	}
	fd, err := unix.Open(procPath(opath), flags|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errnoOf(err)
	}
	return os.NewFile(uintptr(fd), "home"), 0
}

// nodeID tells host files apart across filesystems, and an inode number
// reused after a delete by its birth time.
func nodeID(st *unix.Statx_t) uint64 {
	var b [40]byte
	binary.LittleEndian.PutUint32(b[0:], st.Dev_major)
	binary.LittleEndian.PutUint32(b[4:], st.Dev_minor)
	binary.LittleEndian.PutUint64(b[8:], st.Ino)
	if st.Mask&unix.STATX_BTIME != 0 {
		binary.LittleEndian.PutUint64(b[16:], uint64(st.Btime.Sec))
		binary.LittleEndian.PutUint32(b[24:], st.Btime.Nsec)
	}
	h := fnv.New64a()
	_, _ = h.Write(b[:])
	return h.Sum64()
}

func nanos(t unix.StatxTimestamp) int64 { return t.Sec*1e9 + int64(t.Nsec) }

func (s *server) attr(st *unix.Statx_t) *guestpb.HomeAttr {
	return &guestpb.HomeAttr{
		Node: nodeID(st), Mode: uint32(st.Mode), Size: st.Size, Blocks: st.Blocks, Nlink: st.Nlink,
		Uid: uint32(s.uid), Gid: uint32(s.gid),
		AtimeNs: nanos(st.Atime), MtimeNs: nanos(st.Mtime), CtimeNs: nanos(st.Ctime),
	}
}

func (s *server) rootAttr() *guestpb.HomeAttr {
	t := s.started.UnixNano()
	return &guestpb.HomeAttr{Node: 1, Mode: unix.S_IFDIR | 0o555, Nlink: 2, AtimeNs: t, MtimeNs: t, CtimeNs: t}
}

func (s *server) entryReply(st unix.Statx_t, errno syscall.Errno) *guestpb.HomeEntryReply {
	if errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}
	}
	return &guestpb.HomeEntryReply{Attr: s.attr(&st)}
}

func (s *server) Lookup(_ context.Context, req *guestpb.HomeNodeRequest) (*guestpb.HomeEntryReply, error) {
	return s.getattr(req.GetPath(), nil), nil
}

func (s *server) Getattr(_ context.Context, req *guestpb.HomeNodeRequest) (*guestpb.HomeEntryReply, error) {
	return s.getattr(req.GetPath(), req.GetHandle()), nil
}

func (s *server) getattr(p string, h *guestpb.HomeHandle) *guestpb.HomeEntryReply {
	if p == "" {
		return &guestpb.HomeEntryReply{Attr: s.rootAttr()}
	}
	if h != nil {
		f, errno := s.handle(p, h)
		if errno != 0 {
			return &guestpb.HomeEntryReply{Errno: int32(errno)}
		}
		var st unix.Statx_t
		errno = withFD(f, func(fd int) syscall.Errno { st, errno = statFD(fd); return errno })
		return s.entryReply(st, errno)
	}
	e, errno := s.resolve(p)
	if errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}
	}
	defer e.close()
	return s.entryReply(e.stat())
}

// withFD runs fn on f's descriptor, which stays open meanwhile even if a
// concurrent Release closes f.
func withFD(f *os.File, fn func(fd int) syscall.Errno) syscall.Errno {
	rc, err := f.SyscallConn()
	if err != nil {
		return errnoOf(err)
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) { errno = fn(int(fd)) }); err != nil {
		if errors.Is(err, os.ErrClosed) {
			return unix.EBADF
		}
		return errnoOf(err)
	}
	return errno
}

func (s *server) writable(e *entry) syscall.Errno {
	if e.share.ReadOnly {
		return unix.EROFS
	}
	return 0
}

func (s *server) Setattr(_ context.Context, req *guestpb.HomeSetattrRequest) (*guestpb.HomeEntryReply, error) {
	if req.GetPath() == "" {
		return &guestpb.HomeEntryReply{Errno: int32(unix.EPERM)}, nil
	}
	e, errno := s.resolve(req.GetPath())
	if errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	defer e.close()
	if errno = s.writable(e); errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	return s.entryReply(e.statAfter(s.setattr(e, req))), nil
}

// statAfter returns e's attributes after an operation that ended in errno.
func (e *entry) statAfter(errno syscall.Errno) (unix.Statx_t, syscall.Errno) {
	if errno != 0 {
		return unix.Statx_t{}, errno
	}
	return e.stat()
}

func (s *server) setattr(e *entry, req *guestpb.HomeSetattrRequest) syscall.Errno {
	obj, errno := e.object()
	if errno != 0 {
		return errno
	}
	defer func() { _ = unix.Close(obj) }()
	st, errno := statFD(obj)
	if errno != 0 {
		return errno
	}
	link := st.Mode&unix.S_IFMT == unix.S_IFLNK
	if req.Size != nil {
		if errno = s.truncate(obj, req); errno != 0 {
			return errno
		}
	}
	if req.Mode != nil && !link {
		if err := unix.Fchmodat(unix.AT_FDCWD, procPath(obj), req.GetMode()&0o1777, 0); err != nil {
			return errnoOf(err)
		}
	}
	if req.AtimeNs == nil && req.MtimeNs == nil && !req.GetAtimeNow() && !req.GetMtimeNow() {
		return 0
	}
	ts := []unix.Timespec{{Nsec: unix.UTIME_OMIT}, {Nsec: unix.UTIME_OMIT}}
	for i, set := range []struct {
		value *int64
		now   bool
	}{{req.AtimeNs, req.GetAtimeNow()}, {req.MtimeNs, req.GetMtimeNow()}} {
		switch {
		case set.now:
			ts[i] = unix.Timespec{Nsec: unix.UTIME_NOW}
		case set.value != nil:
			ts[i] = unix.NsecToTimespec(*set.value)
		}
	}
	if link {
		return errnoOf(unix.UtimesNanoAt(e.dir, e.name, ts, unix.AT_SYMLINK_NOFOLLOW))
	}
	return errnoOf(unix.UtimesNanoAt(unix.AT_FDCWD, procPath(obj), ts, 0))
}

func (s *server) truncate(obj int, req *guestpb.HomeSetattrRequest) syscall.Errno {
	if h := req.GetHandle(); h != nil && h.GetFlags()&unix.O_ACCMODE != unix.O_RDONLY {
		f, errno := s.handle(req.GetPath(), h)
		if errno != 0 {
			return errno
		}
		return withFD(f, func(fd int) syscall.Errno { return errnoOf(unix.Ftruncate(fd, int64(req.GetSize()))) })
	}
	f, errno := reopen(obj, unix.O_WRONLY|unix.O_NONBLOCK)
	if errno != 0 {
		return errno
	}
	defer func() { _ = f.Close() }()
	return errnoOf(f.Truncate(int64(req.GetSize())))
}

func (s *server) Readlink(_ context.Context, req *guestpb.HomeNodeRequest) (*guestpb.HomeReadlinkReply, error) {
	e, errno := s.resolve(req.GetPath())
	if errno != 0 {
		return &guestpb.HomeReadlinkReply{Errno: int32(errno)}, nil
	}
	defer e.close()
	if e.isRoot() {
		return &guestpb.HomeReadlinkReply{Errno: int32(unix.EINVAL)}, nil
	}
	buf := make([]byte, maxPath)
	n, err := unix.Readlinkat(e.dir, e.name, buf)
	if err != nil {
		return &guestpb.HomeReadlinkReply{Errno: int32(errnoOf(err))}, nil
	}
	return &guestpb.HomeReadlinkReply{Target: buf[:n]}, nil
}

func (s *server) Readdir(_ context.Context, req *guestpb.HomeNodeRequest) (*guestpb.HomeReaddirReply, error) {
	if req.GetPath() == "" {
		reply := &guestpb.HomeReaddirReply{}
		for i := range s.shares {
			entry := &guestpb.HomeDirEntry{Name: strconv.Itoa(i)}
			if attr := s.getattr(entry.Name, nil).GetAttr(); attr != nil {
				entry.Mode, entry.Node = attr.GetMode()&unix.S_IFMT, attr.GetNode()
			}
			reply.Entries = append(reply.Entries, entry)
		}
		return reply, nil
	}
	e, errno := s.resolve(req.GetPath())
	if errno != 0 {
		return &guestpb.HomeReaddirReply{Errno: int32(errno)}, nil
	}
	defer e.close()
	obj, errno := e.object()
	if errno != 0 {
		return &guestpb.HomeReaddirReply{Errno: int32(errno)}, nil
	}
	dir, errno := reopen(obj, unix.O_RDONLY|unix.O_DIRECTORY)
	_ = unix.Close(obj)
	if errno != 0 {
		return &guestpb.HomeReaddirReply{Errno: int32(errno)}, nil
	}
	defer func() { _ = dir.Close() }()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return &guestpb.HomeReaddirReply{Errno: int32(errnoOf(err))}, nil
	}
	reply := &guestpb.HomeReaddirReply{}
	size := 0
	errno = withFD(dir, func(fd int) syscall.Errno {
		for _, name := range names {
			var st unix.Statx_t
			if err := unix.Statx(fd, name, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE|unix.STATX_INO|unix.STATX_BTIME, &st); err != nil {
				continue
			}
			if size += len(name) + 32; size > maxListing {
				return unix.EOVERFLOW
			}
			reply.Entries = append(reply.Entries, &guestpb.HomeDirEntry{Name: name, Mode: uint32(st.Mode) & unix.S_IFMT, Node: nodeID(&st)})
		}
		return 0
	})
	if errno != 0 {
		return &guestpb.HomeReaddirReply{Errno: int32(errno)}, nil
	}
	return reply, nil
}

// created gives a new file the configured owner and the mode the guest
// asked for, without set-ID bits, through a descriptor of the file itself,
// so a symlink swapped in after the create is never followed.
func (s *server) created(e *entry, mode uint32) syscall.Errno {
	obj, errno := e.object()
	if errno != 0 {
		return errno
	}
	defer func() { _ = unix.Close(obj) }()
	if err := unix.Fchownat(obj, "", s.uid, s.gid, unix.AT_EMPTY_PATH); err != nil {
		return errnoOf(err)
	}
	st, errno := statFD(obj)
	if errno != 0 || st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return errno
	}
	return errnoOf(unix.Fchmodat(unix.AT_FDCWD, procPath(obj), mode&0o1777, 0))
}

// mutable resolves a path whose directory entry a request changes.
func (s *server) mutable(p string) (*entry, syscall.Errno) {
	e, errno := s.resolve(p)
	if errno != 0 {
		return nil, errno
	}
	if e.isRoot() {
		e.close()
		return nil, unix.EBUSY
	}
	if errno = s.writable(e); errno != 0 {
		e.close()
		return nil, errno
	}
	return e, 0
}

func (s *server) Mknod(_ context.Context, req *guestpb.HomeMknodRequest) (*guestpb.HomeEntryReply, error) {
	e, errno := s.mutable(req.GetPath())
	if errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	defer e.close()
	mode := req.GetMode()
	var err error
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		err = unix.Mkdirat(e.dir, e.name, 0o700)
	case unix.S_IFREG, unix.S_IFIFO, unix.S_IFSOCK:
		err = unix.Mknodat(e.dir, e.name, mode&unix.S_IFMT|0o600, 0)
	default:
		err = unix.EPERM
	}
	if err != nil {
		return &guestpb.HomeEntryReply{Errno: int32(errnoOf(err))}, nil
	}
	return s.entryReply(e.statAfter(s.created(e, mode))), nil
}

func (s *server) Symlink(_ context.Context, req *guestpb.HomeSymlinkRequest) (*guestpb.HomeEntryReply, error) {
	e, errno := s.mutable(req.GetPath())
	if errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	defer e.close()
	target := string(req.GetTarget())
	if target == "" || len(target) >= maxPath {
		return &guestpb.HomeEntryReply{Errno: int32(unix.EINVAL)}, nil
	}
	if err := unix.Symlinkat(target, e.dir, e.name); err != nil {
		return &guestpb.HomeEntryReply{Errno: int32(errnoOf(err))}, nil
	}
	return s.entryReply(e.statAfter(s.created(e, 0))), nil
}

// pair resolves two paths of one share, for a link or a rename.
func (s *server) pair(from, to string) (*entry, *entry, syscall.Errno) {
	i, _, okFrom := split(from, len(s.shares))
	j, _, okTo := split(to, len(s.shares))
	if okFrom && okTo && i != j {
		return nil, nil, unix.EXDEV
	}
	a, errno := s.mutable(from)
	if errno != 0 {
		return nil, nil, errno
	}
	b, errno := s.mutable(to)
	if errno != 0 {
		a.close()
		return nil, nil, errno
	}
	return a, b, 0
}

func (s *server) Link(_ context.Context, req *guestpb.HomeLinkRequest) (*guestpb.HomeEntryReply, error) {
	old, e, errno := s.pair(req.GetPath(), req.GetNewPath())
	if errno != 0 {
		return &guestpb.HomeEntryReply{Errno: int32(errno)}, nil
	}
	defer old.close()
	defer e.close()
	if err := unix.Linkat(old.dir, old.name, e.dir, e.name, 0); err != nil {
		return &guestpb.HomeEntryReply{Errno: int32(errnoOf(err))}, nil
	}
	return s.entryReply(e.stat()), nil
}

func (s *server) Remove(_ context.Context, req *guestpb.HomeRemoveRequest) (*guestpb.HomeErrnoReply, error) {
	e, errno := s.mutable(req.GetPath())
	if errno != 0 {
		return &guestpb.HomeErrnoReply{Errno: int32(errno)}, nil
	}
	defer e.close()
	flags := 0
	if req.GetDir() {
		flags = unix.AT_REMOVEDIR
	}
	return &guestpb.HomeErrnoReply{Errno: int32(errnoOf(unix.Unlinkat(e.dir, e.name, flags)))}, nil
}

func (s *server) Rename(_ context.Context, req *guestpb.HomeRenameRequest) (*guestpb.HomeErrnoReply, error) {
	if req.GetFlags()&^(unix.RENAME_NOREPLACE|unix.RENAME_EXCHANGE) != 0 {
		return &guestpb.HomeErrnoReply{Errno: int32(unix.EINVAL)}, nil
	}
	old, e, errno := s.pair(req.GetPath(), req.GetNewPath())
	if errno != 0 {
		return &guestpb.HomeErrnoReply{Errno: int32(errno)}, nil
	}
	defer old.close()
	defer e.close()
	err := unix.Renameat2(old.dir, old.name, e.dir, e.name, uint(req.GetFlags()))
	return &guestpb.HomeErrnoReply{Errno: int32(errnoOf(err))}, nil
}
