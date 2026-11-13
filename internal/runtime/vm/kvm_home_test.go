//go:build kvm

package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/3xDevOps/Aether/internal/guest/guestpb"
	"github.com/3xDevOps/Aether/internal/guest/home"
	"github.com/3xDevOps/Aether/internal/runtime"
)

const sessionUser = "1000:1000"

func kvmUserSession(t *testing.T, e *engine, key string, mounts ...runtime.Mount) time.Duration {
	t.Helper()
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, User: sessionUser, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key, "HOME": "/root"}, CPULimit: 1, MemoryLimitBytes: 512 << 20, Mounts: mounts,
	}
	start := time.Now()
	if _, err := e.create(t.Context(), spec.CreationKey, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}

// eventually runs script in the VM until its output holds want, and returns
// how long that took.
func eventually(t *testing.T, e *engine, key, script, want string) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		out, _ := vmRun(t, e, key, script)
		if strings.Contains(out, want) {
			return time.Since(start)
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("%s in %s: %q, want %q", script, key, out, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func writeHome(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestKVMHomeIsLiveAcrossVMsAndASave(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-home")
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	owner, recipient := filepath.Join(src, "owner"), filepath.Join(src, "recipient")
	writeHome(t, owner, map[string]string{".claude/.credentials.json": "token-1", ".claude/settings.json": "{}"})
	writeHome(t, recipient, map[string]string{".claude/settings.json": "{}"})
	login := runtime.Mount{HostPath: owner, Subpath: ".claude/.credentials.json", ContainerPath: "/root/.claude/.credentials.json"}
	loginInode := inode(t, filepath.Join(owner, login.Subpath))

	// A directory of nothing but sockets is forwarded, not shared; one
	// that loses its socket before a restore must not shift the shares.
	signing, coord := filepath.Join(src, "signing"), filepath.Join(src, "coord")
	writeHome(t, coord, map[string]string{"instructions.md": "coord-instructions"})
	if err := os.MkdirAll(signing, 0o755); err != nil {
		t.Fatal(err)
	}
	agent, err := net.Listen("unix", filepath.Join(signing, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	// An agent's databases are each session's own: a directory of the
	// session on the host, mounted over one in the home.
	states := map[string]runtime.Mount{}
	for _, key := range []string{"a", "b"} {
		dir := filepath.Join(src, "vm-state-"+key)
		for _, d := range []string{dir, filepath.Join(owner, ".codex/vm-sqlite")} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		states[key] = runtime.Mount{HostPath: dir, ContainerPath: "/root/.codex/vm-sqlite"}
	}
	took := kvmUserSession(t, e, "run-vmhome-a", runtime.Mount{HostPath: owner, ContainerPath: "/root"}, login, states["a"],
		runtime.Mount{HostPath: signing, ContainerPath: "/run/aether-signing", ReadOnly: true},
		runtime.Mount{HostPath: coord, ContainerPath: "/run/aether", ReadOnly: true})
	t.Logf("create and start with the home took %s", took)
	kvmUserSession(t, e, "run-vmhome-b", runtime.Mount{HostPath: owner, ContainerPath: "/root"}, login, states["b"])
	if out, code := vmRun(t, e, "run-vmhome-a", "echo a > /root/.codex/vm-sqlite/state_5.sqlite && stat -c '%u %a' /root/.codex/vm-sqlite"); code != 0 || !strings.Contains(out, "1000 700") {
		t.Errorf("the session's own database directory: %d %q", code, out)
	}
	if out, _ := vmRun(t, e, "run-vmhome-b", "ls -A /root/.codex/vm-sqlite | wc -l"); strings.TrimSpace(out) != "0" {
		t.Errorf("another session sees the first one's databases: %q", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(owner, ".codex/vm-sqlite")); len(entries) != 0 {
		t.Errorf("a session's databases reached the home: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(states["a"].HostPath, "state_5.sqlite")); err != nil {
		t.Errorf("the session's databases are not in its own host directory: %v", err)
	}
	kvmUserSession(t, e, "run-vmhome-c", runtime.Mount{HostPath: recipient, ContainerPath: "/root"}, login)

	if out, code := vmRun(t, e, "run-vmhome-a", "id -u; stat -c '%u %a' /root; echo hello > /root/notes.txt && stat -c %u /root/notes.txt"); code != 0 || !strings.Contains(out, "1000\n1000 755\n1000") {
		t.Fatalf("the session user's home: %d %q", code, out)
	}
	t.Logf("a write in one VM showed in the other after %s", eventually(t, e, "run-vmhome-b", "cat /root/notes.txt", "hello"))

	if out, code := vmRun(t, e, "run-vmhome-a", "printf token-2 > /root/.claude/.credentials.json && echo token-3 > /root/t && mv /root/t /root/.claude/.credentials.json; echo mv=$?"); code != 0 || !strings.Contains(out, "mv=1") {
		t.Errorf("refresh in place, then a rename over the login: %d %q", code, out)
	}
	for _, key := range []string{"run-vmhome-b", "run-vmhome-c"} {
		t.Logf("the refreshed login showed in %s after %s", key, eventually(t, e, key, "cat /root/.claude/.credentials.json", "token-2"))
	}
	if data, _ := os.ReadFile(filepath.Join(owner, login.Subpath)); string(data) != "token-2" || inode(t, filepath.Join(owner, login.Subpath)) != loginInode {
		t.Errorf("the owner's login on the host is %q, inode changed: %v", data, inode(t, filepath.Join(owner, login.Subpath)) != loginInode)
	}
	if data, _ := os.ReadFile(filepath.Join(recipient, ".claude/.credentials.json")); len(data) != 0 {
		t.Errorf("the refresh reached the recipient's own home: %q", data)
	}

	held := "mkdir -p /root/work && cd /root/work && { setsid sh -c 'exec 3>>/root/held.log; while :; do echo tick >&3; sleep 0.2; done' </dev/null >/dev/null 2>&1 & echo $!; }"
	pid, _ := vmRun(t, e, "run-vmhome-a", held)
	pid = strings.TrimSpace(pid)
	start := time.Now()
	if err := e.save(t.Context(), "run-vmhome-a"); err != nil {
		t.Fatal(err)
	}
	t.Logf("save with the home mounted took %s", time.Since(start))
	_ = agent.Close()
	size := func() int64 {
		info, err := os.Stat(filepath.Join(owner, "held.log"))
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	saved := size()
	time.Sleep(time.Second)
	if size() != saved {
		t.Error("a saved VM kept writing")
	}
	start = time.Now()
	if err := e.restore(t.Context(), "run-vmhome-a"); err != nil {
		t.Fatal(err)
	}
	t.Logf("restore took %s", time.Since(start))
	time.Sleep(2 * time.Second)
	if size() <= saved {
		t.Error("the process that held a home file open stopped writing after the restore")
	}
	if out, code := vmRun(t, e, "run-vmhome-a", "readlink /proc/"+pid+"/cwd && ls /proc/"+pid+"/cwd/ >/dev/null && cat /root/notes.txt /run/aether/instructions.md && echo again >> /root/notes.txt"); code != 0 || !strings.Contains(out, "/root/work\nhello\ncoord-instructions") {
		t.Errorf("the home after the restore: %d %q", code, out)
	}
	eventually(t, e, "run-vmhome-b", "cat /root/notes.txt", "again")
}

// TestKVMHomeClientNow runs inside a guest, from the test binary: a process
// of the session user talks to the file service directly, as any process in
// the VM can, and tries to write the read-only home.
func TestKVMHomeClientNow(t *testing.T) {
	if os.Getenv("AETHER_VM_TEST_HOME_CLIENT") == "" {
		t.Skip("runs inside a guest only")
	}
	conn, err := grpc.NewClient("passthrough:///home", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return vsock.Dial(vsock.Host, home.Port, nil) }))
	if err != nil {
		t.Fatal(err)
	}
	api := guestpb.NewHomeClient(conn)
	if r, err := api.Lookup(t.Context(), &guestpb.HomeNodeRequest{Path: "0/.claude"}); err != nil || r.GetErrno() != 0 {
		t.Fatalf("lookup = %+v, %v", r, err)
	}
	r, err := api.Mknod(t.Context(), &guestpb.HomeMknodRequest{Path: "0/escaped", Mode: unix.S_IFREG | 0o644})
	if err != nil || syscall.Errno(r.GetErrno()) != unix.EROFS {
		t.Fatalf("mknod in the read-only home = %+v, %v", r, err)
	}
	o, err := api.Open(t.Context(), &guestpb.HomeOpenRequest{Path: "0/.claude/settings.json", Handle: &guestpb.HomeHandle{Id: 1, Flags: unix.O_WRONLY | unix.O_TRUNC}})
	if err != nil || syscall.Errno(o.GetErrno()) != unix.EROFS {
		t.Fatalf("open for writing in the read-only home = %+v, %v", o, err)
	}
	fmt.Println("home-client: refused")
}

func TestKVMIsolatedHomeWritesOnlyItsLogin(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-isolated")
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	memberHome := filepath.Join(src, "home")
	writeHome(t, memberHome, map[string]string{".claude/.credentials.json": "token-1", ".claude/settings.json": "{}"})
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	took := kvmUserSession(t, e, "run-vmhome-i",
		runtime.Mount{HostPath: memberHome, ContainerPath: "/root", Overlay: filepath.Join(src, "overlay")},
		runtime.Mount{HostPath: memberHome, Subpath: ".claude/.credentials.json", ContainerPath: "/root/.claude/.credentials.json"},
		runtime.Mount{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true},
	)
	t.Logf("create and start of an Isolated session took %s", took)
	script := "echo x > /root/new.txt && echo changed > /root/.claude/settings.json && printf token-2 > /root/.claude/.credentials.json && cat /root/new.txt /root/.claude/settings.json && stat -c '%u %a' /root"
	if out, code := vmRun(t, e, "run-vmhome-i", script); code != 0 || !strings.Contains(out, "x\nchanged\n1000 755") {
		t.Fatalf("writes in the Isolated home: %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(memberHome, "new.txt")); err == nil {
		t.Error("an Isolated session created a file in the real home")
	}
	if data, _ := os.ReadFile(filepath.Join(memberHome, ".claude/settings.json")); string(data) != "{}" {
		t.Errorf("an Isolated session changed the real settings: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(memberHome, ".claude/.credentials.json")); string(data) != "token-2" {
		t.Errorf("an Isolated session's login refresh did not reach the home: %q", data)
	}
	out, _ := vmRun(t, e, "run-vmhome-i", "AETHER_VM_TEST_HOME_CLIENT=1 /opt/aether-vmtest -test.run '^TestKVMHomeClientNow$' -test.v 2>&1")
	if !strings.Contains(out, "home-client: refused") {
		t.Errorf("a guest process talking to the file service directly: %s", out)
	}
	if _, err := os.Stat(filepath.Join(memberHome, "escaped")); err == nil {
		t.Error("a guest process wrote the real home through the file service")
	}
}

func fileSum(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestKVMCacheStartsFromTheMembersLastSession(t *testing.T) {
	e := kvmEngine(t)
	src := filepath.Join(e.root, "..", "src-cache")
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	pool := runtime.Mount{HostPath: filepath.Join(src, "pool"), ContainerPath: "/aether-cache", Kind: runtime.MountCache}
	if err := os.MkdirAll(pool.HostPath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(e.root, imagesDir, "caches", cachePool(pool))) })
	newest := func() string {
		name, _ := os.Readlink(filepath.Join(e.root, imagesDir, "caches", cachePool(pool), "current"))
		return name
	}
	extra := []runtime.Mount{
		pool,
		{HostPath: filepath.Join(src, "tmp"), ContainerPath: "/tmp", Kind: runtime.MountScratch},
		{HostPath: e.cfg.Helper, ContainerPath: "/usr/local/bin/aether-internal", ReadOnly: true, Kind: runtime.MountServerBinary},
	}
	took := kvmUserSession(t, e, "run-vmcache-a", extra...)
	t.Logf("create and start of a session with a new cache took %s", took)
	first := newest()
	out, code := vmRun(t, e, "run-vmcache-a", "mkdir -p /aether-cache/go-build && echo built > /aether-cache/go-build/x && stat -c %a /tmp && findmnt -n -o SOURCE /tmp && sha256sum /usr/local/bin/aether-internal")
	if code != 0 || !strings.Contains(out, "1777\n/dev/vd") || !strings.Contains(out, fileSum(t, e.cfg.Helper)) {
		t.Fatalf("cache, /tmp and the server binary: %d %q", code, out)
	}
	start := time.Now()
	if err := e.stop(t.Context(), "run-vmcache-a", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Logf("stop, with the cache copied for promotion, took %s", time.Since(start))
	for newest() == first {
		if time.Since(start) > 2*time.Minute {
			t.Fatal("the stopped session's cache never became the pool's newest")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("the cache was promoted %s after the stop began", time.Since(start))

	took = kvmUserSession(t, e, "run-vmcache-b", extra...)
	t.Logf("create and start of a session over a promoted cache took %s", took)
	if out, _ := vmRun(t, e, "run-vmcache-b", "cat /aether-cache/go-build/x"); !strings.Contains(out, "built") {
		t.Errorf("the next session's cache: %q", out)
	}

	promoted := newest()
	kvmUserSession(t, e, "run-vmcache-s", pool)
	vmRun(t, e, "run-vmcache-s", "head -c 1048576 /dev/urandom > /aether-cache/saved && echo marker > /aether-cache/saved-marker")
	if err := e.save(t.Context(), "run-vmcache-s"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if newest() != promoted {
		t.Fatal("a save promoted the cache")
	}
	if err := e.destroy(t.Context(), "run-vmcache-s"); err != nil {
		t.Fatal(err)
	}
	for start = time.Now(); newest() == promoted; time.Sleep(100 * time.Millisecond) {
		if time.Since(start) > 2*time.Minute {
			t.Fatal("deleting a saved session never promoted its cache")
		}
	}
	kvmUserSession(t, e, "run-vmcache-c", pool)
	if out, _ := vmRun(t, e, "run-vmcache-c", "wc -c < /aether-cache/saved; cat /aether-cache/saved-marker"); !strings.Contains(out, "1048576\nmarker") {
		t.Errorf("the cache of a session deleted while saved lacks what it wrote just before the save: %q", out)
	}

	promoted = newest()
	isolated := pool
	isolated.Overlay = filepath.Join(src, "overlay")
	kvmUserSession(t, e, "run-vmcache-i", isolated)
	vmRun(t, e, "run-vmcache-i", "echo isolated > /aether-cache/y")
	if err := e.stop(t.Context(), "run-vmcache-i", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if newest() != promoted {
		t.Error("an Isolated session's cache was promoted")
	}
}
