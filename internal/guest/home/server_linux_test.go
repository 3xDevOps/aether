package home

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
)

// fixture is a member home beside a file outside it, served as share 0
// writable, 1 the login file beneath it, 2 read-only, and 3 a subpath whose
// symlink leaves its base.
func fixture(t *testing.T) (dir string, s *server) {
	t.Helper()
	dir = t.TempDir()
	h := filepath.Join(dir, "home")
	for _, d := range []string{h, filepath.Join(h, "dir"), filepath.Join(dir, "outside")} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"home/a.txt": "hello", "home/dir/login.json": "token-1", "outside/secret": "secret"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"home/link": filepath.Join(dir, "outside"), "home/escape": "../outside"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(h, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	s = newServer(Config{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Shares: []Share{
		{Source: h}, {Source: h, Subpath: "dir/login.json"}, {Source: h, ReadOnly: true}, {Source: h, Subpath: "escape/secret"},
	}})
	t.Cleanup(s.close)
	return dir, s
}

func handle(id uint64, flags int) *guestpb.HomeHandle {
	return &guestpb.HomeHandle{Id: id, Flags: uint32(flags)}
}

func TestServerKeepsEveryPathInsideItsShare(t *testing.T) {
	_, s := fixture(t)
	ctx := t.Context()
	for p, want := range map[string]syscall.Errno{
		"0/a.txt": 0, "1": 0, "0/link": 0,
		"0/../outside/secret": unix.ENOENT, "0/dir/../../outside": unix.ENOENT, "/etc/passwd": unix.ENOENT, "0//a.txt": unix.ENOENT,
		"0/./a.txt": unix.ENOENT, "00/a.txt": unix.ENOENT, "4/a.txt": unix.ENOENT, "0/": unix.ENOENT,
		"0/link/secret": unix.ELOOP, "3": unix.EXDEV,
	} {
		r, _ := s.Lookup(ctx, &guestpb.HomeNodeRequest{Path: p})
		if got := syscall.Errno(r.GetErrno()); got != want {
			t.Errorf("lookup %q = %v, want %v", p, got, want)
		}
	}
	if r, _ := s.Readlink(ctx, &guestpb.HomeNodeRequest{Path: "0/link"}); r.GetErrno() != 0 || len(r.GetTarget()) == 0 {
		t.Errorf("readlink = %+v", r)
	}
	for _, p := range []string{"0/link", "0/escape"} {
		if r, _ := s.Open(ctx, &guestpb.HomeOpenRequest{Path: p, Handle: handle(1, unix.O_RDONLY)}); syscall.Errno(r.GetErrno()) != unix.ELOOP {
			t.Errorf("open of the symlink %s = %v, want ELOOP", p, syscall.Errno(r.GetErrno()))
		}
	}
	if r, _ := s.Open(ctx, &guestpb.HomeOpenRequest{Path: "0/fifo", Handle: handle(2, unix.O_RDONLY)}); syscall.Errno(r.GetErrno()) != unix.ENXIO {
		t.Errorf("open of a FIFO = %v, want ENXIO", syscall.Errno(r.GetErrno()))
	}
	root, _ := s.Readdir(ctx, &guestpb.HomeNodeRequest{})
	if len(root.GetEntries()) != 4 || root.GetEntries()[1].GetMode() != unix.S_IFREG {
		t.Errorf("root listing = %+v", root.GetEntries())
	}
}

func TestServerNeverWritesAReadOnlyShare(t *testing.T) {
	dir, s := fixture(t)
	ctx := t.Context()
	mode := uint32(0o777)
	for name, errno := range map[string]int32{
		"open": func() int32 {
			r, _ := s.Open(ctx, &guestpb.HomeOpenRequest{Path: "2/a.txt", Handle: handle(1, unix.O_WRONLY)})
			return r.GetErrno()
		}(),
		"create": func() int32 {
			r, _ := s.Open(ctx, &guestpb.HomeOpenRequest{Path: "2/new", Handle: handle(2, unix.O_RDONLY), Create: true})
			return r.GetErrno()
		}(),
		"mknod": func() int32 {
			r, _ := s.Mknod(ctx, &guestpb.HomeMknodRequest{Path: "2/new", Mode: unix.S_IFDIR | 0o755})
			return r.GetErrno()
		}(),
		"setattr": func() int32 {
			r, _ := s.Setattr(ctx, &guestpb.HomeSetattrRequest{Path: "2/a.txt", Mode: &mode})
			return r.GetErrno()
		}(),
		"remove": func() int32 {
			r, _ := s.Remove(ctx, &guestpb.HomeRemoveRequest{Path: "2/a.txt"})
			return r.GetErrno()
		}(),
		"rename": func() int32 {
			r, _ := s.Rename(ctx, &guestpb.HomeRenameRequest{Path: "2/a.txt", NewPath: "2/b.txt"})
			return r.GetErrno()
		}(),
		"write through an unknown handle": func() int32 {
			r, _ := s.Write(ctx, &guestpb.HomeWriteRequest{Path: "2/a.txt", Handle: handle(3, unix.O_RDWR), Data: []byte("x")})
			return r.GetErrno()
		}(),
	} {
		if syscall.Errno(errno) != unix.EROFS {
			t.Errorf("%s on a read-only share = %v, want EROFS", name, syscall.Errno(errno))
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "home/a.txt")); string(data) != "hello" {
		t.Errorf("a read-only share was written: %q", data)
	}
	if r, _ := s.Open(ctx, &guestpb.HomeOpenRequest{Path: "2/a.txt", Handle: handle(4, unix.O_RDONLY)}); r.GetErrno() != 0 {
		t.Errorf("read-only open = %v", syscall.Errno(r.GetErrno()))
	}
}

func TestServerCreatesOnlyPlainFilesWithoutSetIDBits(t *testing.T) {
	dir, s := fixture(t)
	ctx := t.Context()
	if r, _ := s.Mknod(ctx, &guestpb.HomeMknodRequest{Path: "0/tool", Mode: unix.S_IFREG | 0o6755}); r.GetErrno() != 0 {
		t.Fatalf("mknod = %v", syscall.Errno(r.GetErrno()))
	}
	info, err := os.Stat(filepath.Join(dir, "home/tool"))
	if err != nil || info.Mode() != 0o755 {
		t.Errorf("created %v, %v; want a plain 0755 file", info.Mode(), err)
	}
	if r, _ := s.Open(ctx, &guestpb.HomeOpenRequest{Path: "0/dir/new", Handle: handle(1, unix.O_WRONLY), Create: true, Mode: 0o4640}); r.GetErrno() != 0 || r.GetAttr().GetMode() != unix.S_IFREG|0o640 {
		t.Errorf("create = %+v", r)
	}
	for name, mode := range map[string]uint32{"char": unix.S_IFCHR | 0o666, "block": unix.S_IFBLK | 0o666} {
		if r, _ := s.Mknod(ctx, &guestpb.HomeMknodRequest{Path: "0/" + name, Mode: mode}); syscall.Errno(r.GetErrno()) != unix.EPERM {
			t.Errorf("mknod of a %s device = %v, want EPERM", name, syscall.Errno(r.GetErrno()))
		}
	}
	if r, _ := s.Link(ctx, &guestpb.HomeLinkRequest{Path: "0/a.txt", NewPath: "2/a2"}); syscall.Errno(r.GetErrno()) != unix.EXDEV {
		t.Errorf("link across shares = %v, want EXDEV", syscall.Errno(r.GetErrno()))
	}
	if r, _ := s.Remove(ctx, &guestpb.HomeRemoveRequest{Path: "1"}); syscall.Errno(r.GetErrno()) != unix.EBUSY {
		t.Errorf("remove of a share's root = %v, want EBUSY", syscall.Errno(r.GetErrno()))
	}
	if r, _ := s.Mknod(ctx, &guestpb.HomeMknodRequest{Path: "x", Mode: unix.S_IFREG}); r.GetErrno() == 0 {
		t.Error("created a file in the service's root")
	}
}

func TestServerOpensAHandleAgainAfterARestart(t *testing.T) {
	dir, s := fixture(t)
	ctx := t.Context()
	rw := handle(7, unix.O_RDWR)
	if r, _ := s.Open(ctx, &guestpb.HomeOpenRequest{Path: "1", Handle: rw}); r.GetErrno() != 0 {
		t.Fatalf("open = %v", syscall.Errno(r.GetErrno()))
	}
	if r, _ := s.Write(ctx, &guestpb.HomeWriteRequest{Path: "1", Handle: rw, Data: []byte("token-2")}); r.GetWritten() != 7 {
		t.Fatalf("write = %+v", r)
	}
	restarted := newServer(Config{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Shares: []Share{{Source: filepath.Join(dir, "home")}}})
	t.Cleanup(restarted.close)
	if r, _ := restarted.Read(ctx, &guestpb.HomeReadRequest{Path: "0/dir/login.json", Handle: rw, Size: 64}); r.GetErrno() != 0 || string(r.GetData()) != "token-2" {
		t.Errorf("read through a handle the service never opened = %+v", r)
	}
	appends := []*guestpb.HomeHandle{handle(8, unix.O_WRONLY|unix.O_APPEND), handle(9, unix.O_WRONLY|unix.O_APPEND)}
	for _, h := range appends {
		if r, _ := restarted.Open(ctx, &guestpb.HomeOpenRequest{Path: "0/a.txt", Handle: h}); r.GetErrno() != 0 {
			t.Fatalf("open for append = %v", syscall.Errno(r.GetErrno()))
		}
	}
	for i, h := range appends {
		if r, _ := restarted.Write(ctx, &guestpb.HomeWriteRequest{Path: "0/a.txt", Handle: h, Data: []byte{'1' + byte(i)}}); r.GetErrno() != 0 {
			t.Fatalf("append = %v", syscall.Errno(r.GetErrno()))
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "home/a.txt")); string(data) != "hello12" {
		t.Errorf("two appends at the same stale offset left %q", data)
	}
	if err := os.Remove(filepath.Join(dir, "home/dir/login.json")); err != nil {
		t.Fatal(err)
	}
	again := newServer(Config{Shares: []Share{{Source: filepath.Join(dir, "home")}}})
	t.Cleanup(again.close)
	if r, _ := again.Read(ctx, &guestpb.HomeReadRequest{Path: "0/dir/login.json", Handle: rw, Size: 64}); syscall.Errno(r.GetErrno()) != unix.ESTALE {
		t.Errorf("read of a file deleted since the restart = %v, want ESTALE", syscall.Errno(r.GetErrno()))
	}
}

func TestServeAnswersOverASocket(t *testing.T) {
	dir, _ := fixture(t)
	l, err := net.Listen("unix", filepath.Join(dir, "home.sock"))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = Serve(l, Config{Shares: []Share{{Source: filepath.Join(dir, "home")}}}) }()
	t.Cleanup(func() { _ = l.Close() })
	conn, err := grpc.NewClient("unix://"+l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	r, err := guestpb.NewHomeClient(conn).Lookup(t.Context(), &guestpb.HomeNodeRequest{Path: "0/a.txt"})
	if err != nil || r.GetErrno() != 0 || r.GetAttr().GetSize() != 5 {
		t.Errorf("lookup over the socket = %+v, %v", r, err)
	}
}
