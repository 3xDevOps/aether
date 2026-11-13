// Command fusevsock prices a guest FUSE filesystem that forwards by path to
// the host over vsock, the design chosen for the member home after
// virtio-fs failed save and restore. Read-only: lookup, getattr, readdir,
// read.
//
//	fusevsock serve <root> <unix-socket> [tcp-addr]   host side
//	fusevsock mount <mountpoint> <port>               guest, over vsock to CID 2
//	fusevsock loopback <mountpoint> <dir>             guest, local passthrough FUSE
//	fusevsock rtt vsock:<port>|tcp:<addr> <n> <bytes> guest, raw round trips
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

const (
	opEcho byte = iota
	opStat
	opReaddir
	opRead
)

// A request is op, path, offset and size; a reply is errno and a payload.
func writeRequest(w *bufio.Writer, op byte, path string, off uint64, size uint32) error {
	var h [15]byte
	h[0] = op
	binary.LittleEndian.PutUint16(h[1:], uint16(len(path)))
	binary.LittleEndian.PutUint64(h[3:], off)
	binary.LittleEndian.PutUint32(h[11:], size)
	w.Write(h[:])
	w.WriteString(path)
	return w.Flush()
}

func readReply(r *bufio.Reader) (syscall.Errno, []byte, error) {
	var h [8]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	data := make([]byte, binary.LittleEndian.Uint32(h[4:]))
	_, err := io.ReadFull(r, data)
	return syscall.Errno(binary.LittleEndian.Uint32(h[:])), data, err
}

func serve(root string, conn net.Conn) {
	defer conn.Close()
	r, w := bufio.NewReaderSize(conn, 1<<17), bufio.NewWriterSize(conn, 1<<17)
	for {
		var h [15]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return
		}
		path := make([]byte, binary.LittleEndian.Uint16(h[1:]))
		if _, err := io.ReadFull(r, path); err != nil {
			return
		}
		off, size := binary.LittleEndian.Uint64(h[3:]), binary.LittleEndian.Uint32(h[11:])
		full := filepath.Join(root, filepath.Clean("/"+string(path)))
		var errno syscall.Errno
		var data []byte
		switch h[0] {
		case opEcho:
			data = make([]byte, size)
		case opStat:
			var st syscall.Stat_t
			if err := syscall.Lstat(full, &st); err != nil {
				errno = err.(syscall.Errno)
			} else {
				data = binary.LittleEndian.AppendUint32(nil, st.Mode)
				for _, v := range []uint64{uint64(st.Size), st.Ino, st.Nlink, uint64(st.Mtim.Sec), uint64(st.Mtim.Nsec)} {
					data = binary.LittleEndian.AppendUint64(data, v)
				}
			}
		case opReaddir:
			entries, err := os.ReadDir(full)
			if err != nil {
				errno = syscall.ENOENT
			}
			for _, e := range entries {
				mode := uint32(syscall.S_IFREG)
				switch {
				case e.IsDir():
					mode = syscall.S_IFDIR
				case e.Type()&os.ModeSymlink != 0:
					mode = syscall.S_IFLNK
				}
				data = binary.LittleEndian.AppendUint32(data, mode)
				data = append(append(data, e.Name()...), 0)
			}
		case opRead:
			f, err := os.Open(full)
			if err != nil {
				errno = syscall.ENOENT
				break
			}
			data = make([]byte, size)
			n, _ := f.ReadAt(data, int64(off))
			data = data[:n]
			f.Close()
		}
		var rh [8]byte
		binary.LittleEndian.PutUint32(rh[:], uint32(errno))
		binary.LittleEndian.PutUint32(rh[4:], uint32(len(data)))
		w.Write(rh[:])
		w.Write(data)
		if w.Flush() != nil {
			return
		}
	}
}

type client struct{ conns chan *conn }

type conn struct {
	r *bufio.Reader
	w *bufio.Writer
}

// dialVsock returns the socket as a file: net.FileConn rejects AF_VSOCK.
func dialVsock(port uint32) (io.ReadWriter, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "vsock"), nil
}

func dial(target string) (io.ReadWriter, error) {
	kind, addr, _ := strings.Cut(target, ":")
	if kind == "tcp" {
		return net.Dial("tcp", addr)
	}
	port, err := strconv.ParseUint(addr, 10, 32)
	if err != nil {
		return nil, err
	}
	return dialVsock(uint32(port))
}

func newClient(target string, n int) (*client, error) {
	c := &client{conns: make(chan *conn, n)}
	for range n {
		nc, err := dial(target)
		if err != nil {
			return nil, err
		}
		c.conns <- &conn{bufio.NewReaderSize(nc, 1<<17), bufio.NewWriterSize(nc, 1<<17)}
	}
	return c, nil
}

func (c *client) call(op byte, path string, off uint64, size uint32) (syscall.Errno, []byte) {
	cn := <-c.conns
	defer func() { c.conns <- cn }()
	if err := writeRequest(cn.w, op, path, off, size); err != nil {
		return syscall.EIO, nil
	}
	errno, data, err := readReply(cn.r)
	if err != nil {
		return syscall.EIO, nil
	}
	return errno, data
}

type node struct {
	fs.Inode
	c    *client
	path string
}

var (
	_ fs.NodeLookuper  = (*node)(nil)
	_ fs.NodeGetattrer = (*node)(nil)
	_ fs.NodeReaddirer = (*node)(nil)
	_ fs.NodeOpener    = (*node)(nil)
	_ fs.NodeReader    = (*node)(nil)
)

func (n *node) stat(path string, out *fuse.Attr) syscall.Errno {
	errno, d := n.c.call(opStat, path, 0, 0)
	if errno != 0 {
		return errno
	}
	out.Mode = binary.LittleEndian.Uint32(d)
	out.Size = binary.LittleEndian.Uint64(d[4:])
	out.Ino = binary.LittleEndian.Uint64(d[12:])
	out.Nlink = uint32(binary.LittleEndian.Uint64(d[20:]))
	out.Mtime = binary.LittleEndian.Uint64(d[28:])
	out.Mtimensec = uint32(binary.LittleEndian.Uint64(d[36:]))
	out.Ctime, out.Ctimensec = out.Mtime, out.Mtimensec
	return 0
}

func (n *node) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	return n.stat(n.path, &out.Attr)
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := filepath.Join(n.path, name)
	if errno := n.stat(p, &out.Attr); errno != 0 {
		return nil, errno
	}
	child := &node{c: n.c, path: p}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: out.Attr.Mode & syscall.S_IFMT, Ino: out.Attr.Ino}), 0
}

func (n *node) Readdir(context.Context) (fs.DirStream, syscall.Errno) {
	errno, d := n.c.call(opReaddir, n.path, 0, 0)
	if errno != 0 {
		return nil, errno
	}
	var entries []fuse.DirEntry
	for len(d) > 4 {
		mode := binary.LittleEndian.Uint32(d)
		end := 4
		for d[end] != 0 {
			end++
		}
		entries = append(entries, fuse.DirEntry{Name: string(d[4:end]), Mode: mode})
		d = d[end+1:]
	}
	return fs.NewListDirStream(entries), 0
}

func (n *node) Open(context.Context, uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *node) Read(_ context.Context, _ fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	errno, d := n.c.call(opRead, n.path, uint64(off), uint32(len(dest)))
	if errno != 0 {
		return nil, errno
	}
	return fuse.ReadResultData(d), 0
}

func mount(dir string, root fs.InodeEmbedder) {
	sec := time.Second
	server, err := fs.Mount(dir, root, &fs.Options{
		EntryTimeout: &sec, AttrTimeout: &sec,
		MountOptions: fuse.MountOptions{FsName: "fusevsock", MaxBackground: 64, DirectMount: true},
	})
	if err != nil {
		log.Fatal(err)
	}
	server.Wait()
}

func rtt(target string, n, size int) {
	c, err := newClient(target, 1)
	if err != nil {
		log.Fatal(err)
	}
	took := make([]time.Duration, n)
	for i := range took {
		s := time.Now()
		if errno, _ := c.call(opEcho, "", 0, uint32(size)); errno != 0 {
			log.Fatal(errno)
		}
		took[i] = time.Since(s)
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	fmt.Printf("%s %d round trips, %d-byte reply: p50 %s p99 %s max %s\n", target, n, size, took[n/2], took[n*99/100], took[n-1])
}

func main() {
	log.SetFlags(0)
	args := os.Args[1:]
	switch {
	case len(args) >= 3 && args[0] == "serve":
		os.Remove(args[2])
		listeners := []string{"unix:" + args[2]}
		if len(args) > 3 {
			listeners = append(listeners, "tcp:"+args[3])
		}
		for _, l := range listeners {
			kind, addr, _ := strings.Cut(l, ":")
			ln, err := net.Listen(kind, addr)
			if err != nil {
				log.Fatal(err)
			}
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						log.Fatal(err)
					}
					go serve(args[1], c)
				}
			}()
		}
		select {}
	case len(args) == 3 && args[0] == "mount":
		c, err := newClient("vsock:"+args[2], 16)
		if err != nil {
			log.Fatal(err)
		}
		mount(args[1], &node{c: c, path: "/"})
	case len(args) == 3 && args[0] == "loopback":
		root, err := fs.NewLoopbackRoot(args[2])
		if err != nil {
			log.Fatal(err)
		}
		mount(args[1], root)
	case len(args) == 4 && args[0] == "rtt":
		n, _ := strconv.Atoi(args[2])
		size, _ := strconv.Atoi(args[3])
		rtt(args[1], n, size)
	default:
		log.Fatal("usage: fusevsock serve|mount|loopback|rtt ...")
	}
}
