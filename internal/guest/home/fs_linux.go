package home

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
)

const (
	// cacheTTL is how long the guest trusts what it learned of a name or a
	// file, so another VM's change shows within it.
	cacheTTL = time.Second
	// callTimeout bounds one operation, including waiting for the service
	// to come back after a restore.
	callTimeout = 2 * time.Minute
	// retries covers a connection cut under a call, as a save does.
	retries = 3
)

// Mount mounts the service at dir and serves it from this process until the
// VM powers off. dial connects to the service; it runs again whenever the
// connection drops, as it does across a save and restore.
func Mount(dir string, dial func(context.Context) (net.Conn, error)) error {
	conn, err := grpc.NewClient("passthrough:///home",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dial(ctx) }),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 20 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 500 * time.Millisecond},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 5 * time.Second, PermitWithoutStream: true}),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true), grpc.MaxCallRecvMsgSize(maxMessage), grpc.MaxCallSendMsgSize(maxRequest)),
	)
	if err != nil {
		return err
	}
	ttl := cacheTTL
	root := &node{fsys: &filesystem{api: guestpb.NewHomeClient(conn)}, share: -1}
	_, err = fs.Mount(dir, root, &fs.Options{
		EntryTimeout: &ttl, AttrTimeout: &ttl, NegativeTimeout: &ttl,
		MountOptions: fuse.MountOptions{
			FsName: "aether-home", Name: "aether", AllowOther: true,
			DirectMount: true, DirectMountStrict: true,
			Options:  []string{"default_permissions"},
			MaxWrite: maxIO, MaxBackground: 64, DisableReadDirPlus: true,
			// Requests in flight live in PID 1, outside the session's
			// memory cap; the kernel queues the rest.
			MaxInflightRequestBytes: 8 << 20,
		},
	})
	if err != nil {
		_ = conn.Close()
	}
	return err
}

type filesystem struct {
	api     guestpb.HomeClient
	handles atomic.Uint64
}

type reply interface{ GetErrno() int32 }

// call runs one operation, again after a connection that dropped under it.
// An operation the service ran before the cut may run twice, as on NFS.
func call[R reply](ctx context.Context, op func(context.Context) (R, error)) (R, syscall.Errno) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		r, err := op(ctx)
		if err == nil {
			return r, syscall.Errno(r.GetErrno())
		}
		if status.Code(err) == codes.Unavailable && attempt < retries && ctx.Err() == nil {
			continue
		}
		if ctx.Err() != nil && status.Code(err) == codes.Canceled {
			return r, syscall.EINTR
		}
		slog.Warn("guest: home file service", "error", err)
		return r, syscall.EIO
	}
}

// node is one file of the service, named by its path from the root; its
// share is -1 for the root.
type node struct {
	fs.Inode
	fsys  *filesystem
	share int
}

var (
	_ fs.NodeLookuper   = (*node)(nil)
	_ fs.NodeGetattrer  = (*node)(nil)
	_ fs.NodeSetattrer  = (*node)(nil)
	_ fs.NodeReadlinker = (*node)(nil)
	_ fs.NodeReaddirer  = (*node)(nil)
	_ fs.NodeMkdirer    = (*node)(nil)
	_ fs.NodeMknoder    = (*node)(nil)
	_ fs.NodeSymlinker  = (*node)(nil)
	_ fs.NodeLinker     = (*node)(nil)
	_ fs.NodeUnlinker   = (*node)(nil)
	_ fs.NodeRmdirer    = (*node)(nil)
	_ fs.NodeRenamer    = (*node)(nil)
	_ fs.NodeCreater    = (*node)(nil)
	_ fs.NodeOpener     = (*node)(nil)
	_ fs.NodeStatfser   = (*node)(nil)
)

// path names n from the root. A node whose name was removed gets a path no
// share has, so it never names another file.
func (n *node) path() string { return n.Path(n.Root()) }

func (n *node) child(name string) string {
	if p := n.path(); p != "" {
		return p + "/" + name
	}
	return name
}

func (n *node) childShare(name string) int {
	if n.share >= 0 {
		return n.share
	}
	i, err := strconv.Atoi(name)
	if err != nil {
		return -1
	}
	return i
}

// ino keeps the same host file apart in two shares: each share has its own
// read-only flag.
func ino(share int, node uint64) uint64 {
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:8], uint64(share))
	binary.LittleEndian.PutUint64(b[8:], node)
	h := fnv.New64a()
	_, _ = h.Write(b[:])
	if v := h.Sum64(); v > fuse.FUSE_ROOT_ID && v != ^uint64(0) {
		return v
	}
	return fuse.FUSE_ROOT_ID + 1
}

func fill(out *fuse.Attr, a *guestpb.HomeAttr, inode uint64) {
	out.Ino, out.Size, out.Blocks, out.Mode, out.Nlink = inode, a.GetSize(), a.GetBlocks(), a.GetMode(), a.GetNlink()
	out.Owner = fuse.Owner{Uid: a.GetUid(), Gid: a.GetGid()}
	out.Blksize = 4096
	atime, mtime, ctime := time.Unix(0, a.GetAtimeNs()), time.Unix(0, a.GetMtimeNs()), time.Unix(0, a.GetCtimeNs())
	out.SetTimes(&atime, &mtime, &ctime)
}

func (n *node) inode(share int, a *guestpb.HomeAttr) uint64 {
	if share < 0 {
		return fuse.FUSE_ROOT_ID
	}
	return ino(share, a.GetNode())
}

func (n *node) newChild(ctx context.Context, name string, a *guestpb.HomeAttr, out *fuse.EntryOut) *fs.Inode {
	share := n.childShare(name)
	id := n.inode(share, a)
	fill(&out.Attr, a, id)
	return n.NewInode(ctx, &node{fsys: n.fsys, share: share}, fs.StableAttr{Mode: a.GetMode() & syscall.S_IFMT, Ino: id})
}

func (n *node) entry(ctx context.Context, name string, out *fuse.EntryOut, op func(context.Context) (*guestpb.HomeEntryReply, error)) (*fs.Inode, syscall.Errno) {
	r, errno := call(ctx, op)
	if errno != 0 {
		return nil, errno
	}
	return n.newChild(ctx, name, r.GetAttr(), out), 0
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	req := &guestpb.HomeNodeRequest{Path: n.child(name)}
	return n.entry(ctx, name, out, func(ctx context.Context) (*guestpb.HomeEntryReply, error) { return n.fsys.api.Lookup(ctx, req) })
}

func (n *node) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	req := &guestpb.HomeNodeRequest{Path: n.path(), Handle: handleOf(f)}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeEntryReply, error) { return n.fsys.api.Getattr(ctx, req) })
	if errno == 0 {
		fill(&out.Attr, r.GetAttr(), n.inode(n.share, r.GetAttr()))
	}
	return errno
}

// Setattr ignores owner changes: every file belongs to the session user.
func (n *node) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	req := &guestpb.HomeSetattrRequest{Path: n.path(), Handle: handleOf(f)}
	if mode, ok := in.GetMode(); ok {
		req.Mode = &mode
	}
	if size, ok := in.GetSize(); ok {
		req.Size = &size
	}
	req.AtimeNow, req.MtimeNow = in.Valid&fuse.FATTR_ATIME_NOW != 0, in.Valid&fuse.FATTR_MTIME_NOW != 0
	if in.Valid&(fuse.FATTR_ATIME|fuse.FATTR_ATIME_NOW) == fuse.FATTR_ATIME {
		ns := int64(in.Atime)*1e9 + int64(in.Atimensec)
		req.AtimeNs = &ns
	}
	if in.Valid&(fuse.FATTR_MTIME|fuse.FATTR_MTIME_NOW) == fuse.FATTR_MTIME {
		ns := int64(in.Mtime)*1e9 + int64(in.Mtimensec)
		req.MtimeNs = &ns
	}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeEntryReply, error) { return n.fsys.api.Setattr(ctx, req) })
	if errno == 0 {
		fill(&out.Attr, r.GetAttr(), n.inode(n.share, r.GetAttr()))
	}
	return errno
}

func (n *node) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	req := &guestpb.HomeNodeRequest{Path: n.path()}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeReadlinkReply, error) { return n.fsys.api.Readlink(ctx, req) })
	return r.GetTarget(), errno
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	req := &guestpb.HomeNodeRequest{Path: n.path()}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeReaddirReply, error) { return n.fsys.api.Readdir(ctx, req) })
	if errno != 0 {
		return nil, errno
	}
	entries := make([]fuse.DirEntry, 0, len(r.GetEntries()))
	for _, e := range r.GetEntries() {
		entries = append(entries, fuse.DirEntry{Name: e.GetName(), Mode: e.GetMode(), Ino: ino(n.childShare(e.GetName()), e.GetNode())})
	}
	return fs.NewListDirStream(entries), 0
}

func (n *node) mknod(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	req := &guestpb.HomeMknodRequest{Path: n.child(name), Mode: mode}
	return n.entry(ctx, name, out, func(ctx context.Context) (*guestpb.HomeEntryReply, error) { return n.fsys.api.Mknod(ctx, req) })
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return n.mknod(ctx, name, mode|syscall.S_IFDIR, out)
}

func (n *node) Mknod(ctx context.Context, name string, mode, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return n.mknod(ctx, name, mode, out)
}

func (n *node) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	req := &guestpb.HomeSymlinkRequest{Path: n.child(name), Target: []byte(target)}
	return n.entry(ctx, name, out, func(ctx context.Context) (*guestpb.HomeEntryReply, error) { return n.fsys.api.Symlink(ctx, req) })
}

func (n *node) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	req := &guestpb.HomeLinkRequest{Path: target.EmbeddedInode().Path(n.Root()), NewPath: n.child(name)}
	return n.entry(ctx, name, out, func(ctx context.Context) (*guestpb.HomeEntryReply, error) { return n.fsys.api.Link(ctx, req) })
}

func (n *node) remove(ctx context.Context, name string, dir bool) syscall.Errno {
	req := &guestpb.HomeRemoveRequest{Path: n.child(name), Dir: dir}
	_, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeErrnoReply, error) { return n.fsys.api.Remove(ctx, req) })
	return errno
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.remove(ctx, name, false)
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.remove(ctx, name, true)
}

func (n *node) Rename(ctx context.Context, name string, parent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	newPath := newName
	if p := parent.EmbeddedInode().Path(n.Root()); p != "" {
		newPath = p + "/" + newName
	}
	req := &guestpb.HomeRenameRequest{Path: n.child(name), NewPath: newPath, Flags: flags}
	_, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeErrnoReply, error) { return n.fsys.api.Rename(ctx, req) })
	return errno
}

func (n *node) Create(ctx context.Context, name string, flags, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	h := &guestpb.HomeHandle{Id: n.fsys.handles.Add(1), Flags: flags}
	req := &guestpb.HomeOpenRequest{Path: n.child(name), Handle: h, Create: true, Mode: mode}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeEntryReply, error) { return n.fsys.api.Open(ctx, req) })
	if errno != 0 {
		return nil, nil, 0, errno
	}
	h.Node = r.GetAttr().GetNode()
	child := n.newChild(ctx, name, r.GetAttr(), out)
	return child, &file{handle: h, inode: child, fsys: n.fsys}, fuse.FOPEN_KEEP_CACHE, 0
}

// Open keeps the guest's cached pages of the file: the kernel drops them
// once a lookup or getattr shows another size or modification time.
func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	h := &guestpb.HomeHandle{Id: n.fsys.handles.Add(1), Flags: flags}
	req := &guestpb.HomeOpenRequest{Path: n.path(), Handle: h}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeEntryReply, error) { return n.fsys.api.Open(ctx, req) })
	if errno != 0 {
		return nil, 0, errno
	}
	h.Node = r.GetAttr().GetNode()
	return &file{handle: h, inode: n.EmbeddedInode(), fsys: n.fsys}, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	req := &guestpb.HomeNodeRequest{Path: n.path()}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeStatfsReply, error) { return n.fsys.api.Statfs(ctx, req) })
	if errno == 0 {
		*out = fuse.StatfsOut{
			Blocks: r.GetBlocks(), Bfree: r.GetBfree(), Bavail: r.GetBavail(), Files: r.GetFiles(), Ffree: r.GetFfree(),
			Bsize: r.GetBsize(), NameLen: r.GetNamelen(), Frsize: r.GetFrsize(),
		}
	}
	return errno
}

// file is an open file; its path is looked up at each call, so a rename
// while it is open is followed.
type file struct {
	handle *guestpb.HomeHandle
	inode  *fs.Inode
	fsys   *filesystem
}

var (
	_ fs.FileReader   = (*file)(nil)
	_ fs.FileWriter   = (*file)(nil)
	_ fs.FileFsyncer  = (*file)(nil)
	_ fs.FileFlusher  = (*file)(nil)
	_ fs.FileReleaser = (*file)(nil)
)

func handleOf(f fs.FileHandle) *guestpb.HomeHandle {
	if fh, ok := f.(*file); ok {
		return fh.handle
	}
	return nil
}

func (f *file) path() string { return f.inode.Path(f.inode.Root()) }

func (f *file) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	req := &guestpb.HomeReadRequest{Path: f.path(), Handle: f.handle, Offset: off, Size: uint32(len(dest))}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeReadReply, error) { return f.fsys.api.Read(ctx, req) })
	if errno != 0 {
		return nil, errno
	}
	return fuse.ReadResultData(r.GetData()), 0
}

func (f *file) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	req := &guestpb.HomeWriteRequest{Path: f.path(), Handle: f.handle, Offset: off, Data: data}
	r, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeWriteReply, error) { return f.fsys.api.Write(ctx, req) })
	return r.GetWritten(), errno
}

func (f *file) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	req := &guestpb.HomeHandleRequest{Path: f.path(), Handle: f.handle, Datasync: flags&1 != 0}
	_, errno := call(ctx, func(ctx context.Context) (*guestpb.HomeErrnoReply, error) { return f.fsys.api.Fsync(ctx, req) })
	return errno
}

// Flush has nothing to send: every write already reached the host.
func (f *file) Flush(context.Context) syscall.Errno { return 0 }

func (f *file) Release(ctx context.Context) syscall.Errno {
	req := &guestpb.HomeHandleRequest{Handle: f.handle}
	_, errno := call(context.WithoutCancel(ctx), func(ctx context.Context) (*guestpb.HomeErrnoReply, error) { return f.fsys.api.Release(ctx, req) })
	return errno
}
